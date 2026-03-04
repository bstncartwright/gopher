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
		Description: "Spawn and manage delegated subagent sessions. `action:\"create\"` is async fire-and-forget: it returns immediately after spawn. After `create`, do not block this turn with polling (`list`/`log`) to wait for completion; resume delegated follow-up only after a later `delegation.completed`/`delegation.failed`/`delegation.cancelled` event.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []any{"create", "list", "kill", "log"},
					"description": "Delegation action. `create` spawns a subagent asynchronously and returns immediately; do not call `list`/`log` in the same turn to wait for completion.",
				},
				"target_agent": map[string]any{
					"type":        "string",
					"description": "Optional target worker id. Omit to auto-create an ephemeral subagent.",
				},
				"model_policy": map[string]any{
					"type":        "string",
					"description": "Optional model override for ephemeral workers created via `create`.",
				},
				"message": map[string]any{
					"type":        "string",
					"description": "Required when `action` is `create`. Task-specific kickoff instruction for the delegated worker.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Optional short title for the delegated session.",
				},
				"delegation_id": map[string]any{
					"type":        "string",
					"description": "Required for `kill` and `log`. Delegated session id to operate on.",
				},
				"include_inactive": map[string]any{
					"type":        "boolean",
					"description": "Optional for `list`. Include completed/failed/cancelled delegations when true.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Optional for `log`. Zero-based log entry offset.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Optional for `log`. Max entries to return (capped server-side).",
				},
			},
			"required": []any{"action"},
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

	sessionID := ""
	if input.Session != nil {
		sessionID = strings.TrimSpace(input.Session.ID)
	}

	switch strings.TrimSpace(action) {
	case "create":
		targetAgentID, _ := optionalStringArg(input.Args, "target_agent")
		targetAgentID = strings.TrimSpace(targetAgentID)
		modelPolicy, _ := optionalStringArg(input.Args, "model_policy")
		message, err := requiredStringArg(input.Args, "message")
		if err != nil {
			slog.Error("delegate_tool: message arg required")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		title, _ := optionalStringArg(input.Args, "title")

		slog.Debug("delegate_tool: creating delegation",
			"source_agent_id", input.Agent.ID,
			"target_agent_id", targetAgentID,
			"model_policy", strings.TrimSpace(modelPolicy),
			"source_session_id", sessionID,
			"title", title,
			"message_length", len(message),
		)

		result, createErr := input.Agent.Delegation.CreateDelegationSession(ctx, DelegationCreateRequest{
			SourceSessionID: sessionID,
			SourceAgentID:   strings.TrimSpace(input.Agent.ID),
			TargetAgentID:   strings.TrimSpace(targetAgentID),
			ModelPolicy:     strings.TrimSpace(modelPolicy),
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
				"lifecycle": map[string]any{
					"mode":                  "async_spawn",
					"terminal_events":       []any{"delegation.completed", "delegation.failed", "delegation.cancelled"},
					"wait_for_event":        true,
					"wait_in_same_turn":     false,
					"polling_guidance":      "Do not call delegate list/log in this turn to wait for completion.",
					"recommended_next_step": "End this turn and continue delegated follow-up only when a later terminal delegation event arrives.",
				},
			},
		}, nil

	case "list":
		includeInactive, err := optionalBoolStrictArg(input.Args, "include_inactive")
		if err != nil {
			slog.Error("delegate_tool: include_inactive must be boolean", "error", err)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		items, listErr := input.Agent.Delegation.ListDelegationSessions(ctx, DelegationListRequest{
			SourceSessionID: sessionID,
			IncludeInactive: includeInactive,
		})
		if listErr != nil {
			slog.Error("delegate_tool: failed to list delegations", "source_session_id", sessionID, "error", listErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": listErr.Error()}}, listErr
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action":      "list",
				"delegations": items,
				"count":       len(items),
			},
		}, nil

	case "kill":
		delegationID, err := requiredStringArg(input.Args, "delegation_id")
		if err != nil {
			slog.Error("delegate_tool: delegation_id arg required for kill")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		result, killErr := input.Agent.Delegation.KillDelegationSession(ctx, DelegationKillRequest{
			SourceSessionID: sessionID,
			DelegationID:    strings.TrimSpace(delegationID),
		})
		if killErr != nil {
			slog.Error("delegate_tool: failed to kill delegation", "delegation_id", delegationID, "error", killErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": killErr.Error()}}, killErr
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action":     "kill",
				"delegation": result,
			},
		}, nil

	case "log":
		delegationID, err := requiredStringArg(input.Args, "delegation_id")
		if err != nil {
			slog.Error("delegate_tool: delegation_id arg required for log")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		offset := 0
		if raw, exists := input.Args["offset"]; exists {
			if v, ok := toInt(raw); ok {
				offset = v
			} else {
				err := fmt.Errorf("offset must be an integer")
				return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
			}
		}
		limit := 50
		if raw, exists := input.Args["limit"]; exists {
			if v, ok := toInt(raw); ok {
				limit = v
			} else {
				err := fmt.Errorf("limit must be an integer")
				return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
			}
		}
		if offset < 0 {
			offset = 0
		}
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		result, logErr := input.Agent.Delegation.GetDelegationLog(ctx, DelegationLogRequest{
			SourceSessionID: sessionID,
			DelegationID:    strings.TrimSpace(delegationID),
			Offset:          offset,
			Limit:           limit,
		})
		if logErr != nil {
			slog.Error("delegate_tool: failed to fetch delegation log", "delegation_id", delegationID, "error", logErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": logErr.Error()}}, logErr
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action": "log",
				"log":    result,
			},
		}, nil
	default:
		err := fmt.Errorf("unsupported action %q", action)
		slog.Error("delegate_tool: unsupported action", "action", action)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
}

func optionalBoolStrictArg(args map[string]any, key string) (bool, error) {
	if args == nil {
		return false, nil
	}
	raw, exists := args[key]
	if !exists || raw == nil {
		return false, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return value, nil
}
