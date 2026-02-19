package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type cronTool struct{}

func (t *cronTool) Name() string {
	return "cron"
}

func (t *cronTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Manage gateway cron jobs that inject user messages into sessions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type": "string",
					"enum": []any{"create", "list", "delete", "pause", "resume"},
				},
				"session_id": map[string]any{"type": "string"},
				"cron_expr":  map[string]any{"type": "string"},
				"timezone":   map[string]any{"type": "string"},
				"message":    map[string]any{"type": "string"},
				"job_id":     map[string]any{"type": "string"},
			},
			"required": []any{"action"},
		},
	}
}

func (t *cronTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil {
		err := fmt.Errorf("agent is required")
		slog.Error("cron_tool: agent is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Agent.Cron == nil {
		err := fmt.Errorf("cron service is unavailable")
		slog.Warn("cron_tool: cron service is unavailable")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	action, err := requiredStringArg(input.Args, "action")
	if err != nil {
		slog.Error("cron_tool: action arg required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	action = strings.TrimSpace(action)
	slog.Debug("cron_tool: executing action", "action", action, "agent_id", input.Agent.ID, "session_id", input.Session.ID)
	switch action {
	case "create":
		sessionID, _ := optionalStringArg(input.Args, "session_id")
		if strings.TrimSpace(sessionID) == "" && input.Session != nil {
			sessionID = strings.TrimSpace(input.Session.ID)
		}
		message, err := requiredStringArg(input.Args, "message")
		if err != nil {
			slog.Error("cron_tool: message arg required for create")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		cronExpr, err := requiredStringArg(input.Args, "cron_expr")
		if err != nil {
			slog.Error("cron_tool: cron_expr arg required for create")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		timezone, _ := optionalStringArg(input.Args, "timezone")
		slog.Debug("cron_tool: creating cron job",
			"session_id", sessionID,
			"cron_expr", cronExpr,
			"timezone", timezone,
			"message_length", len(message),
		)
		job, createErr := input.Agent.Cron.CreateCronJob(ctx, CronCreateRequest{
			SessionID: sessionID,
			Message:   message,
			CronExpr:  cronExpr,
			Timezone:  timezone,
			CreatedBy: "agent:" + input.Agent.ID,
		})
		if createErr != nil {
			slog.Error("cron_tool: failed to create cron job", "error", createErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": createErr.Error()}}, createErr
		}
		slog.Info("cron_tool: cron job created", "job_id", job.ID, "cron_expr", cronExpr)
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action": "create",
				"job":    job,
			},
		}, nil
	case "list":
		sessionID, _ := optionalStringArg(input.Args, "session_id")
		if strings.TrimSpace(sessionID) == "" && input.Session != nil {
			sessionID = strings.TrimSpace(input.Session.ID)
		}
		slog.Debug("cron_tool: listing cron jobs", "session_id", sessionID)
		jobs, listErr := input.Agent.Cron.ListCronJobs(ctx, CronListRequest{SessionID: sessionID})
		if listErr != nil {
			slog.Error("cron_tool: failed to list cron jobs", "error", listErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": listErr.Error()}}, listErr
		}
		slog.Debug("cron_tool: listed cron jobs", "count", len(jobs))
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action": "list",
				"jobs":   jobs,
			},
		}, nil
	case "delete":
		jobID, err := requiredStringArg(input.Args, "job_id")
		if err != nil {
			slog.Error("cron_tool: job_id arg required for delete")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Debug("cron_tool: deleting cron job", "job_id", jobID)
		deleted, deleteErr := input.Agent.Cron.DeleteCronJob(ctx, jobID)
		if deleteErr != nil {
			slog.Error("cron_tool: failed to delete cron job", "job_id", jobID, "error", deleteErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": deleteErr.Error()}}, deleteErr
		}
		slog.Info("cron_tool: cron job deleted", "job_id", jobID, "deleted", deleted)
		return ToolOutput{
			Status: ToolStatusOK,
			Result: map[string]any{
				"action":  "delete",
				"job_id":  jobID,
				"deleted": deleted,
			},
		}, nil
	case "pause":
		jobID, err := requiredStringArg(input.Args, "job_id")
		if err != nil {
			slog.Error("cron_tool: job_id arg required for pause")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Debug("cron_tool: pausing cron job", "job_id", jobID)
		job, pauseErr := input.Agent.Cron.PauseCronJob(ctx, jobID)
		if pauseErr != nil {
			slog.Error("cron_tool: failed to pause cron job", "job_id", jobID, "error", pauseErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": pauseErr.Error()}}, pauseErr
		}
		slog.Info("cron_tool: cron job paused", "job_id", jobID)
		return ToolOutput{Status: ToolStatusOK, Result: map[string]any{"action": "pause", "job": job}}, nil
	case "resume":
		jobID, err := requiredStringArg(input.Args, "job_id")
		if err != nil {
			slog.Error("cron_tool: job_id arg required for resume")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		slog.Debug("cron_tool: resuming cron job", "job_id", jobID)
		job, resumeErr := input.Agent.Cron.ResumeCronJob(ctx, jobID)
		if resumeErr != nil {
			slog.Error("cron_tool: failed to resume cron job", "job_id", jobID, "error", resumeErr)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": resumeErr.Error()}}, resumeErr
		}
		slog.Info("cron_tool: cron job resumed", "job_id", jobID)
		return ToolOutput{Status: ToolStatusOK, Result: map[string]any{"action": "resume", "job": job}}, nil
	default:
		err := fmt.Errorf("unsupported action %q", action)
		slog.Error("cron_tool: unsupported action", "action", action)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
}
