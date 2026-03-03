package context

import (
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type RepairOptions struct {
	HistoricalWindowMessages int
}

func RepairMessages(messages []ai.Message, opts RepairOptions) ([]ai.Message, []string) {
	if len(messages) == 0 {
		return nil, nil
	}
	historicalWindow := opts.HistoricalWindowMessages
	if historicalWindow <= 0 {
		historicalWindow = 4
	}

	toolResultByID := map[string]struct{}{}
	toolResultByName := map[string]int{}
	for _, msg := range messages {
		if msg.Role != ai.RoleToolResult {
			continue
		}
		id := strings.TrimSpace(msg.ToolCallID)
		addToolCallIDVariants(toolResultByID, id)
		name := normalizeToolName(msg.ToolName)
		if name != "" {
			toolResultByName[name]++
		}
	}

	actions := make([]string, 0, 4)
	sanitized := make([]ai.Message, 0, len(messages))
	for idx, msg := range messages {
		cloned := msg.Clone()
		if cloned.Role == ai.RoleAssistant && idx < len(messages)-historicalWindow {
			if blocks, ok := cloned.ContentBlocks(); ok && len(blocks) > 0 {
				kept := make([]ai.ContentBlock, 0, len(blocks))
				removedNames := make([]string, 0, 2)
				for _, block := range blocks {
					if block.Type != ai.ContentTypeToolCall {
						kept = append(kept, block)
						continue
					}
					if toolCallHasResult(block, toolResultByID, toolResultByName) {
						kept = append(kept, block)
						continue
					}
					if name := strings.TrimSpace(block.Name); name != "" {
						removedNames = append(removedNames, name)
					}
				}
				if len(removedNames) > 0 {
					if len(kept) == 0 {
						kept = append(kept, ai.ContentBlock{Type: ai.ContentTypeText, Text: "(stale tool call removed)"})
					}
					cloned.Content = kept
					actions = append(actions, fmt.Sprintf("removed stale assistant tool calls at index %d (%s)", idx, strings.Join(uniqueTrimmed(removedNames), ", ")))
				}
			}
		}
		sanitized = append(sanitized, cloned)
	}

	toolCallByID := map[string]struct{}{}
	toolCallByName := map[string]struct{}{}
	for _, msg := range sanitized {
		if msg.Role != ai.RoleAssistant {
			continue
		}
		blocks, ok := msg.ContentBlocks()
		if !ok {
			continue
		}
		for _, block := range blocks {
			if block.Type != ai.ContentTypeToolCall {
				continue
			}
			if id := strings.TrimSpace(block.ID); id != "" {
				addToolCallIDVariants(toolCallByID, id)
			}
			if name := normalizeToolName(block.Name); name != "" {
				toolCallByName[name] = struct{}{}
			}
		}
	}

	repaired := make([]ai.Message, 0, len(sanitized))
	for idx, msg := range sanitized {
		if msg.Role != ai.RoleToolResult {
			repaired = append(repaired, msg)
			continue
		}
		id := strings.TrimSpace(msg.ToolCallID)
		name := normalizeToolName(msg.ToolName)
		matched := false
		if id != "" {
			matched = hasAnyToolCallID(toolCallByID, id)
		} else if name != "" {
			_, matched = toolCallByName[name]
		}
		if matched {
			repaired = append(repaired, msg)
			continue
		}
		actions = append(actions, fmt.Sprintf("dropped orphan tool result at index %d", idx))
	}

	return repaired, actions
}

func toolCallHasResult(block ai.ContentBlock, toolResultByID map[string]struct{}, toolResultByName map[string]int) bool {
	id := strings.TrimSpace(block.ID)
	if id != "" {
		return hasAnyToolCallID(toolResultByID, id)
	}
	name := normalizeToolName(block.Name)
	if name != "" && toolResultByName[name] > 0 {
		return true
	}
	return false
}

func addToolCallIDVariants(set map[string]struct{}, id string) {
	for _, candidate := range toolCallIDVariants(id) {
		set[candidate] = struct{}{}
	}
}

func hasAnyToolCallID(set map[string]struct{}, id string) bool {
	for _, candidate := range toolCallIDVariants(id) {
		if _, exists := set[candidate]; exists {
			return true
		}
	}
	return false
}

func toolCallIDVariants(id string) []string {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil
	}
	variants := []string{trimmed}
	if strings.Contains(trimmed, "|") {
		callID := strings.TrimSpace(strings.SplitN(trimmed, "|", 2)[0])
		if callID != "" && callID != trimmed {
			variants = append(variants, callID)
		}
	}
	return variants
}

func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
