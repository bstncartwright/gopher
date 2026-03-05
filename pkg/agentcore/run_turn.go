package agentcore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
)

const (
	runTurnContextBuildTimeout         = 45 * time.Second
	runTurnContextBuildFallbackTimeout = 10 * time.Second
)

func (a *Agent) RunTurn(ctx context.Context, s *Session, in TurnInput) (TurnResult, error) {
	return a.runTurn(ctx, s, in, nil)
}

func (a *Agent) RunTurnWithEventHandler(ctx context.Context, s *Session, in TurnInput, onEvent func(Event) error) (TurnResult, error) {
	return a.runTurn(ctx, s, in, onEvent)
}

func (a *Agent) runTurn(ctx context.Context, s *Session, in TurnInput, onEvent func(Event) error) (TurnResult, error) {
	startTime := time.Now()
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil {
		slog.Error("run_turn: agent is nil")
		return TurnResult{}, fmt.Errorf("agent is nil")
	}
	if s == nil {
		slog.Error("run_turn: session is nil", "agent_id", a.ID)
		return TurnResult{}, fmt.Errorf("session is nil")
	}
	if strings.TrimSpace(s.ID) == "" {
		s.ID = a.NewSession().ID
		slog.Debug("run_turn: created new session", "agent_id", a.ID, "session_id", s.ID)
	}
	if a.Provider == nil {
		slog.Error("run_turn: agent provider is nil", "agent_id", a.ID, "session_id", s.ID)
		return TurnResult{}, fmt.Errorf("agent provider is nil")
	}

	slog.Info("run_turn: starting turn",
		"agent_id", a.ID,
		"session_id", s.ID,
		"model", a.model.ID,
		"prompt_mode", in.PromptMode,
		"message_length", len(in.UserMessage),
	)

	var (
		turnErr          error
		finalText        string
		toolObservations []toolObservation
	)
	defer func() {
		duration := time.Since(startTime)
		slog.Info("run_turn: turn completed",
			"agent_id", a.ID,
			"session_id", s.ID,
			"duration_ms", duration.Milliseconds(),
			"has_error", turnErr != nil,
			"final_text_length", len(finalText),
			"tool_observations", len(toolObservations),
		)
		a.persistTurnMemories(ctx, s, in.UserMessage, finalText, toolObservations, turnErr)
	}()

	emitter := newEventEmitter(a.ID, s.ID, a.Logger, onEvent)
	slog.Debug("run_turn: building provider context", "agent_id", a.ID, "session_id", s.ID)
	conversation, diagnostics, err := a.buildTurnProviderContext(ctx, s, in)
	if err != nil {
		turnErr = err
		slog.Error("run_turn: failed to build provider context",
			"agent_id", a.ID,
			"session_id", s.ID,
			"error", err,
		)
		return TurnResult{Events: emitter.Events()}, err
	}
	s.LastContextDiagnostics = diagnostics
	slog.Debug("run_turn: provider context built",
		"agent_id", a.ID,
		"session_id", s.ID,
		"message_count", len(conversation.Messages),
		"tools_count", len(conversation.Tools),
		"system_prompt_length", len(conversation.SystemPrompt),
	)

	runner := NewToolRunner(a)
	overflowRetryUsed := 0
	overflowFlushAttempted := false
	hostedWebSearchFallbackUsed := false
	reasoningLevel := a.Config.ReasoningLevelValue()
	for round := 0; ; round++ {
		roundStart := time.Now()
		slog.Debug("run_turn: starting round",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
		)
		if ctx.Err() != nil {
			turnErr = ctx.Err()
			slog.Warn("run_turn: context cancelled",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"error", ctx.Err(),
			)
			return TurnResult{Events: emitter.Events()}, turnErr
		}
		slog.Debug("run_turn: calling provider stream",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
			"model", a.model.ID,
			"provider", a.model.Provider,
		)
		hostedSearchMode := hostedWebSearchMode(conversation.Tools)
		stream := a.Provider.Stream(a.model, conversation, &ai.SimpleStreamOptions{
			StreamOptions: ai.StreamOptions{
				RequestContext: ctx,
				APIKey:         ai.GetEnvAPIKey(string(a.model.Provider)),
				SessionID:      s.ID,
			},
			Reasoning: reasoningLevel,
		})
		if stream == nil {
			err := fmt.Errorf("provider returned nil stream")
			slog.Error("run_turn: provider returned nil stream",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"model", a.model.ID,
			)
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
				turnErr = emitErr
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			turnErr = err
			return TurnResult{Events: emitter.Events()}, err
		}

		toolCalls := make([]ai.ContentBlock, 0, 2)
		streamErrors := make([]string, 0, 1)
		textDeltaCount := 0
		for event := range stream.Events() {
			switch event.Type {
			case ai.EventTextDelta:
				if event.Delta == "" {
					continue
				}
				textDeltaCount++
				if err := emitter.Emit(EventTypeAgentDelta, map[string]any{"delta": event.Delta}); err != nil {
					turnErr = err
					slog.Error("run_turn: failed to emit text delta",
						"agent_id", a.ID,
						"session_id", s.ID,
						"round", round,
						"error", err,
					)
					return TurnResult{Events: emitter.Events()}, err
				}
			case ai.EventThinkingDelta:
				if !a.CaptureThinkingDeltas || event.Delta == "" {
					continue
				}
				if err := emitter.Emit(EventTypeAgentThinkingDelta, map[string]any{"delta": event.Delta}); err != nil {
					turnErr = err
					return TurnResult{Events: emitter.Events()}, err
				}
			case ai.EventToolCallEnd:
				if event.ToolCall != nil {
					toolCalls = append(toolCalls, event.ToolCall.Clone())
					slog.Debug("run_turn: received tool call",
						"agent_id", a.ID,
						"session_id", s.ID,
						"round", round,
						"tool_name", event.ToolCall.Name,
						"tool_id", event.ToolCall.ID,
					)
				}
			case ai.EventWebSearchStart:
				if event.WebSearch == nil {
					continue
				}
				args := map[string]any{}
				if strings.TrimSpace(event.WebSearch.Query) != "" {
					args["query"] = event.WebSearch.Query
				}
				if len(event.WebSearch.Action) > 0 {
					args["action"] = ai.CloneMap(event.WebSearch.Action)
				}
				payload := map[string]any{
					"name":    "web_search",
					"args":    args,
					"backend": "provider_native",
					"mode":    string(hostedSearchMode),
				}
				if err := emitter.Emit(EventTypeToolCall, payload); err != nil {
					turnErr = err
					return TurnResult{Events: emitter.Events()}, err
				}
			case ai.EventWebSearchEnd:
				if event.WebSearch == nil {
					continue
				}
				result := map[string]any{
					"query":  event.WebSearch.Query,
					"status": event.WebSearch.Status,
				}
				if len(event.WebSearch.Action) > 0 {
					result["action"] = ai.CloneMap(event.WebSearch.Action)
				}
				status := ToolStatusOK
				if strings.EqualFold(strings.TrimSpace(event.WebSearch.Status), "failed") {
					status = ToolStatusError
				}
				if err := emitter.Emit(EventTypeToolResult, map[string]any{
					"name":    "web_search",
					"status":  status,
					"result":  result,
					"backend": "provider_native",
					"mode":    string(hostedSearchMode),
				}); err != nil {
					turnErr = err
					return TurnResult{Events: emitter.Events()}, err
				}
				toolObservations = append(toolObservations, toolObservation{
					Name:   "web_search",
					Args:   map[string]any{"query": event.WebSearch.Query},
					Status: status,
					Result: result,
				})
			case ai.EventError:
				if event.Error == nil {
					continue
				}
				slog.Warn("run_turn: received stream error event",
					"agent_id", a.ID,
					"session_id", s.ID,
					"round", round,
					"error_message", event.Error.ErrorMessage,
				)
				if strings.TrimSpace(event.Error.ErrorMessage) != "" {
					streamErrors = append(streamErrors, strings.TrimSpace(event.Error.ErrorMessage))
				}
			}
		}
		slog.Debug("run_turn: stream events processed",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
			"text_deltas", textDeltaCount,
			"tool_calls", len(toolCalls),
		)

		assistant, err := stream.Result(ctx)
		if err != nil {
			slog.Error("run_turn: failed to get stream result",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"error", err,
			)
			if a.shouldRetryContextOverflow(err.Error(), overflowRetryUsed) {
				recovered, recoverErr := a.recoverFromContextOverflow(ctx, s, in, err.Error(), overflowRetryUsed+1, &overflowFlushAttempted)
				if recoverErr == nil {
					conversation = recovered
					overflowRetryUsed++
					slog.Warn("run_turn: recovered from context overflow, retrying round",
						"agent_id", a.ID,
						"session_id", s.ID,
						"round", round,
						"overflow_retry", overflowRetryUsed,
					)
					continue
				}
				err = recoverErr
			}
			if a.isOverflowMessage(err.Error()) {
				err = a.contextOverflowError(err, s.LastContextDiagnostics)
			}
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
				turnErr = emitErr
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			turnErr = err
			return TurnResult{Events: emitter.Events()}, err
		}

		if ai.IsContextOverflow(assistant, a.model.ContextWindow) && a.shouldRetryContextOverflow(assistant.ErrorMessage, overflowRetryUsed) {
			recovered, recoverErr := a.recoverFromContextOverflow(ctx, s, in, assistant.ErrorMessage, overflowRetryUsed+1, &overflowFlushAttempted)
			if recoverErr == nil {
				conversation = recovered
				overflowRetryUsed++
				slog.Warn("run_turn: overflow detected in assistant result, retrying",
					"agent_id", a.ID,
					"session_id", s.ID,
					"round", round,
					"overflow_retry", overflowRetryUsed,
				)
				continue
			}
			err = recoverErr
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
				turnErr = emitErr
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			turnErr = err
			return TurnResult{Events: emitter.Events()}, err
		}
		if ai.IsContextOverflow(assistant, a.model.ContextWindow) {
			err = a.contextOverflowError(errors.New(strings.TrimSpace(assistant.ErrorMessage)), s.LastContextDiagnostics)
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
				turnErr = emitErr
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			turnErr = err
			return TurnResult{Events: emitter.Events()}, err
		}
		slog.Debug("run_turn: stream result received",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
			"stop_reason", assistant.StopReason,
			"content_blocks", len(assistant.Content),
			"usage_input", assistant.Usage.Input,
			"usage_output", assistant.Usage.Output,
			"usage_total", assistant.Usage.TotalTokens,
			"usage_cost", assistant.Usage.Cost,
		)
		slog.Debug("run_turn: stream round summary",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
			"stream_errors", len(streamErrors),
			"assistant_error_message_length", len(strings.TrimSpace(assistant.ErrorMessage)),
			"assistant_stop_reason", assistant.StopReason,
		)

		if len(toolCalls) == 0 {
			for _, block := range assistant.Content {
				if block.Type == ai.ContentTypeToolCall {
					toolCalls = append(toolCalls, block.Clone())
				}
			}
		}

		if len(streamErrors) > 0 &&
			strings.TrimSpace(assistant.ErrorMessage) == "" &&
			assistant.StopReason != ai.StopReasonError &&
			assistant.StopReason != ai.StopReasonAborted {
			streamErr := streamErrors[0]
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": streamErr}); emitErr != nil {
				turnErr = emitErr
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			finalTextCandidate := strings.TrimSpace(extractText(assistant.Content))
			if finalTextCandidate == "" && len(toolCalls) == 0 {
				err := fmt.Errorf("provider stream failed before completion: %s", streamErr)
				slog.Warn("run_turn: stream error with empty assistant output",
					"agent_id", a.ID,
					"session_id", s.ID,
					"round", round,
					"stream_error", streamErr,
				)
				if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
					turnErr = emitErr
					return TurnResult{Events: emitter.Events()}, emitErr
				}
				turnErr = err
				return TurnResult{Events: emitter.Events()}, err
			}
		}
		if assistant.StopReason == ai.StopReasonError || assistant.StopReason == ai.StopReasonAborted {
			providerErr := strings.TrimSpace(assistant.ErrorMessage)
			if providerErr == "" && len(streamErrors) > 0 {
				providerErr = strings.TrimSpace(streamErrors[0])
			}
			if providerErr == "" {
				switch assistant.StopReason {
				case ai.StopReasonAborted:
					providerErr = "provider request aborted"
				default:
					providerErr = "provider returned error stop reason"
				}
			}
			if shouldRetryHostedWebSearchFallback(providerErr, conversation.Tools, textDeltaCount, toolCalls, hostedWebSearchFallbackUsed) {
				conversation.Tools = buildProviderAITools(activeToolRegistry(a.Tools, ToolInput{Agent: a, Session: s}), a.model, a.Config, a.Policies, true)
				hostedWebSearchFallbackUsed = true
				slog.Warn("run_turn: retrying with MCP web_search fallback",
					"agent_id", a.ID,
					"session_id", s.ID,
					"round", round,
					"provider_error", providerErr,
				)
				continue
			}
			err := fmt.Errorf("%s", providerErr)
			slog.Warn("run_turn: provider returned terminal error stop reason",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"stop_reason", assistant.StopReason,
				"error_message", providerErr,
			)
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
				turnErr = emitErr
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			turnErr = err
			return TurnResult{Events: emitter.Events()}, err
		}

		conversation.Messages = append(conversation.Messages, assistant.ToMessage())

		if len(toolCalls) == 0 {
			finalText = extractText(assistant.Content)
			slog.Info("run_turn: turn complete without tool calls",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"final_text_length", len(finalText),
				"round_duration_ms", time.Since(roundStart).Milliseconds(),
			)
			if err := emitter.Emit(EventTypeAgentMsg, map[string]any{"text": finalText}); err != nil {
				turnErr = err
				return TurnResult{Events: emitter.Events()}, err
			}
			s.Messages = boundMessages(conversation.Messages, a.Config.MaxContextMessages)
			turnErr = nil
			finalText = strings.TrimSpace(finalText)
			return TurnResult{FinalText: finalText, Events: emitter.Events()}, nil
		}

		slog.Info("run_turn: executing tool calls",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
			"tool_calls_count", len(toolCalls),
		)
		for _, toolCall := range toolCalls {
			slog.Debug("run_turn: executing tool",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"tool_name", toolCall.Name,
				"tool_id", toolCall.ID,
				"args_keys", getMapKeys(toolCall.Arguments),
			)
			if err := emitter.Emit(EventTypeToolCall, map[string]any{"name": toolCall.Name, "args": toolCall.Arguments}); err != nil {
				turnErr = err
				return TurnResult{Events: emitter.Events()}, err
			}
		}

		type toolExecution struct {
			call     ai.ContentBlock
			output   ToolOutput
			runErr   error
			duration time.Duration
		}
		executions := make([]toolExecution, len(toolCalls))
		var wg sync.WaitGroup
		for idx, toolCall := range toolCalls {
			wg.Add(1)
			go func(i int, call ai.ContentBlock) {
				defer wg.Done()
				toolStart := time.Now()
				output, runErr := runner.Run(ctx, s, call)
				executions[i] = toolExecution{
					call:     call,
					output:   output,
					runErr:   runErr,
					duration: time.Since(toolStart),
				}
			}(idx, toolCall)
		}
		wg.Wait()

		for _, execResult := range executions {
			toolCall := execResult.call
			output := execResult.output
			runErr := execResult.runErr
			toolDuration := execResult.duration
			slog.Info("run_turn: tool execution complete",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"tool_name", toolCall.Name,
				"tool_id", toolCall.ID,
				"status", output.Status,
				"duration_ms", toolDuration.Milliseconds(),
				"has_error", runErr != nil,
			)
			if runErr != nil {
				slog.Warn("run_turn: tool execution error",
					"agent_id", a.ID,
					"session_id", s.ID,
					"round", round,
					"tool_name", toolCall.Name,
					"error", runErr,
				)
				if err := emitter.Emit(EventTypeError, map[string]any{"message": runErr.Error()}); err != nil {
					turnErr = err
					return TurnResult{Events: emitter.Events()}, err
				}
			}
			if err := emitter.Emit(EventTypeToolResult, map[string]any{
				"name":   toolCall.Name,
				"result": output.Result,
				"status": output.Status,
			}); err != nil {
				turnErr = err
				return TurnResult{Events: emitter.Events()}, err
			}
			toolObservations = append(toolObservations, toolObservation{
				Name:   toolCall.Name,
				Args:   cloneAnyMap(toolCall.Arguments),
				Status: output.Status,
				Result: output.Result,
			})

			toolCallID := toolCall.ID
			if toolCallID == "" {
				toolCallID = toolCall.Name
			}
			toolText, wasTruncated := formatToolResultTextForContext(output)
			if toolText == "" && runErr != nil {
				toolText = runErr.Error()
			}
			if toolText == "" {
				toolText = "{}"
			}
			if wasTruncated {
				s.LastContextDiagnostics.ToolResultTruncation++
			}

			slog.Debug("run_turn: appending tool result to conversation",
				"agent_id", a.ID,
				"session_id", s.ID,
				"round", round,
				"tool_name", toolCall.Name,
				"tool_call_id", toolCallID,
				"result_length", len(toolText),
				"is_error", runErr != nil,
			)
			conversation.Messages = append(conversation.Messages, ai.NewToolResultMessage(
				toolCallID,
				toolCall.Name,
				[]ai.ContentBlock{{Type: ai.ContentTypeText, Text: toolText}},
				runErr != nil,
			))
		}
		slog.Debug("run_turn: round complete, persisting messages",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
			"round_duration_ms", time.Since(roundStart).Milliseconds(),
			"total_messages", len(conversation.Messages),
		)
		// Persist conversation progress after each tool round so retries don't replay executed calls.
		s.Messages = boundMessages(conversation.Messages, a.Config.MaxContextMessages)
	}
}

func hostedWebSearchMode(tools []ai.Tool) NativeWebSearchMode {
	for _, tool := range tools {
		if !tool.IsHostedWebSearch() {
			continue
		}
		if tool.ExternalWebAccess != nil && *tool.ExternalWebAccess {
			return NativeWebSearchModeLive
		}
		return NativeWebSearchModeCached
	}
	return NativeWebSearchModeDisabled
}

func shouldRetryHostedWebSearchFallback(message string, tools []ai.Tool, textDeltaCount int, toolCalls []ai.ContentBlock, retryUsed bool) bool {
	if retryUsed || textDeltaCount != 0 || len(toolCalls) != 0 || hostedWebSearchMode(tools) == NativeWebSearchModeDisabled {
		return false
	}
	lowered := strings.ToLower(strings.TrimSpace(message))
	if lowered == "" || !strings.Contains(lowered, "web_search") {
		return false
	}
	return strings.Contains(lowered, "unsupported") ||
		strings.Contains(lowered, "not supported") ||
		strings.Contains(lowered, "invalid") ||
		strings.Contains(lowered, "unknown") ||
		strings.Contains(lowered, "unrecognized")
}

func (a *Agent) buildTurnProviderContext(ctx context.Context, s *Session, in TurnInput) (ai.Context, ctxbundle.ContextDiagnostics, error) {
	primaryCtx, cancelPrimary := boundedContext(ctx, runTurnContextBuildTimeout)
	defer cancelPrimary()

	conversation, diagnostics, err := a.buildProviderContextDetailedWithAttachments(primaryCtx, s, in.UserMessage, in.Attachments, providerContextBuildOptions{
		Mode:                         normalizePromptMode(in.PromptMode),
		EnableModelCompactionSummary: true,
	})
	if err == nil {
		return conversation, diagnostics, nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return ai.Context{}, ctxbundle.ContextDiagnostics{}, err
	}
	if ctx != nil && ctx.Err() != nil {
		return ai.Context{}, ctxbundle.ContextDiagnostics{}, err
	}

	slog.Warn("run_turn: provider context timed out, retrying with degraded context",
		"agent_id", a.ID,
		"session_id", s.ID,
		"timeout_ms", runTurnContextBuildTimeout.Milliseconds(),
	)

	fallbackCtx, cancelFallback := boundedContext(ctx, runTurnContextBuildFallbackTimeout)
	defer cancelFallback()
	fallbackConversation, fallbackDiagnostics, fallbackErr := a.buildProviderContextDetailedWithAttachments(
		fallbackCtx,
		s,
		in.UserMessage,
		in.Attachments,
		providerContextBuildOptions{
			Mode:                         normalizePromptMode(in.PromptMode),
			MaxMemories:                  2,
			DisableRetrievedMemory:       true,
			EnableModelCompactionSummary: false,
			Warnings: []string{
				"context build fallback after timeout",
			},
		},
	)
	if fallbackErr != nil {
		return ai.Context{}, ctxbundle.ContextDiagnostics{}, err
	}
	return fallbackConversation, fallbackDiagnostics, nil
}

func boundedContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining <= timeout {
			return ctx, func() {}
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func (a *Agent) shouldRetryContextOverflow(message string, retriesUsed int) bool {
	if a == nil {
		return false
	}
	if !a.Config.ContextManagement.OverflowRetryEnabled() {
		return false
	}
	if retriesUsed >= a.Config.ContextManagement.OverflowRetryLimitValue() {
		return false
	}
	candidate := ai.AssistantMessage{
		StopReason:   ai.StopReasonError,
		ErrorMessage: strings.TrimSpace(message),
	}
	return ai.IsContextOverflow(candidate, a.model.ContextWindow)
}

func (a *Agent) isOverflowMessage(message string) bool {
	candidate := ai.AssistantMessage{
		StopReason:   ai.StopReasonError,
		ErrorMessage: strings.TrimSpace(message),
	}
	return ai.IsContextOverflow(candidate, a.model.ContextWindow)
}

func (a *Agent) recoverFromContextOverflow(ctx context.Context, s *Session, in TurnInput, reason string, retries int, flushAttempted *bool) (ai.Context, error) {
	if a == nil || s == nil {
		return ai.Context{}, fmt.Errorf("cannot recover context overflow without agent/session")
	}
	overflowStage := fmt.Sprintf("retry_%d", retries)
	if flushAttempted != nil && !*flushAttempted && a.shouldTriggerPreCompactionFlush(s) {
		*flushAttempted = true
		if err := a.flushMemoryBeforeCompaction(ctx, s, reason); err != nil {
			s.LastContextDiagnostics.Warnings = append(s.LastContextDiagnostics.Warnings, "memory flush before compaction failed: "+err.Error())
		}
	}

	if a.Config.ContextManagement.PruningEnabled() {
		pruned := ctxbundle.PruneMessagesDetailed(s.Messages, ctxbundle.PruneOptions{})
		s.Messages = pruned.Messages
		s.LastContextDiagnostics.PruneActions = append(s.LastContextDiagnostics.PruneActions, pruned.Actions...)
		s.LastContextDiagnostics.ToolResultTruncation += pruned.ToolResultTruncations
	}
	if a.Config.ContextManagement.ModeValue() == "safeguard" {
		repaired, repairActions := ctxbundle.RepairMessages(s.Messages, ctxbundle.RepairOptions{})
		s.Messages = repaired
		s.LastContextDiagnostics.PairRepairActions = append(s.LastContextDiagnostics.PairRepairActions, repairActions...)
	}

	if a.Config.ContextManagement.CompactionEnabled() {
		totalTokens := ctxbundle.EstimateMessagesTokens(s.Messages)
		keepPercent := 50
		if retries >= 2 {
			keepPercent = 30
		}
		if retries >= 3 {
			keepPercent = 18
		}
		target := (totalTokens * keepPercent) / 100
		if target < 1 {
			target = 1
		}
		kept, dropped, _ := ctxbundle.SelectMessagesForBudget(s.Messages, target)
		if a.Config.ContextManagement.ModeValue() == "safeguard" {
			repairedKept, repairActions := ctxbundle.RepairMessages(kept, ctxbundle.RepairOptions{})
			kept = repairedKept
			s.LastContextDiagnostics.PairRepairActions = append(s.LastContextDiagnostics.PairRepairActions, repairActions...)
		}
		if len(dropped) > 0 {
			s.Messages = kept
			compaction, strategy, summaryErr := a.summarizeDroppedMessages(ctx, s, dropped)
			if summaryErr != nil {
				s.LastContextDiagnostics.Warnings = append(s.LastContextDiagnostics.Warnings, "overflow compaction summary fallback: "+summaryErr.Error())
			}
			if strings.TrimSpace(compaction.Summary) != "" {
				s.CompactionSummaries = prependCompactionSummary(s.CompactionSummaries, compaction.Summary, 3)
				if retries >= 2 && len(s.CompactionSummaries) > 2 {
					s.CompactionSummaries = s.CompactionSummaries[:2]
				}
				if retries >= 3 && len(s.CompactionSummaries) > 1 {
					s.CompactionSummaries = s.CompactionSummaries[:1]
				}
				s.LastContextDiagnostics.CompactionActions = append(s.LastContextDiagnostics.CompactionActions, compaction.Actions...)
				if strings.TrimSpace(strategy) != "" {
					s.LastContextDiagnostics.SummaryStrategy = strategy
				}
			}
		}
	}

	carriedDiagnostics := s.LastContextDiagnostics
	warnings := []string{
		"context overflow detected: " + strings.TrimSpace(reason),
		"overflow recovery stage: " + overflowStage,
	}
	mode := normalizePromptMode(in.PromptMode)
	maxMemories := 8
	disableRetrievedMemory := false
	if retries >= 2 {
		maxMemories = 4
	}
	if retries >= 3 {
		mode = PromptModeMinimal
		maxMemories = 1
		disableRetrievedMemory = true
	}
	conversation, diagnostics, err := a.buildProviderContextDetailedWithOptions(ctx, s, in.UserMessage, providerContextBuildOptions{
		Mode:                         mode,
		CompactionSummaries:          s.CompactionSummaries,
		OverflowRetries:              retries,
		OverflowStage:                overflowStage,
		MaxMemories:                  maxMemories,
		DisableRetrievedMemory:       disableRetrievedMemory,
		EnableModelCompactionSummary: true,
		Warnings:                     warnings,
	})
	if err != nil {
		return ai.Context{}, fmt.Errorf("overflow recovery failed: %w", err)
	}
	s.LastContextDiagnostics = mergeOverflowDiagnostics(diagnostics, carriedDiagnostics)
	return conversation, nil
}

func (a *Agent) contextOverflowError(cause error, diagnostics ctxbundle.ContextDiagnostics) error {
	msg := strings.TrimSpace("context overflow after retry")
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		msg = msg + ": " + strings.TrimSpace(cause.Error())
	}
	msg = msg + fmt.Sprintf(
		" | estimated=%d reserve=%d model_window=%d recent=%d/%d memory=%d/%d compact=%d/%d",
		diagnostics.EstimatedInputTokens,
		diagnostics.ReserveTokens,
		diagnostics.ModelContextWindow,
		diagnostics.RecentMessagesLane.UsedTokens,
		diagnostics.RecentMessagesLane.CapTokens,
		diagnostics.RetrievedMemoryLane.UsedTokens,
		diagnostics.RetrievedMemoryLane.CapTokens,
		diagnostics.CompactionSummaryLane.UsedTokens,
		diagnostics.CompactionSummaryLane.CapTokens,
	)
	return fmt.Errorf("%s", msg)
}

func (a *Agent) shouldTriggerPreCompactionFlush(s *Session) bool {
	if a == nil || s == nil {
		return false
	}
	flushCfg := a.Config.ContextManagement.MemoryFlush
	if !flushCfg.EnabledValue() {
		return false
	}
	if !a.hasMemoryWriteTools() {
		return false
	}
	estimated := ctxbundle.EstimateMessagesTokens(s.Messages)
	trigger := a.model.ContextWindow - a.Config.ContextManagement.ReserveMinTokensValue() - flushCfg.SoftThresholdTokensValue()
	if trigger < 1 {
		trigger = 1
	}
	if estimated >= trigger {
		return true
	}
	transcript := renderFlushTranscript(s.Messages, flushCfg.ForceFlushTranscriptBytesValue())
	if len(transcript) >= int(flushCfg.ForceFlushTranscriptBytesValue()) {
		return true
	}
	// Overflow already occurred; flush once to reduce durable-memory loss even when token estimation undershoots.
	return len(s.Messages) > 0
}

func (a *Agent) hasMemoryWriteTools() bool {
	if a == nil {
		return false
	}
	if a.MemoryFiles == nil {
		return false
	}
	if hasTool(a.Tools, "write") || hasTool(a.Tools, "edit") || hasTool(a.Tools, "apply_patch") {
		return true
	}
	return false
}

func (a *Agent) flushMemoryBeforeCompaction(ctx context.Context, s *Session, reason string) error {
	if a == nil || s == nil {
		return nil
	}
	if a.SessionMemoryFlusher != nil {
		if err := a.SessionMemoryFlusher.FlushSession(ctx, s.ID); err != nil {
			return err
		}
		// When a dedicated flusher is available, avoid consuming an extra provider turn.
		return nil
	}
	if a.MemoryFiles == nil {
		return nil
	}
	flushCfg := a.Config.ContextManagement.MemoryFlush
	transcript := renderFlushTranscript(s.Messages, flushCfg.ForceFlushTranscriptBytesValue())
	if strings.TrimSpace(transcript) == "" {
		return nil
	}

	summary := summarizeFlushTranscript(transcript, reason)
	if summary == "" {
		return nil
	}
	if _, err := a.MemoryFiles.AppendDailyEntry(summary); err != nil {
		return err
	}
	if a.MemorySearch != nil {
		_ = a.MemorySearch.Sync(ctx, false)
	}
	return nil
}

func renderFlushTranscript(messages []ai.Message, maxBytes int64) string {
	if len(messages) == 0 {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = 2 << 20
	}
	lines := make([]string, 0, len(messages))
	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(string(msg.Role)))
		if role == "" {
			role = "unknown"
		}
		content := flushMessageText(msg)
		if strings.TrimSpace(content) == "" {
			continue
		}
		lines = append(lines, role+": "+strings.TrimSpace(content))
	}
	if len(lines) == 0 {
		return ""
	}
	joined := strings.Join(lines, "\n")
	if int64(len(joined)) <= maxBytes {
		return joined
	}
	return joined[len(joined)-int(maxBytes):]
}

func flushMessageText(msg ai.Message) string {
	if text, ok := msg.ContentText(); ok {
		return text
	}
	if blocks, ok := msg.ContentBlocks(); ok {
		parts := make([]string, 0, len(blocks))
		for _, block := range blocks {
			if block.Type != ai.ContentTypeText {
				continue
			}
			trimmed := strings.TrimSpace(block.Text)
			if trimmed == "" {
				continue
			}
			parts = append(parts, trimmed)
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func summarizeFlushTranscript(transcript string, reason string) string {
	lines := strings.Split(strings.TrimSpace(transcript), "\n")
	if len(lines) == 0 {
		return ""
	}
	maxLines := 8
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	summary := "Pre-compaction flush"
	if strings.TrimSpace(reason) != "" {
		summary += " (" + strings.TrimSpace(reason) + ")"
	}
	return summary + "\n" + strings.Join(lines, "\n")
}

func getMapKeys(m map[string]any) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func mergeOverflowDiagnostics(current ctxbundle.ContextDiagnostics, carried ctxbundle.ContextDiagnostics) ctxbundle.ContextDiagnostics {
	current.OverflowRetries = maxInt(current.OverflowRetries, carried.OverflowRetries)
	current.ToolResultTruncation += carried.ToolResultTruncation
	if strings.TrimSpace(current.SummaryStrategy) == "" {
		current.SummaryStrategy = strings.TrimSpace(carried.SummaryStrategy)
	}
	if strings.TrimSpace(current.OverflowStage) == "" {
		current.OverflowStage = strings.TrimSpace(carried.OverflowStage)
	}
	current.PruneActions = mergeUniqueStrings(carried.PruneActions, current.PruneActions)
	current.CompactionActions = mergeUniqueStrings(carried.CompactionActions, current.CompactionActions)
	current.PairRepairActions = mergeUniqueStrings(carried.PairRepairActions, current.PairRepairActions)
	current.Warnings = mergeUniqueStrings(carried.Warnings, current.Warnings)
	return current
}

func mergeUniqueStrings(prefix []string, suffix []string) []string {
	if len(prefix) == 0 && len(suffix) == 0 {
		return nil
	}
	out := make([]string, 0, len(prefix)+len(suffix))
	seen := make(map[string]struct{}, len(prefix)+len(suffix))
	appendIfNew := func(values []string) {
		for _, value := range values {
			trimmed := strings.TrimSpace(value)
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			out = append(out, trimmed)
		}
	}
	appendIfNew(prefix)
	appendIfNew(suffix)
	if len(out) == 0 {
		return nil
	}
	return out
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
