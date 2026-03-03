package context

import (
	"fmt"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type PruneOptions struct{}

func PruneMessages(messages []ai.Message, opts PruneOptions) ([]ai.Message, []string) {
	result := PruneMessagesDetailed(messages, opts)
	return result.Messages, result.Actions
}

type PruneResult struct {
	Messages              []ai.Message
	Actions               []string
	ToolResultTruncations int
}

func PruneMessagesDetailed(messages []ai.Message, _ PruneOptions) PruneResult {
	if len(messages) == 0 {
		return PruneResult{}
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

		out = append(out, pruned)
	}

	return PruneResult{
		Messages:              out,
		Actions:               actions,
		ToolResultTruncations: toolResultTruncations,
	}
}
