package agentcore

import (
	"context"
	"strings"
	"sync"
	"time"

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
	userMsg, ok := latestPromptMessage(input.History, input.ActorID)
	if !ok {
		return sessionrt.AgentOutput{}, nil
	}

	a.mu.Lock()
	sessionData, exists := a.sessions[input.SessionID]
	if !exists {
		sessionData = a.agent.NewSession()
		sessionData.ID = string(input.SessionID)
		a.sessions[input.SessionID] = sessionData
	}
	a.mu.Unlock()

	turnCtx, cancel := withTurnTimeout(ctx)
	defer cancel()

	result, err := a.agent.RunTurn(turnCtx, sessionData, TurnInput{
		UserMessage: userMsg.Content,
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
	userMsg, ok := latestPromptMessage(input.History, input.ActorID)
	if !ok {
		return nil
	}

	a.mu.Lock()
	sessionData, exists := a.sessions[input.SessionID]
	if !exists {
		sessionData = a.agent.NewSession()
		sessionData.ID = string(input.SessionID)
		a.sessions[input.SessionID] = sessionData
	}
	a.mu.Unlock()

	turnCtx, cancel := withTurnTimeout(ctx)
	defer cancel()

	emitFn := emit
	if emitFn == nil {
		emitFn = func(sessionrt.Event) error { return nil }
	}

	_, err := a.agent.RunTurnWithEventHandler(turnCtx, sessionData, TurnInput{
		UserMessage: userMsg.Content,
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
			return payload, true
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
		return payload, true
	}
	return sessionrt.Message{}, false
}

func promptMessageFromPayload(payload any) (sessionrt.Message, bool) {
	switch value := payload.(type) {
	case sessionrt.Message:
		return value, true
	case map[string]any:
		roleRaw, ok := value["role"].(string)
		if !ok {
			return sessionrt.Message{}, false
		}
		content, ok := value["content"].(string)
		if !ok {
			return sessionrt.Message{}, false
		}
		out := sessionrt.Message{
			Role:    sessionrt.Role(strings.TrimSpace(roleRaw)),
			Content: content,
		}
		targetRaw, targetOK := value["target_actor_id"].(string)
		if targetOK {
			out.TargetActorID = sessionrt.ActorID(strings.TrimSpace(targetRaw))
		}
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
