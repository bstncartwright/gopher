package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type processTool struct{}

func (t *processTool) Name() string {
	return "process"
}

func (t *processTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Manage background exec sessions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []any{"list", "poll", "log", "write", "kill"},
				},
				"session_id": map[string]any{"type": "string"},
				"data":       map[string]any{"type": "string"},
				"offset":     map[string]any{"type": "integer"},
				"limit":      map[string]any{"type": "integer"},
			},
			"required": []any{"action"},
		},
	}
}

func (t *processTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	pm := input.Agent.Processes
	if pm == nil {
		err := fmt.Errorf("process manager not available")
		slog.Error("process_tool: process manager not available")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	action, err := requiredStringArg(input.Args, "action")
	if err != nil {
		slog.Error("process_tool: action arg required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	slog.Debug("process_tool: executing action", "action", action, "session_id", input.Session.ID)

	switch action {
	case "list":
		sessions := pm.List()
		summaries := make([]map[string]any, 0, len(sessions))
		for _, s := range sessions {
			summaries = append(summaries, map[string]any{
				"session_id": s.ID,
				"command":    s.Command,
				"pid":        s.PID,
				"running":    !s.Done,
				"exit_code":  s.ExitCode,
			})
		}
		slog.Debug("process_tool: listed sessions", "count", len(summaries))
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{"sessions": summaries},
		}, nil

	case "poll":
		sessionID, err := requiredStringArg(input.Args, "session_id")
		if err != nil {
			slog.Error("process_tool: session_id arg required for poll")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Debug("process_tool: polling session", "process_session_id", sessionID)
		newOutput, exitCode, done, err := pm.Poll(sessionID)
		if err != nil {
			slog.Error("process_tool: poll failed", "process_session_id", sessionID, "error", err)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Debug("process_tool: poll result", "process_session_id", sessionID, "done", done, "exit_code", exitCode, "output_lines", len(newOutput))
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"session_id": sessionID,
				"output":     strings.Join(newOutput, "\n"),
				"done":       done,
				"exit_code":  exitCode,
			},
		}, nil

	case "log":
		sessionID, err := requiredStringArg(input.Args, "session_id")
		if err != nil {
			slog.Error("process_tool: session_id arg required for log")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		var offset, limit int
		if raw, exists := input.Args["offset"]; exists {
			if v, ok := toInt(raw); ok {
				offset = v
			}
		}
		if raw, exists := input.Args["limit"]; exists {
			if v, ok := toInt(raw); ok {
				limit = v
			}
		}
		slog.Debug("process_tool: getting log", "process_session_id", sessionID, "offset", offset, "limit", limit)
		lines, total, err := pm.Log(sessionID, offset, limit)
		if err != nil {
			slog.Error("process_tool: log failed", "process_session_id", sessionID, "error", err)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"session_id": sessionID,
				"output":     strings.Join(lines, "\n"),
				"total":      total,
			},
		}, nil

	case "write":
		sessionID, err := requiredStringArg(input.Args, "session_id")
		if err != nil {
			slog.Error("process_tool: session_id arg required for write")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		data, err := requiredStringArg(input.Args, "data")
		if err != nil {
			slog.Error("process_tool: data arg required for write")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Debug("process_tool: writing to session", "process_session_id", sessionID, "data_length", len(data))
		if err := pm.Write(sessionID, data); err != nil {
			slog.Error("process_tool: write failed", "process_session_id", sessionID, "error", err)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"session_id": sessionID,
				"written":    len(data),
			},
		}, nil

	case "kill":
		sessionID, err := requiredStringArg(input.Args, "session_id")
		if err != nil {
			slog.Error("process_tool: session_id arg required for kill")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Debug("process_tool: killing session", "process_session_id", sessionID)
		if err := pm.Kill(sessionID); err != nil {
			slog.Error("process_tool: kill failed", "process_session_id", sessionID, "error", err)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Info("process_tool: session killed", "process_session_id", sessionID)
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"session_id": sessionID,
				"killed":     true,
			},
		}, nil

	default:
		err := fmt.Errorf("unknown action %q", action)
		slog.Error("process_tool: unknown action", "action", action)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
}
