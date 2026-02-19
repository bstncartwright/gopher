package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type delegateTool struct{}

func (t *delegateTool) Name() string {
	return "delegate"
}

func (t *delegateTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Create a new delegation session and room where another agent can be tagged to collaborate.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []any{"create"},
				},
				"target_agent": map[string]any{"type": "string"},
				"message":      map[string]any{"type": "string"},
				"title":        map[string]any{"type": "string"},
			},
			"required": []any{"action", "target_agent", "message"},
		},
	}
}

func (t *delegateTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil {
		err := fmt.Errorf("agent is required")
		slog.Error("delegate_tool: agent is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Agent.Delegation == nil {
		err := fmt.Errorf("delegation service is unavailable")
		slog.Warn("delegate_tool: delegation service is unavailable")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	action, err := requiredStringArg(input.Args, "action")
	if err != nil {
		slog.Error("delegate_tool: action arg required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if strings.TrimSpace(action) != "create" {
		err := fmt.Errorf("unsupported action %q", action)
		slog.Error("delegate_tool: unsupported action", "action", action)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	targetAgentID, err := requiredStringArg(input.Args, "target_agent")
	if err != nil {
		slog.Error("delegate_tool: target_agent arg required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	message, err := requiredStringArg(input.Args, "message")
	if err != nil {
		slog.Error("delegate_tool: message arg required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	title, _ := optionalStringArg(input.Args, "title")

	sessionID := ""
	if input.Session != nil {
		sessionID = strings.TrimSpace(input.Session.ID)
	}

	slog.Debug("delegate_tool: creating delegation",
		"source_agent_id", input.Agent.ID,
		"target_agent_id", targetAgentID,
		"source_session_id", sessionID,
		"title", title,
		"message_length", len(message),
	)

	result, createErr := input.Agent.Delegation.CreateDelegationSession(ctx, DelegationCreateRequest{
		SourceSessionID: sessionID,
		SourceAgentID:   strings.TrimSpace(input.Agent.ID),
		TargetAgentID:   strings.TrimSpace(targetAgentID),
		Message:         strings.TrimSpace(message),
		Title:           strings.TrimSpace(title),
	})
	if createErr != nil {
		slog.Error("delegate_tool: failed to create delegation",
			"target_agent_id", targetAgentID,
			"error", createErr,
		)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": createErr.Error()}}, createErr
	}

	slog.Info("delegate_tool: delegation created",
		"source_agent_id", input.Agent.ID,
		"target_agent_id", targetAgentID,
		"delegation_session_id", result.SessionID,
	)
	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{
			"action":     "create",
			"delegation": result,
		},
	}, nil
}
