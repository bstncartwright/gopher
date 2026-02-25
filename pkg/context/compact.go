package context

import (
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type CompactionResult struct {
	Summary string
	Actions []string
}

func BuildCompactionSummary(dropped []ai.Message) CompactionResult {
	if len(dropped) == 0 {
		return CompactionResult{}
	}

	userFacts := make([]string, 0, 3)
	assistantOutcomes := make([]string, 0, 3)
	pendingTools := make([]string, 0, 4)

	for _, msg := range dropped {
		switch msg.Role {
		case ai.RoleUser:
			if text, ok := msg.ContentText(); ok && strings.TrimSpace(text) != "" {
				userFacts = append(userFacts, squeezeForSummary(text))
				continue
			}
			if blocks, ok := msg.ContentBlocks(); ok {
				for _, block := range blocks {
					if block.Type == ai.ContentTypeText && strings.TrimSpace(block.Text) != "" {
						userFacts = append(userFacts, squeezeForSummary(block.Text))
					}
				}
			}
		case ai.RoleAssistant:
			if blocks, ok := msg.ContentBlocks(); ok {
				for _, block := range blocks {
					switch block.Type {
					case ai.ContentTypeText:
						if strings.TrimSpace(block.Text) != "" {
							assistantOutcomes = append(assistantOutcomes, squeezeForSummary(block.Text))
						}
					case ai.ContentTypeToolCall:
						if strings.TrimSpace(block.Name) != "" {
							pendingTools = append(pendingTools, strings.TrimSpace(block.Name))
						}
					}
				}
			}
		}
	}

	lines := []string{
		fmt.Sprintf("Compacted %d older messages.", len(dropped)),
	}
	if len(userFacts) > 0 {
		lines = append(lines, "Key user context: "+strings.Join(lastN(userFacts, 2), " | "))
	}
	if len(assistantOutcomes) > 0 {
		lines = append(lines, "Recent outcomes: "+strings.Join(lastN(assistantOutcomes, 2), " | "))
	}
	if len(pendingTools) > 0 {
		lines = append(lines, "Tool activity: "+strings.Join(uniqueTrimmed(lastN(pendingTools, 4)), ", "))
	}

	return CompactionResult{
		Summary: strings.Join(lines, "\n"),
		Actions: []string{"generated deterministic summary for dropped history"},
	}
}

func squeezeForSummary(value string) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(text) > 180 {
		text = text[:177] + "..."
	}
	return text
}

func lastN(items []string, n int) []string {
	if len(items) == 0 {
		return nil
	}
	if n <= 0 || len(items) <= n {
		out := make([]string, len(items))
		copy(out, items)
		return out
	}
	out := make([]string, n)
	copy(out, items[len(items)-n:])
	return out
}

func uniqueTrimmed(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
