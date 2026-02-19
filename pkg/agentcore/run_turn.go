package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func (a *Agent) RunTurn(ctx context.Context, s *Session, in TurnInput) (TurnResult, error) {
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

	emitter := newEventEmitter(a.ID, s.ID, a.Logger)
	slog.Debug("run_turn: building provider context", "agent_id", a.ID, "session_id", s.ID)
	conversation, err := a.buildProviderContext(ctx, s, in.UserMessage, in.PromptMode)
	if err != nil {
		turnErr = err
		slog.Error("run_turn: failed to build provider context",
			"agent_id", a.ID,
			"session_id", s.ID,
			"error", err,
		)
		return TurnResult{Events: emitter.Events()}, err
	}
	slog.Debug("run_turn: provider context built",
		"agent_id", a.ID,
		"session_id", s.ID,
		"message_count", len(conversation.Messages),
		"tools_count", len(conversation.Tools),
		"system_prompt_length", len(conversation.SystemPrompt),
	)

	runner := NewToolRunner(a)
	for round := 0; round < MaxToolRounds; round++ {
		roundStart := time.Now()
		slog.Debug("run_turn: starting round",
			"agent_id", a.ID,
			"session_id", s.ID,
			"round", round,
			"max_rounds", MaxToolRounds,
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
		stream := a.Provider.Stream(a.model, conversation, &ai.SimpleStreamOptions{
			StreamOptions: ai.StreamOptions{
				RequestContext: ctx,
				APIKey:         ai.GetEnvAPIKey(string(a.model.Provider)),
				SessionID:      s.ID,
			},
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
			case ai.EventError:
				if event.Error == nil {
					continue
				}
				slog.Error("run_turn: received error from stream",
					"agent_id", a.ID,
					"session_id", s.ID,
					"round", round,
					"error_message", event.Error.ErrorMessage,
				)
				if err := emitter.Emit(EventTypeError, map[string]any{"message": event.Error.ErrorMessage}); err != nil {
					turnErr = err
					return TurnResult{Events: emitter.Events()}, err
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

		if len(toolCalls) == 0 {
			for _, block := range assistant.Content {
				if block.Type == ai.ContentTypeToolCall {
					toolCalls = append(toolCalls, block.Clone())
				}
			}
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

			toolStart := time.Now()
			output, runErr := runner.Run(ctx, s, toolCall)
			toolDuration := time.Since(toolStart)
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
			toolText := formatToolResultText(output)
			if toolText == "" && runErr != nil {
				toolText = runErr.Error()
			}
			if toolText == "" {
				toolText = "{}"
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

	s.Messages = boundMessages(conversation.Messages, a.Config.MaxContextMessages)
	err = fmt.Errorf("max tool rounds exceeded (%d)", MaxToolRounds)
	slog.Error("run_turn: max tool rounds exceeded",
		"agent_id", a.ID,
		"session_id", s.ID,
		"max_rounds", MaxToolRounds,
		"total_duration_ms", time.Since(startTime).Milliseconds(),
	)
	if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
		turnErr = emitErr
		return TurnResult{Events: emitter.Events()}, emitErr
	}
	turnErr = err
	return TurnResult{Events: emitter.Events()}, err
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
