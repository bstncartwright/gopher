package context

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/memory"
)

type ContextRequest struct {
	BaseSystemPrompt string
	Messages         []ai.Message
	Retrieved        []memory.MemoryRecord
	CurrentTask      string
	MaxTokens        int
	MaxMemories      int
}

type ContextBundle struct {
	SystemPrompt  string
	Messages      []ai.Message
	Sources       []memory.MemoryRecord
	TokenEstimate int
}

type Assembler interface {
	Build(ctx context.Context, input ContextRequest) (ContextBundle, error)
}

type AssemblerOptions struct {
	DefaultMaxTokens int
	SafetyMargin     int
	MaxMemoryRecords int
}

type DefaultAssembler struct {
	defaultMaxTokens int
	safetyMargin     int
	maxMemoryRecords int
}

func NewAssembler(opts AssemblerOptions) *DefaultAssembler {
	maxTokens := opts.DefaultMaxTokens
	if maxTokens <= 0 {
		maxTokens = 12000
	}
	safetyMargin := opts.SafetyMargin
	if safetyMargin <= 0 {
		safetyMargin = 512
	}
	maxMemories := opts.MaxMemoryRecords
	if maxMemories <= 0 {
		maxMemories = 10
	}
	return &DefaultAssembler{
		defaultMaxTokens: maxTokens,
		safetyMargin:     safetyMargin,
		maxMemoryRecords: maxMemories,
	}
}

func (a *DefaultAssembler) Build(ctx context.Context, input ContextRequest) (ContextBundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ContextBundle{}, ctx.Err()
	default:
	}

	basePrompt := strings.TrimSpace(input.BaseSystemPrompt)
	messages := cloneMessages(input.Messages)
	memories := cloneRecords(input.Retrieved)

	maxTokens := input.MaxTokens
	if maxTokens <= 0 {
		maxTokens = a.defaultMaxTokens
	}
	maxMemories := input.MaxMemories
	if maxMemories <= 0 {
		maxMemories = a.maxMemoryRecords
	}

	tokensUsed := EstimateTextTokens(basePrompt)
	tokensUsed += EstimateTextTokens(input.CurrentTask)
	tokensUsed += EstimateMessagesTokens(messages)

	availableForMemory := maxTokens - tokensUsed - a.safetyMargin
	selected, memoryTokens := SelectMemoriesForBudget(memories, availableForMemory, maxMemories)

	systemPrompt := basePrompt
	if len(selected) > 0 {
		memorySection := renderMemorySection(selected)
		if strings.TrimSpace(memorySection) != "" {
			systemPrompt = strings.TrimSpace(basePrompt + "\n\n" + memorySection)
			tokensUsed += EstimateTextTokens(memorySection)
		}
	}
	tokensUsed += memoryTokens

	return ContextBundle{
		SystemPrompt:  systemPrompt,
		Messages:      messages,
		Sources:       selected,
		TokenEstimate: tokensUsed,
	}, nil
}

func renderMemorySection(records []memory.MemoryRecord) string {
	if len(records) == 0 {
		return ""
	}
	lines := []string{"### retrieved memory"}
	for _, record := range records {
		content := strings.Join(strings.Fields(strings.TrimSpace(record.Content)), " ")
		if content == "" {
			continue
		}
		if len(content) > 480 {
			content = content[:477] + "..."
		}
		metadata := formatMetadata(record.Metadata)
		lines = append(lines, fmt.Sprintf("- [%s | %s%s] %s", record.Type.String(), record.Scope, metadata, content))
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func formatMetadata(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", key, value))
	}
	if len(parts) == 0 {
		return ""
	}
	return " | " + strings.Join(parts, ",")
}

func cloneMessages(in []ai.Message) []ai.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]ai.Message, 0, len(in))
	for _, msg := range in {
		out = append(out, msg.Clone())
	}
	return out
}

func cloneRecords(in []memory.MemoryRecord) []memory.MemoryRecord {
	if len(in) == 0 {
		return nil
	}
	out := make([]memory.MemoryRecord, 0, len(in))
	for _, record := range in {
		copyRecord := record
		copyRecord.Metadata = cloneStringMap(record.Metadata)
		copyRecord.Embedding = cloneEmbedding(record.Embedding)
		out = append(out, copyRecord)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneEmbedding(in []float32) []float32 {
	if len(in) == 0 {
		return nil
	}
	out := make([]float32, len(in))
	copy(out, in)
	return out
}
