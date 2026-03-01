package agentcore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
)

const (
	compactionChunkRenderMaxChars = 480
	compactionSummaryMaxChars     = 1200
)

func (a *Agent) newModelCompactionSummaryBuilder(s *Session) ctxbundle.CompactionSummaryBuilder {
	return func(ctx context.Context, dropped []ai.Message, fallback func([]ai.Message) ctxbundle.CompactionResult) (ctxbundle.CompactionResult, string, error) {
		return a.summarizeDroppedMessages(ctx, s, dropped)
	}
}

func (a *Agent) summarizeDroppedMessages(ctx context.Context, s *Session, dropped []ai.Message) (ctxbundle.CompactionResult, string, error) {
	if len(dropped) == 0 {
		return ctxbundle.CompactionResult{}, "deterministic", nil
	}
	if a == nil || !a.Config.ContextManagement.ModelCompactionSummaryEnabled() {
		return ctxbundle.BuildCompactionSummary(dropped), "deterministic", nil
	}

	timeout := time.Duration(a.Config.ContextManagement.CompactionSummaryTimeoutMSValue()) * time.Millisecond
	summaryCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		summaryCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	chunks := splitMessagesForCompactionSummary(dropped, a.Config.ContextManagement.CompactionChunkTokenTargetValue())
	if len(chunks) == 0 {
		return ctxbundle.BuildCompactionSummary(dropped), "deterministic_fallback", nil
	}

	chunkSummaries := make([]string, 0, len(chunks))
	for idx, chunk := range chunks {
		chunkText := renderMessagesForCompactionSummary(chunk)
		if strings.TrimSpace(chunkText) == "" {
			continue
		}
		prompt := fmt.Sprintf("Chunk %d/%d:\n%s", idx+1, len(chunks), chunkText)
		summary, err := a.runCompactionSummaryModelCall(summaryCtx, s, prompt)
		if err != nil {
			return ctxbundle.BuildCompactionSummary(dropped), "deterministic_fallback", err
		}
		chunkSummaries = append(chunkSummaries, summary)
	}
	if len(chunkSummaries) == 0 {
		return ctxbundle.BuildCompactionSummary(dropped), "deterministic_fallback", nil
	}

	finalSummary := strings.TrimSpace(strings.Join(chunkSummaries, "\n"))
	if len(chunkSummaries) > 1 {
		reduced, err := a.runCompactionSummaryModelCall(summaryCtx, s, "Merge these chunk summaries into one compact history summary:\n"+finalSummary)
		if err != nil {
			return ctxbundle.BuildCompactionSummary(dropped), "deterministic_fallback", err
		}
		finalSummary = reduced
	}
	finalSummary = squeezeCompactionSummary(finalSummary)
	if finalSummary == "" {
		return ctxbundle.BuildCompactionSummary(dropped), "deterministic_fallback", nil
	}

	return ctxbundle.CompactionResult{
		Summary: finalSummary,
		Actions: []string{"generated model-assisted summary for dropped history"},
	}, "model_assisted", nil
}

func (a *Agent) runCompactionSummaryModelCall(ctx context.Context, s *Session, chunkText string) (string, error) {
	if a == nil || a.Provider == nil {
		return "", fmt.Errorf("provider unavailable for compaction summary")
	}

	instructions := strings.TrimSpace(`You are compressing prior conversation history for context preservation.
Return concise bullet points only. Include:
- user goals and constraints
- key decisions and outcomes
- tool actions that still matter
Do not include chain-of-thought.`)
	conversation := ai.Context{
		SystemPrompt: instructions,
		Messages: []ai.Message{
			ai.NewUserTextMessage(strings.TrimSpace(chunkText)),
		},
	}

	stream := a.Provider.Stream(a.model, conversation, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			RequestContext: ctx,
			APIKey:         ai.GetEnvAPIKey(string(a.model.Provider)),
			SessionID:      compactionSummarySessionID(s),
		},
		Reasoning: a.Config.ReasoningLevelValue(),
	})
	if stream == nil {
		return "", fmt.Errorf("provider returned nil stream")
	}
	for range stream.Events() {
		// Drain stream events; summary is read from final result.
	}
	assistant, err := stream.Result(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(assistant.ErrorMessage) != "" || assistant.StopReason == ai.StopReasonError || assistant.StopReason == ai.StopReasonAborted {
		return "", fmt.Errorf("%s", strings.TrimSpace(assistant.ErrorMessage))
	}
	summary := squeezeCompactionSummary(extractText(assistant.Content))
	if summary == "" {
		return "", fmt.Errorf("empty compaction summary from model")
	}
	return summary, nil
}

func splitMessagesForCompactionSummary(messages []ai.Message, chunkTargetTokens int) [][]ai.Message {
	if len(messages) == 0 {
		return nil
	}
	if chunkTargetTokens <= 0 {
		chunkTargetTokens = 1800
	}
	chunks := make([][]ai.Message, 0, 4)
	current := make([]ai.Message, 0, 12)
	currentTokens := 0
	for _, msg := range messages {
		cost := ctxbundle.EstimateMessageTokens(msg)
		if cost <= 0 {
			cost = 1
		}
		if currentTokens > 0 && currentTokens+cost > chunkTargetTokens {
			chunks = append(chunks, current)
			current = make([]ai.Message, 0, 12)
			currentTokens = 0
		}
		current = append(current, msg.Clone())
		currentTokens += cost
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func renderMessagesForCompactionSummary(messages []ai.Message) string {
	if len(messages) == 0 {
		return ""
	}
	lines := make([]string, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case ai.RoleUser:
			lines = append(lines, "user: "+clipCompactionText(messageText(msg)))
		case ai.RoleAssistant:
			lines = append(lines, "assistant: "+clipCompactionText(assistantSummaryText(msg)))
		case ai.RoleToolResult:
			name := strings.TrimSpace(msg.ToolName)
			if name == "" {
				name = "tool"
			}
			lines = append(lines, "tool_result("+name+"): "+clipCompactionText(messageText(msg)))
		default:
			lines = append(lines, string(msg.Role)+": "+clipCompactionText(messageText(msg)))
		}
	}
	return strings.Join(lines, "\n")
}

func assistantSummaryText(msg ai.Message) string {
	blocks, ok := msg.ContentBlocks()
	if !ok || len(blocks) == 0 {
		return messageText(msg)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentTypeText:
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case ai.ContentTypeToolCall:
			name := strings.TrimSpace(block.Name)
			if name == "" {
				name = "tool"
			}
			parts = append(parts, "tool_call:"+name)
		}
	}
	return strings.Join(parts, " | ")
}

func messageText(msg ai.Message) string {
	if text, ok := msg.ContentText(); ok {
		return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	}
	blocks, ok := msg.ContentBlocks()
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case ai.ContentTypeText:
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		case ai.ContentTypeThinking:
			if thinking := strings.TrimSpace(block.Thinking); thinking != "" {
				parts = append(parts, thinking)
			}
		}
	}
	return strings.Join(parts, " | ")
}

func clipCompactionText(value string) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(text) > compactionChunkRenderMaxChars {
		text = text[:compactionChunkRenderMaxChars-3] + "..."
	}
	return text
}

func squeezeCompactionSummary(value string) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(text) > compactionSummaryMaxChars {
		text = text[:compactionSummaryMaxChars-3] + "..."
	}
	return text
}

func compactionSummarySessionID(s *Session) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s.ID)
}
