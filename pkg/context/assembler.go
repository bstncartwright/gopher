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
	BaseSystemPrompt         string
	Messages                 []ai.Message
	Retrieved                []memory.MemoryRecord
	CompactionSummaries      []string
	CurrentTask              string
	MaxTokens                int
	MaxMemories              int
	ReserveMinTokens         int
	EnablePruning            bool
	EnableRepair             bool
	EnableCompaction         bool
	PruneOptions             PruneOptions
	BootstrapTokens          int
	WorkingTokens            int
	OverflowRetries          int
	OverflowStage            string
	Warnings                 []string
	CompactionSummaryBuilder CompactionSummaryBuilder
}

type CompactionSummaryBuilder func(ctx context.Context, dropped []ai.Message, fallback func([]ai.Message) CompactionResult) (CompactionResult, string, error)

type ContextBundle struct {
	SystemPrompt         string
	Messages             []ai.Message
	Sources              []memory.MemoryRecord
	TokenEstimate        int
	Diagnostics          ContextDiagnostics
	NewCompactionSummary string
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
	compactionSummaries := cloneStrings(input.CompactionSummaries)

	maxTokens := input.MaxTokens
	if maxTokens <= 0 {
		maxTokens = a.defaultMaxTokens
	}
	maxMemories := input.MaxMemories
	if maxMemories <= 0 {
		maxMemories = a.maxMemoryRecords
	}

	diagnostics := ContextDiagnostics{
		ModelContextWindow: maxTokens,
		ReserveFloorTokens: input.ReserveMinTokens,
		ReserveTokens:      ComputeReserveTokens(maxTokens, input.ReserveMinTokens),
		OverflowRetries:    input.OverflowRetries,
		OverflowStage:      strings.TrimSpace(input.OverflowStage),
		BootstrapLane: LaneDiagnostics{
			UsedTokens: input.BootstrapTokens,
			CapTokens:  maxTokens,
		},
		WorkingMemoryLane: LaneDiagnostics{
			UsedTokens: input.WorkingTokens,
			CapTokens:  maxTokens,
		},
	}
	diagnostics.Warnings = append(diagnostics.Warnings, cloneStrings(input.Warnings)...)

	if input.EnablePruning {
		prunedResult := PruneMessagesDetailed(messages, input.PruneOptions)
		messages = prunedResult.Messages
		diagnostics.PruneActions = append(diagnostics.PruneActions, prunedResult.Actions...)
		diagnostics.ToolResultTruncation += prunedResult.ToolResultTruncations
	}
	if input.EnableRepair {
		repaired, repairActions := RepairMessages(messages, RepairOptions{})
		messages = repaired
		diagnostics.PairRepairActions = append(diagnostics.PairRepairActions, repairActions...)
	}

	usable := maxTokens - diagnostics.ReserveTokens
	if usable <= 0 {
		usable = maxTokens
	}
	if usable <= 0 {
		usable = a.defaultMaxTokens
	}

	recentCap := percentOf(usable, 45)
	memoryCap := percentOf(usable, 15)
	compactionCap := percentOf(usable, 8)

	if recentCap <= 0 {
		recentCap = usable
	}

	selectedCompactions, compactionSection, compactionTokens := selectCompactionSummariesForBudget(compactionSummaries, compactionCap)
	newCompaction := ""

	selectedMemories, memoryTokens := SelectMemoriesForBudget(memories, memoryCap, maxMemories)
	memorySection := renderMemorySection(selectedMemories)
	memorySectionTokens := EstimateTextTokens(memorySection)

	baseTokens := EstimateTextTokens(basePrompt)
	remainingForMessages := maxTokens - diagnostics.ReserveTokens - baseTokens - memorySectionTokens - compactionTokens - a.safetyMargin
	if remainingForMessages < 0 {
		// Shed compaction context first, then memory context.
		for remainingForMessages < 0 && len(selectedCompactions) > 0 {
			selectedCompactions = selectedCompactions[:len(selectedCompactions)-1]
			compactionSection = renderCompactionSection(selectedCompactions)
			compactionTokens = EstimateTextTokens(compactionSection)
			remainingForMessages = maxTokens - diagnostics.ReserveTokens - baseTokens - memorySectionTokens - compactionTokens - a.safetyMargin
		}
		for remainingForMessages < 0 && len(selectedMemories) > 0 {
			selectedMemories = selectedMemories[:len(selectedMemories)-1]
			memorySection = renderMemorySection(selectedMemories)
			memorySectionTokens = EstimateTextTokens(memorySection)
			remainingForMessages = maxTokens - diagnostics.ReserveTokens - baseTokens - memorySectionTokens - compactionTokens - a.safetyMargin
		}
	}
	if remainingForMessages < 0 {
		remainingForMessages = 0
	}

	messageBudget := recentCap
	if remainingForMessages < messageBudget {
		messageBudget = remainingForMessages
	}
	if remainingForMessages > messageBudget {
		messageBudget = remainingForMessages
	}

	selectedMessages, droppedMessages, messageTokens := SelectMessagesForBudget(messages, messageBudget)
	if input.EnableCompaction && len(droppedMessages) > 0 {
		compacted := BuildCompactionSummary(droppedMessages)
		strategy := "deterministic"
		if input.CompactionSummaryBuilder != nil {
			candidate, summaryStrategy, err := input.CompactionSummaryBuilder(ctx, droppedMessages, BuildCompactionSummary)
			if err != nil {
				diagnostics.Warnings = append(diagnostics.Warnings, "model compaction summary failed: "+err.Error())
			}
			if strings.TrimSpace(candidate.Summary) != "" {
				compacted = candidate
				if strings.TrimSpace(summaryStrategy) != "" {
					strategy = strings.TrimSpace(summaryStrategy)
				}
			}
		}
		if strings.TrimSpace(compacted.Summary) != "" {
			newCompaction = compacted.Summary
			selectedCompactions = prependSummary(selectedCompactions, newCompaction, 3)
			compactionSection = renderCompactionSection(selectedCompactions)
			compactionTokens = EstimateTextTokens(compactionSection)
			diagnostics.CompactionActions = append(diagnostics.CompactionActions, compacted.Actions...)
			diagnostics.SummaryStrategy = strategy
		}
	}
	if input.EnableRepair {
		repairedSelected, repairActions := RepairMessages(selectedMessages, RepairOptions{})
		if len(repairActions) > 0 {
			selectedMessages = repairedSelected
			messageTokens = EstimateMessagesTokens(selectedMessages)
			diagnostics.PairRepairActions = append(diagnostics.PairRepairActions, repairActions...)
		}
	}

	systemPrompt := basePrompt
	if strings.TrimSpace(compactionSection) != "" {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + compactionSection)
	}
	if strings.TrimSpace(memorySection) != "" {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + memorySection)
	}

	tokensUsed := EstimateTextTokens(systemPrompt) + messageTokens

	diagnostics.SystemLane = LaneDiagnostics{
		UsedTokens: EstimateTextTokens(basePrompt),
		CapTokens:  maxTokens - diagnostics.ReserveTokens,
	}
	diagnostics.RecentMessagesLane = LaneDiagnostics{
		UsedTokens: messageTokens,
		CapTokens:  messageBudget,
	}
	diagnostics.RetrievedMemoryLane = LaneDiagnostics{
		UsedTokens: memoryTokens,
		CapTokens:  memoryCap,
	}
	diagnostics.CompactionSummaryLane = LaneDiagnostics{
		UsedTokens: compactionTokens,
		CapTokens:  compactionCap,
	}
	diagnostics.EstimatedInputTokens = tokensUsed
	if diagnostics.EstimatedInputTokens > maxTokens-diagnostics.ReserveTokens {
		diagnostics.Warnings = append(diagnostics.Warnings, "estimated input tokens exceed non-reserve budget")
	}
	for _, record := range selectedMemories {
		diagnostics.SelectedMemoryIDs = append(diagnostics.SelectedMemoryIDs, record.ID)
		diagnostics.SelectedMemoryTypes = append(diagnostics.SelectedMemoryTypes, record.Type.String())
	}
	if len(droppedMessages) > 0 {
		diagnostics.Warnings = append(diagnostics.Warnings, fmt.Sprintf("dropped %d older messages for budget", len(droppedMessages)))
	}

	return ContextBundle{
		SystemPrompt:         systemPrompt,
		Messages:             selectedMessages,
		Sources:              selectedMemories,
		TokenEstimate:        tokensUsed,
		Diagnostics:          diagnostics,
		NewCompactionSummary: newCompaction,
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

func renderCompactionSection(summaries []string) string {
	if len(summaries) == 0 {
		return ""
	}
	lines := []string{"### compacted history"}
	for _, summary := range summaries {
		trimmed := strings.TrimSpace(summary)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 720 {
			trimmed = trimmed[:717] + "..."
		}
		lines = append(lines, "- "+strings.ReplaceAll(trimmed, "\n", " | "))
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func selectCompactionSummariesForBudget(summaries []string, capTokens int) ([]string, string, int) {
	if capTokens <= 0 || len(summaries) == 0 {
		return nil, "", 0
	}
	selected := make([]string, 0, len(summaries))
	used := 0
	for _, summary := range summaries {
		trimmed := strings.TrimSpace(summary)
		if trimmed == "" {
			continue
		}
		cost := EstimateTextTokens(trimmed)
		if used+cost > capTokens {
			continue
		}
		selected = append(selected, trimmed)
		used += cost
	}
	section := renderCompactionSection(selected)
	return selected, section, EstimateTextTokens(section)
}

func SelectMessagesForBudget(messages []ai.Message, availableTokens int) ([]ai.Message, []ai.Message, int) {
	if len(messages) == 0 {
		return nil, nil, 0
	}
	if availableTokens <= 0 {
		return selectLastMessageFallback(messages)
	}

	start := len(messages)
	used := 0
	for start > 0 {
		chunkStart := start - 1
		if messages[chunkStart].Role == ai.RoleToolResult && chunkStart > 0 && assistantHasToolCalls(messages[chunkStart-1]) {
			chunkStart--
		}
		chunk := messages[chunkStart:start]
		chunkTokens := EstimateMessagesTokens(chunk)
		if used+chunkTokens > availableTokens {
			break
		}
		used += chunkTokens
		start = chunkStart
	}
	if start == len(messages) {
		return selectLastMessageFallback(messages)
	}

	selected := cloneMessages(messages[start:])
	dropped := cloneMessages(messages[:start])
	return selected, dropped, used
}

func selectLastMessageFallback(messages []ai.Message) ([]ai.Message, []ai.Message, int) {
	idx := lastUserMessageIndex(messages)
	if idx < 0 {
		idx = len(messages) - 1
	}
	selected := []ai.Message{messages[idx].Clone()}
	dropped := make([]ai.Message, 0, len(messages)-1)
	dropped = append(dropped, cloneMessages(messages[:idx])...)
	if idx+1 < len(messages) {
		dropped = append(dropped, cloneMessages(messages[idx+1:])...)
	}
	return selected, dropped, EstimateMessagesTokens(selected)
}

func assistantHasToolCalls(msg ai.Message) bool {
	if msg.Role != ai.RoleAssistant {
		return false
	}
	blocks, ok := msg.ContentBlocks()
	if !ok {
		return false
	}
	for _, block := range blocks {
		if block.Type == ai.ContentTypeToolCall {
			return true
		}
	}
	return false
}

func lastUserMessageIndex(messages []ai.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == ai.RoleUser {
			return i
		}
	}
	return -1
}

func percentOf(value int, percentage int) int {
	if value <= 0 || percentage <= 0 {
		return 0
	}
	return (value * percentage) / 100
}

func prependSummary(summaries []string, summary string, max int) []string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return summaries
	}
	out := make([]string, 0, len(summaries)+1)
	out = append(out, summary)
	for _, item := range summaries {
		item = strings.TrimSpace(item)
		if item == "" || item == summary {
			continue
		}
		out = append(out, item)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
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

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
