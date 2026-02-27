package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type heartbeatTool struct{}

func (t *heartbeatTool) Name() string {
	return "heartbeat"
}

func (t *heartbeatTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Manage this agent's heartbeat schedule and prompt.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []any{"get", "set", "disable"},
				},
				"every":         map[string]any{"type": "string"},
				"prompt":        map[string]any{"type": "string"},
				"ack_max_chars": map[string]any{"type": "integer"},
				"session":       map[string]any{"type": "string"},
				"active_hours": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"start":    map[string]any{"type": "string"},
						"end":      map[string]any{"type": "string"},
						"timezone": map[string]any{"type": "string"},
					},
					"required": []any{"start", "end"},
				},
				"user_timezone": map[string]any{"type": "string"},
			},
			"required": []any{"action"},
		},
	}
}

func (t *heartbeatTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil {
		err := fmt.Errorf("agent is required")
		slog.Error("heartbeat_tool: agent is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Agent.HeartbeatService == nil {
		err := fmt.Errorf("heartbeat service is unavailable")
		slog.Warn("heartbeat_tool: heartbeat service is unavailable")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	action, err := requiredStringArg(input.Args, "action")
	if err != nil {
		slog.Error("heartbeat_tool: action arg required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	action = strings.TrimSpace(action)
	agentID := strings.TrimSpace(input.Agent.ID)

	switch action {
	case "get":
		state, getErr := input.Agent.HeartbeatService.GetHeartbeat(ctx, agentID)
		if getErr != nil {
			slog.Error("heartbeat_tool: failed to get heartbeat config", "agent_id", agentID, "error", getErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": getErr.Error()}}, getErr
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action":    "get",
				"heartbeat": state,
			},
		}, nil
	case "set":
		every, err := requiredStringArg(input.Args, "every")
		if err != nil {
			slog.Error("heartbeat_tool: every arg required for set")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		req := HeartbeatSetRequest{
			AgentID: agentID,
			Every:   strings.TrimSpace(every),
		}
		if prompt, ok := optionalStringArg(input.Args, "prompt"); ok {
			promptValue := prompt
			req.Prompt = &promptValue
		}
		if session, ok := optionalStringArg(input.Args, "session"); ok {
			sessionValue := session
			req.Session = &sessionValue
		}
		if rawActiveHours, ok := input.Args["active_hours"]; ok {
			activeHours, parseErr := parseHeartbeatActiveHoursArg(rawActiveHours)
			if parseErr != nil {
				slog.Error("heartbeat_tool: invalid active_hours", "error", parseErr)
				return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": parseErr.Error()}}, parseErr
			}
			req.ActiveHours = activeHours
		}
		if tz, ok := optionalStringArg(input.Args, "user_timezone"); ok {
			timezoneValue := tz
			req.UserTimezone = &timezoneValue
		}
		if rawAck, ok := input.Args["ack_max_chars"]; ok {
			ackMaxChars, ok := toInt(rawAck)
			if !ok {
				err := fmt.Errorf("ack_max_chars must be an integer")
				slog.Error("heartbeat_tool: invalid ack_max_chars type", "value", rawAck)
				return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
			}
			req.AckMaxChars = &ackMaxChars
		}

		state, setErr := input.Agent.HeartbeatService.SetHeartbeat(ctx, req)
		if setErr != nil {
			slog.Error("heartbeat_tool: failed to set heartbeat config", "agent_id", agentID, "error", setErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": setErr.Error()}}, setErr
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action":    "set",
				"heartbeat": state,
			},
		}, nil
	case "disable":
		state, disableErr := input.Agent.HeartbeatService.DisableHeartbeat(ctx, agentID)
		if disableErr != nil {
			slog.Error("heartbeat_tool: failed to disable heartbeat config", "agent_id", agentID, "error", disableErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": disableErr.Error()}}, disableErr
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action":    "disable",
				"heartbeat": state,
			},
		}, nil
	default:
		err := fmt.Errorf("unsupported action %q", action)
		slog.Error("heartbeat_tool: unsupported action", "action", action)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
}

func parseHeartbeatActiveHoursArg(raw any) (*HeartbeatActiveHoursConfig, error) {
	if raw == nil {
		return nil, nil
	}
	body, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("active_hours must be an object")
	}
	startRaw, ok := body["start"]
	if !ok {
		return nil, fmt.Errorf("active_hours.start is required")
	}
	start, ok := startRaw.(string)
	if !ok {
		return nil, fmt.Errorf("active_hours.start must be a string")
	}
	endRaw, ok := body["end"]
	if !ok {
		return nil, fmt.Errorf("active_hours.end is required")
	}
	end, ok := endRaw.(string)
	if !ok {
		return nil, fmt.Errorf("active_hours.end must be a string")
	}
	timezone := ""
	if timezoneRaw, exists := body["timezone"]; exists {
		timezoneValue, ok := timezoneRaw.(string)
		if !ok {
			return nil, fmt.Errorf("active_hours.timezone must be a string")
		}
		timezone = timezoneValue
	}
	return &HeartbeatActiveHoursConfig{
		Start:    start,
		End:      end,
		Timezone: timezone,
	}, nil
}
