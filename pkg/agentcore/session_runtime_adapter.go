package agentcore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type SessionRuntimeAdapter struct {
	agent *Agent
	opts  SessionRuntimeAdapterOptions

	mu       sync.Mutex
	sessions map[sessionrt.SessionID]*Session
}

type SessionRuntimeAdapterOptions struct {
	CaptureDeltas   bool
	CaptureThinking bool
}

var _ sessionrt.StreamingAgentExecutor = (*SessionRuntimeAdapter)(nil)

var sessionRuntimeTurnTimeout = 5 * time.Minute

func NewSessionRuntimeAdapter(agent *Agent) *SessionRuntimeAdapter {
	return NewSessionRuntimeAdapterWithOptions(agent, SessionRuntimeAdapterOptions{})
}

func NewSessionRuntimeAdapterWithOptions(agent *Agent, opts SessionRuntimeAdapterOptions) *SessionRuntimeAdapter {
	return &SessionRuntimeAdapter{
		agent:    agent,
		opts:     opts,
		sessions: make(map[sessionrt.SessionID]*Session),
	}
}

func (a *SessionRuntimeAdapter) Step(ctx context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	userMsg, promptIdx, ok := latestPromptMessageWithIndex(input.History, input.ActorID)
	if !ok {
		return sessionrt.AgentOutput{}, nil
	}

	sessionData := a.getOrInitSession(input.SessionID, input.ActorID, input.History, promptIdx)

	turnCtx, cancel := withTurnTimeout(ctx)
	defer cancel()

	if strings.EqualFold(strings.TrimSpace(a.agent.Config.Runtime.Type), "acp") {
		response, err := a.runACPTurn(turnCtx, input, userMsg.Content)
		if err != nil {
			return sessionrt.AgentOutput{}, err
		}
		out := []sessionrt.Event{{
			Type: sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleAgent,
				Content: response,
			},
		}}
		if patch := a.buildStatePatch(sessionData); patch != nil {
			out = append(out, sessionrt.Event{Type: sessionrt.EventStatePatch, Payload: patch})
		}
		return sessionrt.AgentOutput{Events: out}, nil
	}

	result, err := a.agent.RunTurn(turnCtx, sessionData, TurnInput{
		UserMessage: userMsg.Content,
		Attachments: mapSessionAttachments(userMsg.Attachments),
		PromptMode:  PromptModeFull,
	})
	if err != nil {
		return sessionrt.AgentOutput{}, err
	}

	out := make([]sessionrt.Event, 0, len(result.Events))
	for _, event := range result.Events {
		mapped, ok := a.mapEvent(event)
		if !ok {
			continue
		}
		out = append(out, mapped)
	}
	if patch := a.buildStatePatch(sessionData); patch != nil {
		out = append(out, sessionrt.Event{
			Type:    sessionrt.EventStatePatch,
			Payload: patch,
		})
	}

	return sessionrt.AgentOutput{Events: out}, nil
}

func (a *SessionRuntimeAdapter) StepStream(ctx context.Context, input sessionrt.AgentInput, emit sessionrt.AgentEventEmitter) error {
	userMsg, promptIdx, ok := latestPromptMessageWithIndex(input.History, input.ActorID)
	if !ok {
		return nil
	}

	sessionData := a.getOrInitSession(input.SessionID, input.ActorID, input.History, promptIdx)

	turnCtx, cancel := withTurnTimeout(ctx)
	defer cancel()

	emitFn := emit
	if emitFn == nil {
		emitFn = func(sessionrt.Event) error { return nil }
	}

	if strings.EqualFold(strings.TrimSpace(a.agent.Config.Runtime.Type), "acp") {
		response, err := a.runACPTurn(turnCtx, input, userMsg.Content)
		if err != nil {
			return err
		}
		if err := emitFn(sessionrt.Event{
			Type: sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleAgent,
				Content: response,
			},
		}); err != nil {
			return err
		}
		if patch := a.buildStatePatch(sessionData); patch != nil {
			if emitErr := emitFn(sessionrt.Event{Type: sessionrt.EventStatePatch, Payload: patch}); emitErr != nil {
				return emitErr
			}
		}
		return nil
	}

	_, err := a.agent.RunTurnWithEventHandler(turnCtx, sessionData, TurnInput{
		UserMessage: userMsg.Content,
		Attachments: mapSessionAttachments(userMsg.Attachments),
		PromptMode:  PromptModeFull,
	}, func(event Event) error {
		mapped, ok := a.mapEvent(event)
		if !ok {
			return nil
		}
		return emitFn(mapped)
	})
	if err != nil {
		return err
	}
	if patch := a.buildStatePatch(sessionData); patch != nil {
		if emitErr := emitFn(sessionrt.Event{
			Type:    sessionrt.EventStatePatch,
			Payload: patch,
		}); emitErr != nil {
			return emitErr
		}
	}
	return err
}

func (a *SessionRuntimeAdapter) runACPTurn(ctx context.Context, input sessionrt.AgentInput, prompt string) (string, error) {
	runtimeCfg := a.agent.Config.Runtime
	acp := runtimeCfg.ACP
	timeout := time.Duration(acp.TimeoutSeconds) * time.Second
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	args := make([]string, 0, len(acp.Args))
	repl := strings.NewReplacer(
		"{agent}", strings.TrimSpace(acp.Agent),
		"{prompt}", prompt,
		"{session_id}", strings.TrimSpace(string(input.SessionID)),
		"{actor_id}", strings.TrimSpace(string(input.ActorID)),
	)
	for _, candidate := range acp.Args {
		args = append(args, repl.Replace(candidate))
	}
	cmd := exec.CommandContext(ctx, acp.Command, args...)
	cmd.Dir = strings.TrimSpace(a.agent.Workspace)
	if len(acp.Env) > 0 {
		env := os.Environ()
		for key, value := range acp.Env {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			env = append(env, key+"="+repl.Replace(value))
		}
		cmd.Env = env
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("acp runtime command failed (%s %s): %w: %s", acp.Command, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = strings.TrimSpace(stderr.String())
	}
	if out == "" {
		out = "ACP run completed with no output."
	}
	return out, nil
}

func withTurnTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionRuntimeTurnTimeout <= 0 {
		return ctx, func() {}
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, sessionRuntimeTurnTimeout)
}

func latestPromptMessage(events []sessionrt.Event, actorID sessionrt.ActorID) (sessionrt.Message, bool) {
	msg, _, ok := latestPromptMessageWithIndex(events, actorID)
	return msg, ok
}

func latestPromptMessageWithIndex(events []sessionrt.Event, actorID sessionrt.ActorID) (sessionrt.Message, int, bool) {
	targetActor := strings.TrimSpace(string(actorID))
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessionrt.EventMessage {
			continue
		}
		payload, ok := promptMessageFromPayload(event.Payload)
		if !ok {
			continue
		}
		if payload.Role == sessionrt.RoleUser {
			return payload, i, true
		}
		if payload.Role != sessionrt.RoleAgent {
			continue
		}
		target := strings.TrimSpace(string(payload.TargetActorID))
		if target == "" {
			continue
		}
		if targetActor != "" && target != targetActor {
			continue
		}
		return payload, i, true
	}
	return sessionrt.Message{}, -1, false
}

func promptMessageFromPayload(payload any) (sessionrt.Message, bool) {
	switch value := payload.(type) {
	case sessionrt.Message:
		return value, true
	case map[string]any:
		if _, ok := value["role"]; !ok {
			return sessionrt.Message{}, false
		}
		if _, ok := value["content"]; !ok {
			return sessionrt.Message{}, false
		}
		blob, err := json.Marshal(value)
		if err != nil {
			return sessionrt.Message{}, false
		}
		var out sessionrt.Message
		if err := json.Unmarshal(blob, &out); err != nil {
			return sessionrt.Message{}, false
		}
		out.Role = sessionrt.Role(strings.TrimSpace(string(out.Role)))
		return out, true
	default:
		return sessionrt.Message{}, false
	}
}

func stringPayloadField(payload any, key string) (string, bool) {
	value, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	text, ok := value[key].(string)
	if !ok {
		return "", false
	}
	return text, true
}

func clonePayloadMap(payload any) map[string]any {
	src, ok := payload.(map[string]any)
	if !ok || src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func (a *SessionRuntimeAdapter) getOrInitSession(sessionID sessionrt.SessionID, actorID sessionrt.ActorID, history []sessionrt.Event, promptIdx int) *Session {
	a.mu.Lock()
	defer a.mu.Unlock()

	if sessionData, exists := a.sessions[sessionID]; exists {
		return sessionData
	}

	sessionData := a.agent.NewSession()
	sessionData.ID = string(sessionID)
	bootstrapHistory := history
	if promptIdx >= 0 && promptIdx <= len(history) {
		bootstrapHistory = history[:promptIdx]
	}
	hydrateSessionMessagesFromHistory(sessionData, bootstrapHistory, actorID, a.agent.Config.MaxContextMessages, a.agent.model)
	a.sessions[sessionID] = sessionData
	return sessionData
}

func hydrateSessionMessagesFromHistory(sessionData *Session, history []sessionrt.Event, actorID sessionrt.ActorID, maxMessages int, model ai.Model) {
	if sessionData == nil || len(history) == 0 {
		return
	}
	targetActor := strings.TrimSpace(string(actorID))
	messages := make([]Message, 0, len(history))
	for _, event := range history {
		switch event.Type {
		case sessionrt.EventMessage:
			msg, ok := promptMessageFromPayload(event.Payload)
			if !ok {
				continue
			}
			mappedRole, include := mapSessionEventRole(msg, event.From, targetActor)
			if !include {
				continue
			}
			mapped := Message{
				Role:      mappedRole,
				Content:   msg.Content,
				Timestamp: eventTimestampMillis(event),
			}
			if len(msg.Attachments) > 0 {
				mapped.Content = buildInboundAttachmentContent(msg.Content, mapSessionAttachments(msg.Attachments), model)
			}
			messages = append(messages, mapped)
		case sessionrt.EventToolResult:
			if targetActor != "" {
				fromActor := strings.TrimSpace(string(event.From))
				if fromActor != "" && fromActor != targetActor {
					continue
				}
			}
			toolMsg, ok := toolResultMessageFromPayload(event)
			if !ok {
				continue
			}
			messages = append(messages, toolMsg)
		}
	}
	if len(messages) > 0 {
		sessionData.Messages = boundMessages(messages, maxMessages)
	}
}

func mapSessionEventRole(msg sessionrt.Message, from sessionrt.ActorID, targetActor string) (ai.MessageRole, bool) {
	switch msg.Role {
	case sessionrt.RoleUser:
		return ai.RoleUser, true
	case sessionrt.RoleSystem:
		return "", false
	case sessionrt.RoleAgent:
		target := strings.TrimSpace(string(msg.TargetActorID))
		if target != "" {
			if targetActor != "" && target != targetActor {
				return "", false
			}
			// A targeted agent message is an inbound prompt for this actor.
			return ai.RoleUser, true
		}
		if targetActor != "" {
			fromActor := strings.TrimSpace(string(from))
			if fromActor != "" && fromActor != targetActor {
				return "", false
			}
		}
		return ai.RoleAssistant, true
	default:
		return "", false
	}
}

func toolResultMessageFromPayload(event sessionrt.Event) (Message, bool) {
	payload, ok := event.Payload.(map[string]any)
	if !ok || payload == nil {
		return Message{}, false
	}
	if strings.EqualFold(strings.TrimSpace(asString(payload["backend"])), "provider_native") {
		return Message{}, false
	}
	toolName := strings.TrimSpace(asString(payload["name"]))
	if toolName == "" {
		return Message{}, false
	}
	toolCallID := strings.TrimSpace(asString(payload["tool_call_id"]))
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(asString(payload["id"]))
	}
	if toolCallID == "" {
		toolCallID = string(event.ID)
	}
	if toolCallID == "" {
		toolCallID = toolName
	}
	status := ToolStatus(strings.TrimSpace(strings.ToLower(asString(payload["status"]))))
	toolText, _ := formatToolResultTextForContext(ToolOutput{
		Status: status,
		Result: payload["result"],
	})
	if toolText == "" {
		toolText = "{}"
	}
	msg := ai.NewToolResultMessage(
		toolCallID,
		toolName,
		[]ai.ContentBlock{{Type: ai.ContentTypeText, Text: toolText}},
		status == ToolStatusError,
	)
	msg.Timestamp = eventTimestampMillis(event)
	return msg, true
}

func eventTimestampMillis(event sessionrt.Event) int64 {
	if !event.Timestamp.IsZero() {
		return event.Timestamp.UnixMilli()
	}
	if event.Seq > 0 {
		return int64(event.Seq)
	}
	return 0
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func mapSessionAttachments(in []sessionrt.Attachment) []Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(in))
	for _, attachment := range in {
		out = append(out, Attachment{
			Path:     strings.TrimSpace(attachment.Path),
			Name:     strings.TrimSpace(attachment.Name),
			MIMEType: strings.TrimSpace(attachment.MIMEType),
			Text:     attachment.Text,
			Data:     append([]byte(nil), attachment.Data...),
		})
	}
	return out
}

func (a *SessionRuntimeAdapter) mapEvent(event Event) (sessionrt.Event, bool) {
	switch event.Type {
	case EventTypeAgentDelta:
		if !a.opts.CaptureDeltas {
			return sessionrt.Event{}, false
		}
		delta, _ := stringPayloadField(event.Payload, "delta")
		return sessionrt.Event{
			Type:    sessionrt.EventAgentDelta,
			Payload: map[string]any{"delta": delta},
		}, true
	case EventTypeAgentThinkingDelta:
		if !a.opts.CaptureThinking {
			return sessionrt.Event{}, false
		}
		delta, _ := stringPayloadField(event.Payload, "delta")
		return sessionrt.Event{
			Type:    sessionrt.EventAgentThinkingDelta,
			Payload: map[string]any{"delta": delta},
		}, true
	case EventTypeAgentMsg:
		text, _ := stringPayloadField(event.Payload, "text")
		return sessionrt.Event{
			Type:    sessionrt.EventMessage,
			Payload: sessionrt.Message{Role: sessionrt.RoleAgent, Content: text},
		}, true
	case EventTypeToolCall:
		return sessionrt.Event{
			Type:    sessionrt.EventToolCall,
			Payload: clonePayloadMap(event.Payload),
		}, true
	case EventTypeToolResult:
		return sessionrt.Event{
			Type:    sessionrt.EventToolResult,
			Payload: clonePayloadMap(event.Payload),
		}, true
	case EventTypeError:
		message, _ := stringPayloadField(event.Payload, "message")
		return sessionrt.Event{
			Type:    sessionrt.EventError,
			Payload: sessionrt.ErrorPayload{Message: message},
		}, true
	default:
		return sessionrt.Event{}, false
	}
}

func (a *SessionRuntimeAdapter) buildStatePatch(sessionData *Session) map[string]any {
	if a == nil || a.agent == nil || sessionData == nil {
		return nil
	}
	diagnostics := sessionData.LastContextDiagnostics
	warnings := make([]string, len(diagnostics.Warnings))
	copy(warnings, diagnostics.Warnings)
	pruneActions := make([]string, len(diagnostics.PruneActions))
	copy(pruneActions, diagnostics.PruneActions)
	compactionActions := make([]string, len(diagnostics.CompactionActions))
	copy(compactionActions, diagnostics.CompactionActions)

	return map[string]any{
		"updated_at":                   time.Now().UTC().Format(time.RFC3339Nano),
		"agent_id":                     strings.TrimSpace(a.agent.ID),
		"model_id":                     strings.TrimSpace(a.agent.model.ID),
		"model_provider":               strings.TrimSpace(string(a.agent.model.Provider)),
		"model_context_window":         a.agent.model.ContextWindow,
		"session_message_count":        len(sessionData.Messages),
		"compaction_summary_count":     len(sessionData.CompactionSummaries),
		"reserve_tokens":               diagnostics.ReserveTokens,
		"reserve_floor_tokens":         diagnostics.ReserveFloorTokens,
		"estimated_input_tokens":       diagnostics.EstimatedInputTokens,
		"overflow_retries":             diagnostics.OverflowRetries,
		"overflow_stage":               diagnostics.OverflowStage,
		"summary_strategy":             diagnostics.SummaryStrategy,
		"recent_messages_used_tokens":  diagnostics.RecentMessagesLane.UsedTokens,
		"recent_messages_cap_tokens":   diagnostics.RecentMessagesLane.CapTokens,
		"retrieved_memory_used_tokens": diagnostics.RetrievedMemoryLane.UsedTokens,
		"retrieved_memory_cap_tokens":  diagnostics.RetrievedMemoryLane.CapTokens,
		"compaction_used_tokens":       diagnostics.CompactionSummaryLane.UsedTokens,
		"compaction_cap_tokens":        diagnostics.CompactionSummaryLane.CapTokens,
		"working_memory_used_tokens":   diagnostics.WorkingMemoryLane.UsedTokens,
		"working_memory_cap_tokens":    diagnostics.WorkingMemoryLane.CapTokens,
		"bootstrap_used_tokens":        diagnostics.BootstrapLane.UsedTokens,
		"bootstrap_cap_tokens":         diagnostics.BootstrapLane.CapTokens,
		"selected_memory_count":        len(diagnostics.SelectedMemoryIDs),
		"memory_search_mode":           diagnostics.MemorySearchMode,
		"memory_provider":              diagnostics.MemoryProvider,
		"memory_fallback_reason":       diagnostics.MemoryFallbackReason,
		"memory_unavailable_reason":    diagnostics.MemoryUnavailableReason,
		"tool_result_truncation_count": diagnostics.ToolResultTruncation,
		"warnings":                     warnings,
		"prune_actions":                pruneActions,
		"compaction_actions":           compactionActions,
		"pair_repair_actions":          append([]string(nil), diagnostics.PairRepairActions...),
	}
}
