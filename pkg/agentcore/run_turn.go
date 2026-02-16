package agentcore

import (
	"context"
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func (a *Agent) RunTurn(ctx context.Context, s *Session, in TurnInput) (TurnResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil {
		return TurnResult{}, fmt.Errorf("agent is nil")
	}
	if s == nil {
		return TurnResult{}, fmt.Errorf("session is nil")
	}
	if strings.TrimSpace(s.ID) == "" {
		s.ID = a.NewSession().ID
	}
	if a.Provider == nil {
		return TurnResult{}, fmt.Errorf("agent provider is nil")
	}

	emitter := newEventEmitter(a.ID, s.ID, a.Logger)
	conversation, err := a.buildProviderContext(s, in.UserMessage)
	if err != nil {
		return TurnResult{Events: emitter.Events()}, err
	}

	runner := NewToolRunner(a)
	for round := 0; round < MaxToolRounds; round++ {
		stream := a.Provider.Stream(a.model, conversation, &ai.SimpleStreamOptions{
			StreamOptions: ai.StreamOptions{
				RequestContext: ctx,
				APIKey:         ai.GetEnvAPIKey(string(a.model.Provider)),
				SessionID:      s.ID,
			},
		})
		if stream == nil {
			err := fmt.Errorf("provider returned nil stream")
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			return TurnResult{Events: emitter.Events()}, err
		}

		toolCalls := make([]ai.ContentBlock, 0, 2)
		for event := range stream.Events() {
			switch event.Type {
			case ai.EventTextDelta:
				if event.Delta == "" {
					continue
				}
				if err := emitter.Emit(EventTypeAgentDelta, map[string]any{"delta": event.Delta}); err != nil {
					return TurnResult{Events: emitter.Events()}, err
				}
			case ai.EventToolCallEnd:
				if event.ToolCall != nil {
					toolCalls = append(toolCalls, event.ToolCall.Clone())
				}
			case ai.EventError:
				if event.Error == nil {
					continue
				}
				if err := emitter.Emit(EventTypeError, map[string]any{"message": event.Error.ErrorMessage}); err != nil {
					return TurnResult{Events: emitter.Events()}, err
				}
			}
		}

		assistant, err := stream.Result(ctx)
		if err != nil {
			if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
				return TurnResult{Events: emitter.Events()}, emitErr
			}
			return TurnResult{Events: emitter.Events()}, err
		}

		if len(toolCalls) == 0 {
			for _, block := range assistant.Content {
				if block.Type == ai.ContentTypeToolCall {
					toolCalls = append(toolCalls, block.Clone())
				}
			}
		}

		conversation.Messages = append(conversation.Messages, assistant.ToMessage())

		if len(toolCalls) == 0 {
			finalText := extractText(assistant.Content)
			if err := emitter.Emit(EventTypeAgentMsg, map[string]any{"text": finalText}); err != nil {
				return TurnResult{Events: emitter.Events()}, err
			}
			s.Messages = boundMessages(conversation.Messages, a.Config.MaxContextMessages)
			return TurnResult{FinalText: finalText, Events: emitter.Events()}, nil
		}

		for _, toolCall := range toolCalls {
			if err := emitter.Emit(EventTypeToolCall, map[string]any{"name": toolCall.Name, "args": toolCall.Arguments}); err != nil {
				return TurnResult{Events: emitter.Events()}, err
			}

			output, runErr := runner.Run(ctx, s, toolCall)
			if runErr != nil {
				if err := emitter.Emit(EventTypeError, map[string]any{"message": runErr.Error()}); err != nil {
					return TurnResult{Events: emitter.Events()}, err
				}
			}
			if err := emitter.Emit(EventTypeToolResult, map[string]any{
				"name":   toolCall.Name,
				"result": output.Result,
				"status": output.Status,
			}); err != nil {
				return TurnResult{Events: emitter.Events()}, err
			}

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

			conversation.Messages = append(conversation.Messages, ai.NewToolResultMessage(
				toolCallID,
				toolCall.Name,
				[]ai.ContentBlock{{Type: ai.ContentTypeText, Text: toolText}},
				runErr != nil,
			))
		}
	}

	err = fmt.Errorf("max tool rounds exceeded (%d)", MaxToolRounds)
	if emitErr := emitter.Emit(EventTypeError, map[string]any{"message": err.Error()}); emitErr != nil {
		return TurnResult{Events: emitter.Events()}, emitErr
	}
	return TurnResult{Events: emitter.Events()}, err
}
