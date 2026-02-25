package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	"github.com/bstncartwright/gopher/pkg/memory"
)

type memoryExistence interface {
	Exists() (bool, error)
}

type providerContextBuildOptions struct {
	Mode                PromptMode
	CompactionSummaries []string
	OverflowRetries     int
	Warnings            []string
}

func (a *Agent) buildProviderContext(ctx context.Context, s *Session, userMessage string, modeOverride ...PromptMode) (ai.Context, error) {
	mode := PromptModeFull
	if len(modeOverride) > 0 {
		mode = normalizePromptMode(modeOverride[0])
	}
	out, _, err := a.buildProviderContextDetailedWithOptions(ctx, s, userMessage, providerContextBuildOptions{
		Mode: mode,
	})
	return out, err
}

func (a *Agent) buildProviderContextDetailed(ctx context.Context, s *Session, userMessage string, modeOverride ...PromptMode) (ai.Context, ctxbundle.ContextDiagnostics, error) {
	mode := PromptModeFull
	if len(modeOverride) > 0 {
		mode = normalizePromptMode(modeOverride[0])
	}
	return a.buildProviderContextDetailedWithOptions(ctx, s, userMessage, providerContextBuildOptions{Mode: mode})
}

func (a *Agent) buildProviderContextDetailedWithOptions(ctx context.Context, s *Session, userMessage string, opts providerContextBuildOptions) (ai.Context, ctxbundle.ContextDiagnostics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mode := normalizePromptMode(opts.Mode)

	working, err := a.Memory.LoadWorking()
	if err != nil {
		return ai.Context{}, ctxbundle.ContextDiagnostics{}, fmt.Errorf("load working memory: %w", err)
	}
	if working == nil {
		working = map[string]any{}
	}
	s.WorkingState = ai.CloneMap(working)

	includeWorking := true
	if store, ok := a.Memory.(memoryExistence); ok {
		exists, err := store.Exists()
		if err != nil {
			return ai.Context{}, ctxbundle.ContextDiagnostics{}, fmt.Errorf("check working memory: %w", err)
		}
		includeWorking = exists
	}

	contextFiles, err := loadBootstrapContextFiles(a.Workspace, mode, a.Config.BootstrapMaxChars, a.Config.BootstrapTotalMaxChars)
	if err != nil {
		return ai.Context{}, ctxbundle.ContextDiagnostics{}, fmt.Errorf("load bootstrap context files: %w", err)
	}

	skillsPrompt := ""
	if mode == PromptModeFull && hasTool(a.Tools, "read") {
		skillsPrompt = strings.TrimSpace(formatSkillsForPrompt(a.skills))
	}

	systemPrompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:      a.Workspace,
		AgentID:        a.ID,
		KnownAgents:    a.KnownAgents,
		PromptMode:     mode,
		Tools:          a.Tools,
		Policies:       a.Policies,
		SkillsPrompt:   skillsPrompt,
		ContextFiles:   contextFiles,
		IncludeWorking: includeWorking,
		Working:        working,
		UserTimezone:   a.Config.UserTimezone,
		Model:          a.model,
		Heartbeat:      a.Heartbeat,
	})
	if err != nil {
		return ai.Context{}, ctxbundle.ContextDiagnostics{}, fmt.Errorf("build system prompt: %w", err)
	}

	expandedUserMessage := expandSkillCommand(userMessage, a.skills)
	messages := cloneSessionMessages(s.Messages)
	userTimestamp := int64(0)
	if len(messages) > 0 {
		userTimestamp = messages[len(messages)-1].Timestamp + 1
	}
	messages = append(messages, ai.Message{
		Role:      ai.RoleUser,
		Content:   expandedUserMessage,
		Timestamp: userTimestamp,
	})

	var retrieved []memory.MemoryRecord
	if mode == PromptModeFull {
		retrieved = a.retrieveLongTermMemory(ctx, s, userMessage)
	}

	compactionSummaries := opts.CompactionSummaries
	if len(compactionSummaries) == 0 {
		compactionSummaries = append([]string(nil), s.CompactionSummaries...)
	}

	bootstrapTokens := estimateBootstrapTokens(contextFiles)
	workingTokens := estimateWorkingTokens(working, includeWorking)

	diagnostics := ctxbundle.ContextDiagnostics{
		ModelContextWindow: a.model.ContextWindow,
		BootstrapLane: ctxbundle.LaneDiagnostics{
			UsedTokens: bootstrapTokens,
			CapTokens:  a.model.ContextWindow,
		},
		WorkingMemoryLane: ctxbundle.LaneDiagnostics{
			UsedTokens: workingTokens,
			CapTokens:  a.model.ContextWindow,
		},
	}
	if a.Assembler != nil {
		assembled, err := a.Assembler.Build(ctx, ctxbundle.ContextRequest{
			BaseSystemPrompt:    systemPrompt,
			Messages:            messages,
			Retrieved:           retrieved,
			CompactionSummaries: compactionSummaries,
			CurrentTask:         expandedUserMessage,
			MaxTokens:           a.model.ContextWindow,
			EnablePruning:       a.Config.ContextManagement.PruningEnabled(),
			EnableCompaction:    a.Config.ContextManagement.CompactionEnabled(),
			BootstrapTokens:     bootstrapTokens,
			WorkingTokens:       workingTokens,
			OverflowRetries:     opts.OverflowRetries,
			Warnings:            opts.Warnings,
		})
		if err != nil {
			return ai.Context{}, ctxbundle.ContextDiagnostics{}, fmt.Errorf("assemble context: %w", err)
		}
		systemPrompt = assembled.SystemPrompt
		messages = assembled.Messages
		diagnostics = assembled.Diagnostics
		if strings.TrimSpace(assembled.NewCompactionSummary) != "" {
			s.CompactionSummaries = prependCompactionSummary(s.CompactionSummaries, assembled.NewCompactionSummary, 3)
		}
	} else if len(retrieved) > 0 {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + renderMemoryFallbackSection(retrieved))
	}

	diagnostics.BootstrapLane = ctxbundle.LaneDiagnostics{
		UsedTokens: bootstrapTokens,
		CapTokens:  a.model.ContextWindow,
	}
	diagnostics.WorkingMemoryLane = ctxbundle.LaneDiagnostics{
		UsedTokens: workingTokens,
		CapTokens:  a.model.ContextWindow,
	}
	if diagnostics.ModelContextWindow <= 0 {
		diagnostics.ModelContextWindow = a.model.ContextWindow
	}
	s.LastContextDiagnostics = diagnostics

	return ai.Context{
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        toolSchemasToAITools(a.Tools),
	}, diagnostics, nil
}

func (a *Agent) InspectContext(ctx context.Context, s *Session, in TurnInput) (ctxbundle.ContextDiagnostics, error) {
	if s == nil {
		return ctxbundle.ContextDiagnostics{}, fmt.Errorf("session is nil")
	}
	_, diagnostics, err := a.buildProviderContextDetailed(ctx, s, in.UserMessage, in.PromptMode)
	return diagnostics, err
}

func (a *Agent) retrieveLongTermMemory(ctx context.Context, s *Session, userMessage string) []memory.MemoryRecord {
	if a == nil || a.LongTermMemory == nil {
		return nil
	}

	scopes := []memory.MemoryScope{
		memory.ScopeGlobal,
		memory.AgentScope(a.ID),
		memory.SessionScope(s.ID),
	}
	projectName := strings.TrimSpace(filepath.Base(a.Workspace))
	if projectName != "" {
		scopes = append(scopes, memory.ProjectScope(projectName))
	}

	typeQuota := []struct {
		Type  memory.MemoryType
		Limit int
	}{
		{Type: memory.MemorySemantic, Limit: 3},
		{Type: memory.MemoryProcedural, Limit: 2},
		{Type: memory.MemoryEpisodic, Limit: 2},
		{Type: memory.MemoryTool, Limit: 1},
	}

	out := make([]memory.MemoryRecord, 0, 8)
	seen := map[string]struct{}{}
	for _, item := range typeQuota {
		records, err := a.LongTermMemory.Retrieve(ctx, memory.MemoryQuery{
			SessionID: s.ID,
			AgentID:   a.ID,
			Topic:     userMessage,
			Keywords:  memory.ExtractKeywords(userMessage, 10),
			Limit:     item.Limit,
			Scopes:    scopes,
			Types:     []memory.MemoryType{item.Type},
		})
		if err != nil {
			continue
		}
		for _, record := range records {
			key := dedupeMemoryKey(record)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, record)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.After(out[j].Timestamp)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func renderMemoryFallbackSection(records []memory.MemoryRecord) string {
	if len(records) == 0 {
		return ""
	}
	lines := []string{"### retrieved memory"}
	for _, record := range records {
		content := strings.Join(strings.Fields(strings.TrimSpace(record.Content)), " ")
		if content == "" {
			continue
		}
		if len(content) > 320 {
			content = content[:317] + "..."
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", record.Type.String(), content))
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func hasTool(registry ToolRegistry, name string) bool {
	if registry == nil {
		return false
	}
	_, ok := registry.Get(name)
	return ok
}

func marshalStableJSON(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func cloneSessionMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]Message, 0, len(in))
	for _, msg := range in {
		out = append(out, msg.Clone())
	}
	return out
}

func dedupeMemoryKey(record memory.MemoryRecord) string {
	id := strings.TrimSpace(record.ID)
	if id != "" {
		return id
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(strings.TrimSpace(record.Content)))
	_, _ = hash.Write([]byte(record.Type.String()))
	_, _ = hash.Write([]byte(strings.TrimSpace(record.AgentID)))
	return fmt.Sprintf("hash:%x", hash.Sum64())
}

func estimateBootstrapTokens(files []BootstrapContextFile) int {
	total := 0
	for _, file := range files {
		total += ctxbundle.EstimateTextTokens(file.Content)
	}
	return total
}

func estimateWorkingTokens(working map[string]any, includeWorking bool) int {
	if !includeWorking {
		return 0
	}
	blob, err := json.MarshalIndent(working, "", "  ")
	if err != nil {
		return 0
	}
	return ctxbundle.EstimateTextTokens(string(blob))
}

func prependCompactionSummary(in []string, summary string, max int) []string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return in
	}
	out := make([]string, 0, len(in)+1)
	out = append(out, summary)
	for _, existing := range in {
		existing = strings.TrimSpace(existing)
		if existing == "" || existing == summary {
			continue
		}
		out = append(out, existing)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}
