package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type reactionTool struct{}

func (t *reactionTool) Name() string {
	return "reaction"
}

func (t *reactionTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "React to an existing conversation message.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"emoji": map[string]any{"type": "string"},
			},
			"required": []any{"emoji"},
		},
	}
}

func (t *reactionTool) Available(input ToolInput) bool {
	return input.Agent != nil && input.Agent.ReactionService != nil
}

func (t *reactionTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil {
		err := fmt.Errorf("agent is required")
		slog.Error("reaction_tool: agent is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Agent.ReactionService == nil {
		err := fmt.Errorf("reaction service is unavailable")
		slog.Warn("reaction_tool: reaction service is unavailable")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Session == nil || strings.TrimSpace(input.Session.ID) == "" {
		err := fmt.Errorf("session is required")
		slog.Error("reaction_tool: session is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	emoji, err := requiredStringArg(input.Args, "emoji")
	if err != nil {
		slog.Error("reaction_tool: emoji required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	emoji = strings.TrimSpace(emoji)

	result, sendErr := input.Agent.ReactionService.SendReaction(ctx, ReactionSendRequest{
		SessionID: strings.TrimSpace(input.Session.ID),
		Emoji:     emoji,
	})
	if sendErr != nil {
		slog.Error("reaction_tool: failed to send reaction", "error", sendErr)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": sendErr.Error()}}, sendErr
	}

	output := map[string]any{
		"sent":            result.Sent,
		"conversation_id": strings.TrimSpace(result.ConversationID),
		"target_event_id": strings.TrimSpace(result.TargetEventID),
		"emoji":           strings.TrimSpace(result.Emoji),
	}
	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}
