package context

import (
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type PruneOptions struct {
	MaxHistoricalToolResultChars int
}

func PruneMessages(messages []ai.Message, opts PruneOptions) ([]ai.Message, []string) {
	if len(messages) == 0 {
		return nil, nil
	}
	maxToolChars := opts.MaxHistoricalToolResultChars
	if maxToolChars <= 0 {
		maxToolChars = 240
	}

	out := make([]ai.Message, 0, len(messages))
	actions := make([]string, 0, 4)

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
		if pruned.Role == ai.RoleToolResult && idx < len(messages)-4 {
			if blocks, ok := pruned.ContentBlocks(); ok && len(blocks) > 0 {
				changed := false
				for blockIdx := range blocks {
					if blocks[blockIdx].Type != ai.ContentTypeText {
						continue
					}
					text := strings.TrimSpace(blocks[blockIdx].Text)
					if len(text) <= maxToolChars {
						continue
					}
					blocks[blockIdx].Text = text[:maxToolChars] + "... (truncated)"
					changed = true
				}
				if changed {
					pruned.Content = blocks
					actions = append(actions, fmt.Sprintf("truncated historical tool result payload at index %d", idx))
				}
			}
		}

		out = append(out, pruned)
	}

	return out, actions
}
