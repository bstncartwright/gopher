package agentcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	"github.com/bstncartwright/gopher/pkg/memory"
)

type memoryExistence interface {
	Exists() (bool, error)
}

type providerContextBuildOptions struct {
	Mode                         PromptMode
	CompactionSummaries          []string
	OverflowRetries              int
	OverflowStage                string
	MaxMemories                  int
	DisableRetrievedMemory       bool
	EnableModelCompactionSummary bool
	Warnings                     []string
}

func (a *Agent) buildProviderContext(ctx context.Context, s *Session, userMessage string, modeOverride ...PromptMode) (ai.Context, error) {
	mode := PromptModeFull
	if len(modeOverride) > 0 {
		mode = normalizePromptMode(modeOverride[0])
	}
	out, _, err := a.buildProviderContextDetailedWithAttachments(ctx, s, userMessage, nil, providerContextBuildOptions{
		Mode:                         mode,
		EnableModelCompactionSummary: true,
	})
	return out, err
}

func (a *Agent) buildProviderContextDetailed(ctx context.Context, s *Session, userMessage string, modeOverride ...PromptMode) (ai.Context, ctxbundle.ContextDiagnostics, error) {
	mode := PromptModeFull
	if len(modeOverride) > 0 {
		mode = normalizePromptMode(modeOverride[0])
	}
	return a.buildProviderContextDetailedWithAttachments(ctx, s, userMessage, nil, providerContextBuildOptions{
		Mode:                         mode,
		EnableModelCompactionSummary: true,
	})
}

func (a *Agent) buildProviderContextDetailedWithOptions(ctx context.Context, s *Session, userMessage string, opts providerContextBuildOptions) (ai.Context, ctxbundle.ContextDiagnostics, error) {
	return a.buildProviderContextDetailedWithAttachments(ctx, s, userMessage, nil, opts)
}

func (a *Agent) buildProviderContextDetailedWithAttachments(ctx context.Context, s *Session, userMessage string, attachments []Attachment, opts providerContextBuildOptions) (ai.Context, ctxbundle.ContextDiagnostics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mode := normalizePromptMode(opts.Mode)
	activeTools := activeToolRegistry(a.Tools, ToolInput{Agent: a, Session: s})

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
	if mode == PromptModeFull && hasTool(activeTools, "read") {
		skillsPrompt = strings.TrimSpace(formatSkillsForPrompt(a.skills))
	}

	systemPrompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:      a.Workspace,
		AgentID:        a.ID,
		KnownAgents:    a.KnownAgents,
		PromptMode:     mode,
		Tools:          activeTools,
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
	userContent := any(expandedUserMessage)
	if len(attachments) > 0 {
		userContent = buildInboundAttachmentContent(expandedUserMessage, attachments, a.model)
	}
	messages = append(messages, ai.Message{
		Role:      ai.RoleUser,
		Content:   userContent,
		Timestamp: userTimestamp,
	})

	// Retrieved-memory injection is intentionally disabled for all agents.
	// Memory context should come from auto-loaded files, and deeper lookup
	// should be explicit via memory_search/memory_get tools.
	var retrieved []memory.MemoryRecord

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
		maxMemories := opts.MaxMemories
		if maxMemories <= 0 {
			maxMemories = 8
		}
		enableRepair := a.Config.ContextManagement.ModeValue() == "safeguard"
		summaryBuilder := ctxbundle.CompactionSummaryBuilder(nil)
		if opts.EnableModelCompactionSummary && a.Config.ContextManagement.ModelCompactionSummaryEnabled() {
			summaryBuilder = a.newModelCompactionSummaryBuilder(s)
		}
		assembled, err := a.Assembler.Build(ctx, ctxbundle.ContextRequest{
			BaseSystemPrompt:           systemPrompt,
			Messages:                   messages,
			Retrieved:                  retrieved,
			CompactionSummaries:        compactionSummaries,
			CurrentTask:                expandedUserMessage,
			MaxTokens:                  a.model.ContextWindow,
			MaxMemories:                maxMemories,
			ReserveMinTokens:           a.Config.ContextManagement.ReserveMinTokensValue(),
			EnablePruning:              a.Config.ContextManagement.PruningEnabled(),
			EnableRepair:               enableRepair,
			EnableCompaction:           a.Config.ContextManagement.CompactionEnabled(),
			PruneOptions:               ctxbundle.PruneOptions{},
			BootstrapTokens:            bootstrapTokens,
			WorkingTokens:              workingTokens,
			RetrievedMemoryLanePercent: a.Config.ContextManagement.RetrievedMemoryLanePercentValue(),
			OverflowRetries:            opts.OverflowRetries,
			OverflowStage:              opts.OverflowStage,
			Warnings:                   opts.Warnings,
			CompactionSummaryBuilder:   summaryBuilder,
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
	if a.MemorySearch != nil {
		if status, err := a.MemorySearch.Status(ctx); err == nil {
			diagnostics.MemorySearchMode = status.RetrievalMode()
			diagnostics.MemoryProvider = status.Provider
			diagnostics.MemoryFallbackReason = status.FallbackReason
			diagnostics.MemoryUnavailableReason = status.UnavailableReason
		}
	}
	s.LastContextDiagnostics = diagnostics

	return ai.Context{
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        toolSchemasToAITools(activeTools),
	}, diagnostics, nil
}

func buildInboundAttachmentContent(text string, attachments []Attachment, model ai.Model) any {
	if len(attachments) == 0 {
		return text
	}

	blocks := make([]ai.ContentBlock, 0, 1+len(attachments)*2)
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, ai.ContentBlock{Type: ai.ContentTypeText, Text: text})
	}
	for _, attachment := range attachments {
		blocks = append(blocks, attachmentContentBlocks(attachment, model)...)
	}
	if len(blocks) == 0 {
		return text
	}
	return blocks
}

func attachmentContentBlocks(attachment Attachment, model ai.Model) []ai.ContentBlock {
	mimeType := strings.ToLower(strings.TrimSpace(attachment.MIMEType))
	if strings.HasPrefix(mimeType, "image/") && len(attachment.Data) > 0 && modelSupportsInput(model, "image") {
		blocks := []ai.ContentBlock{{
			Type:     ai.ContentTypeImage,
			MimeType: mimeType,
			Data:     base64.StdEncoding.EncodeToString(attachment.Data),
		}}
		if text := strings.TrimSpace(attachment.Text); text != "" {
			blocks = append(blocks, ai.ContentBlock{Type: ai.ContentTypeText, Text: formatInboundAttachmentText(attachment, text)})
		}
		return blocks
	}
	if text := strings.TrimSpace(attachment.Text); text != "" {
		return []ai.ContentBlock{{Type: ai.ContentTypeText, Text: formatInboundAttachmentText(attachment, text)}}
	}
	return []ai.ContentBlock{{Type: ai.ContentTypeText, Text: formatInboundAttachmentSummary(attachment)}}
}

func formatInboundAttachmentSummary(attachment Attachment) string {
	label := "file"
	mimeType := strings.ToLower(strings.TrimSpace(attachment.MIMEType))
	switch {
	case strings.HasPrefix(mimeType, "image/"):
		label = "image"
	case strings.HasPrefix(mimeType, "audio/"):
		label = "audio"
	case strings.HasPrefix(mimeType, "video/"):
		label = "video"
	case mimeType == "application/pdf":
		label = "pdf"
	case mimeType != "":
		label = mimeType
	}

	name := strings.TrimSpace(attachment.Name)
	switch {
	case name != "" && mimeType != "":
		return fmt.Sprintf("Attached %s: %s (%s).", label, name, mimeType)
	case name != "":
		return fmt.Sprintf("Attached %s: %s.", label, name)
	case mimeType != "":
		return fmt.Sprintf("Attached %s (%s).", label, mimeType)
	default:
		return fmt.Sprintf("Attached %s.", label)
	}
}

func formatInboundAttachmentText(attachment Attachment, text string) string {
	summary := formatInboundAttachmentSummary(attachment)
	return summary + "\n\n" + text
}

func modelSupportsInput(model ai.Model, inputType string) bool {
	inputType = strings.TrimSpace(strings.ToLower(inputType))
	if inputType == "" {
		return false
	}
	for _, item := range model.Input {
		if strings.EqualFold(strings.TrimSpace(item), inputType) {
			return true
		}
	}
	return false
}

func (a *Agent) InspectContext(ctx context.Context, s *Session, in TurnInput) (ctxbundle.ContextDiagnostics, error) {
	if s == nil {
		return ctxbundle.ContextDiagnostics{}, fmt.Errorf("session is nil")
	}
	_, diagnostics, err := a.buildProviderContextDetailed(ctx, s, in.UserMessage, in.PromptMode)
	return diagnostics, err
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
		citation := strings.TrimSpace(record.Metadata["citation"])
		if citation != "" {
			lines = append(lines, fmt.Sprintf("- [%s | %s] %s", record.Type.String(), citation, content))
			continue
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
