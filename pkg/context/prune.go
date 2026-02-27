package context

import (
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type PruneOptions struct {
	MaxHistoricalToolResultChars int
	MaxRecentToolResultChars     int
	RecentWindowMessages         int
}

func PruneMessages(messages []ai.Message, opts PruneOptions) ([]ai.Message, []string) {
	result := PruneMessagesDetailed(messages, opts)
	return result.Messages, result.Actions
}

type PruneResult struct {
	Messages              []ai.Message
	Actions               []string
	ToolResultTruncations int
}

func PruneMessagesDetailed(messages []ai.Message, opts PruneOptions) PruneResult {
	if len(messages) == 0 {
		return PruneResult{}
	}
	maxHistoricalToolChars := opts.MaxHistoricalToolResultChars
	if maxHistoricalToolChars <= 0 {
		maxHistoricalToolChars = 240
	}
	maxRecentToolChars := opts.MaxRecentToolResultChars
	if maxRecentToolChars <= 0 {
		maxRecentToolChars = 2400
	}
	recentWindowMessages := opts.RecentWindowMessages
	if recentWindowMessages <= 0 {
		recentWindowMessages = 4
	}

	out := make([]ai.Message, 0, len(messages))
	actions := make([]string, 0, 4)
	toolResultTruncations := 0

	for idx, msg := range messages {
		pruned := msg.Clone()

		if pruned.Role == ai.RoleAssistant {
			if blocks, ok := pruned.ContentBlocks(); ok && len(blocks) > 0 && idx < len(messages)-1 {
				changed := false
				kept := make([]ai.ContentBlock, 0, len(blocks))
				for _, block := range blocks {
					if block.Type == ai.ContentTypeThinking {
						changed = true
						continue
					}
					kept = append(kept, block)
				}
				if changed {
					if len(kept) == 0 {
						kept = append(kept, ai.ContentBlock{Type: ai.ContentTypeText, Text: "(thinking removed)"})
					}
					pruned.Content = kept
					actions = append(actions, fmt.Sprintf("removed thinking blocks from assistant message at index %d", idx))
				}
			}
		}

		// Truncate older tool payloads to keep tool state but reduce noisy text.
		if pruned.Role == ai.RoleToolResult {
			if blocks, ok := pruned.ContentBlocks(); ok && len(blocks) > 0 {
				changed := false
				isHistorical := idx < len(messages)-recentWindowMessages
				maxChars := maxRecentToolChars
				actionPrefix := "truncated recent tool result payload"
				if isHistorical {
					maxChars = maxHistoricalToolChars
					actionPrefix = "truncated historical tool result payload"
				}
				for blockIdx := range blocks {
					if blocks[blockIdx].Type != ai.ContentTypeText {
						continue
					}
					text := strings.TrimSpace(blocks[blockIdx].Text)
					if len(text) <= maxChars {
						continue
					}
					blocks[blockIdx].Text = text[:maxChars] + "... (truncated)"
					changed = true
				}
				if changed {
					pruned.Content = blocks
					toolResultTruncations++
					actions = append(actions, fmt.Sprintf("%s at index %d", actionPrefix, idx))
				}
			}
		}

		out = append(out, pruned)
	}

	return PruneResult{
		Messages:              out,
		Actions:               actions,
		ToolResultTruncations: toolResultTruncations,
	}
}
