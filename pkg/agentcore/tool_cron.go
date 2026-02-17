package agentcore

import (
	"context"
	"fmt"
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
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if input.Agent.Cron == nil {
		err := fmt.Errorf("cron service is unavailable")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	action, err := requiredStringArg(input.Args, "action")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	action = strings.TrimSpace(action)
	switch action {
	case "create":
		sessionID, _ := optionalStringArg(input.Args, "session_id")
		if strings.TrimSpace(sessionID) == "" && input.Session != nil {
			sessionID = strings.TrimSpace(input.Session.ID)
		}
		message, err := requiredStringArg(input.Args, "message")
		if err != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		cronExpr, err := requiredStringArg(input.Args, "cron_expr")
		if err != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		timezone, _ := optionalStringArg(input.Args, "timezone")
		job, createErr := input.Agent.Cron.CreateCronJob(ctx, CronCreateRequest{
			SessionID: sessionID,
			Message:   message,
			CronExpr:  cronExpr,
			Timezone:  timezone,
			CreatedBy: "agent:" + input.Agent.ID,
		})
		if createErr != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": createErr.Error()}}, createErr
		}
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
		jobs, listErr := input.Agent.Cron.ListCronJobs(ctx, CronListRequest{SessionID: sessionID})
		if listErr != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": listErr.Error()}}, listErr
		}
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
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		deleted, deleteErr := input.Agent.Cron.DeleteCronJob(ctx, jobID)
		if deleteErr != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": deleteErr.Error()}}, deleteErr
		}
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
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		job, pauseErr := input.Agent.Cron.PauseCronJob(ctx, jobID)
		if pauseErr != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": pauseErr.Error()}}, pauseErr
		}
		return ToolOutput{Status: ToolStatusOK, Result: map[string]any{"action": "pause", "job": job}}, nil
	case "resume":
		jobID, err := requiredStringArg(input.Args, "job_id")
		if err != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		job, resumeErr := input.Agent.Cron.ResumeCronJob(ctx, jobID)
		if resumeErr != nil {
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": resumeErr.Error()}}, resumeErr
		}
		return ToolOutput{Status: ToolStatusOK, Result: map[string]any{"action": "resume", "job": job}}, nil
	default:
		err := fmt.Errorf("unsupported action %q", action)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
}
