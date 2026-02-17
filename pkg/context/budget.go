package context

import (
	"encoding/json"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/memory"
)

const approxCharsPerToken = 4

func EstimateTextTokens(text string) int {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0
	}
	runes := []rune(trimmed)
	tokens := len(runes) / approxCharsPerToken
	if len(runes)%approxCharsPerToken != 0 {
		tokens++
	}
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

func EstimateMessageTokens(message ai.Message) int {
	tokens := 1
	if text, ok := message.ContentText(); ok {
		tokens += EstimateTextTokens(text)
		return tokens
	}
	if blocks, ok := message.ContentBlocks(); ok {
		for _, block := range blocks {
			tokens += EstimateTextTokens(block.Text)
			tokens += EstimateTextTokens(block.Thinking)
			tokens += EstimateTextTokens(block.Name)
			if len(block.Arguments) > 0 {
				blob, err := json.Marshal(block.Arguments)
				if err == nil {
					tokens += EstimateTextTokens(string(blob))
				}
			}
		}
		return tokens
	}
	if message.Content != nil {
		blob, err := json.Marshal(message.Content)
		if err == nil {
			tokens += EstimateTextTokens(string(blob))
		}
	}
	return tokens
}

func EstimateMessagesTokens(messages []ai.Message) int {
	total := 0
	for _, message := range messages {
		total += EstimateMessageTokens(message)
	}
	return total
}

func EstimateMemoryTokens(record memory.MemoryRecord) int {
	tokens := EstimateTextTokens(record.Content)
	tokens += EstimateTextTokens(record.Type.String())
	tokens += EstimateTextTokens(string(record.Scope))
	if len(record.Metadata) > 0 {
		blob, err := json.Marshal(record.Metadata)
		if err == nil {
			tokens += EstimateTextTokens(string(blob))
		}
	}
	return tokens + 8
}

func SelectMemoriesForBudget(memories []memory.MemoryRecord, availableTokens int, maxRecords int) ([]memory.MemoryRecord, int) {
	if availableTokens <= 0 || len(memories) == 0 {
		return nil, 0
	}
	if maxRecords <= 0 || maxRecords > len(memories) {
		maxRecords = len(memories)
	}

	selected := make([]memory.MemoryRecord, 0, maxRecords)
	used := 0
	for _, candidate := range memories {
		if len(selected) >= maxRecords {
			break
		}
		cost := EstimateMemoryTokens(candidate)
		if cost <= 0 {
			continue
		}
		if used+cost > availableTokens {
			continue
		}
		selected = append(selected, candidate)
		used += cost
	}
	return selected, used
}
