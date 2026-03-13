package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type scheduledTaskCronDispatcher struct {
	manager    sessionrt.SessionManager
	delegation agentcore.DelegationToolService
	session    *gateway.SessionCronDispatcher
}

func newScheduledTaskCronDispatcher(manager sessionrt.SessionManager, delegation agentcore.DelegationToolService) (*scheduledTaskCronDispatcher, error) {
	if manager == nil {
		slog.Error("gateway_scheduled_task_dispatch: session manager is required")
		return nil, fmt.Errorf("session manager is required")
	}
	sessionDispatcher, err := gateway.NewSessionCronDispatcher(manager)
	if err != nil {
		slog.Error("gateway_scheduled_task_dispatch: failed to create session cron dispatcher", "error", err)
		return nil, err
	}
	slog.Info("gateway_scheduled_task_dispatch: cron dispatcher initialized", "delegation_enabled", delegation != nil)
	return &scheduledTaskCronDispatcher{
		manager:    manager,
		delegation: delegation,
		session:    sessionDispatcher,
	}, nil
}

func (d *scheduledTaskCronDispatcher) Dispatch(ctx context.Context, job gateway.CronJob, firedAt time.Time) (gateway.CronDispatchResult, error) {
	slog.Info(
		"gateway_scheduled_task_dispatch: dispatching scheduled task",
		"job_id", job.ID,
		"mode", gatewayMode(job.Mode),
		"session_id", job.SessionID,
		"notify_actor_id", job.NotifyActorID,
		"target_agent", job.TargetAgent,
		"fired_at", firedAt.UTC().Format(time.RFC3339Nano),
	)
	switch gatewayMode(job.Mode) {
	case gateway.CronModeSession:
		return d.session.Dispatch(ctx, job, firedAt)
	case gateway.CronModeIsolated:
		if d.delegation == nil {
			slog.Error("gateway_scheduled_task_dispatch: delegation service unavailable", "job_id", job.ID)
			return gateway.CronDispatchResult{}, fmt.Errorf("delegation service is unavailable")
		}
		result, err := d.delegation.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
			SourceSessionID:         job.SessionID,
			SourceAgentID:           job.NotifyActorID,
			TargetAgentID:           job.TargetAgent,
			ModelPolicy:             job.ModelPolicy,
			Message:                 gateway.BuildScheduledTaskPrompt(job, firedAt, gateway.CronModeIsolated),
			Title:                   strings.TrimSpace(job.Title),
			SuppressTerminalMessage: true,
		})
		if err != nil {
			slog.Error("gateway_scheduled_task_dispatch: failed to create delegation session", "job_id", job.ID, "error", err)
			return gateway.CronDispatchResult{}, err
		}
		slog.Info("gateway_scheduled_task_dispatch: delegation session created", "job_id", job.ID, "delegation_session_id", strings.TrimSpace(result.SessionID))
		return gateway.CronDispatchResult{
			Status:      gateway.CronRunStatusRunning,
			Summary:     strings.TrimSpace(result.Announcement),
			ActiveRunID: strings.TrimSpace(result.SessionID),
		}, nil
	default:
		slog.Error("gateway_scheduled_task_dispatch: unsupported cron mode", "job_id", job.ID, "mode", strings.TrimSpace(job.Mode))
		return gateway.CronDispatchResult{}, fmt.Errorf("unsupported cron mode %q", strings.TrimSpace(job.Mode))
	}
}

func (d *scheduledTaskCronDispatcher) Poll(ctx context.Context, job gateway.CronJob, _ time.Time) (gateway.CronDispatchResult, error) {
	slog.Debug("gateway_scheduled_task_dispatch: polling scheduled task", "job_id", job.ID, "mode", gatewayMode(job.Mode), "active_run_id", strings.TrimSpace(job.ActiveRunID))
	switch gatewayMode(job.Mode) {
	case gateway.CronModeSession:
		return gateway.CronDispatchResult{Status: gateway.CronRunStatusCompleted}, nil
	case gateway.CronModeIsolated:
		if d.delegation == nil {
			slog.Error("gateway_scheduled_task_dispatch: delegation service unavailable during poll", "job_id", job.ID)
			return gateway.CronDispatchResult{}, fmt.Errorf("delegation service is unavailable")
		}
		summary, err := d.delegation.GetDelegationSummary(ctx, agentcore.DelegationSummaryRequest{
			SourceSessionID: job.SessionID,
			DelegationID:    job.ActiveRunID,
		})
		if err != nil {
			slog.Error("gateway_scheduled_task_dispatch: failed to poll delegation summary", "job_id", job.ID, "error", err)
			return gateway.CronDispatchResult{}, err
		}
		status := strings.ToLower(strings.TrimSpace(summary.Status))
		switch status {
		case "", "active":
			return gateway.CronDispatchResult{Status: gateway.CronRunStatusRunning}, nil
		case gateway.CronRunStatusCompleted, gateway.CronRunStatusFailed:
			text := strings.TrimSpace(summary.Summary)
			if text == "" {
				text = terminalStatusLabel(status)
			}
			if err := d.manager.SendEvent(ctx, sessionrt.Event{
				SessionID: sessionrt.SessionID(job.SessionID),
				From:      sessionrt.SystemActorID,
				Type:      sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:          sessionrt.RoleAgent,
					Content:       gateway.BuildScheduledTaskResult(job, status, text),
					TargetActorID: sessionrt.ActorID(job.NotifyActorID),
				},
			}); err != nil {
				slog.Error("gateway_scheduled_task_dispatch: failed to publish scheduled task result", "job_id", job.ID, "status", status, "error", err)
				return gateway.CronDispatchResult{}, err
			}
			slog.Info("gateway_scheduled_task_dispatch: scheduled task completed", "job_id", job.ID, "status", status)
			result := gateway.CronDispatchResult{
				Status:  status,
				Summary: text,
			}
			if status == gateway.CronRunStatusFailed {
				result.Error = text
			}
			return result, nil
		default:
			slog.Error("gateway_scheduled_task_dispatch: unsupported delegation status", "job_id", job.ID, "status", summary.Status)
			return gateway.CronDispatchResult{}, fmt.Errorf("unsupported delegation status %q", summary.Status)
		}
	default:
		slog.Error("gateway_scheduled_task_dispatch: unsupported cron mode during poll", "job_id", job.ID, "mode", strings.TrimSpace(job.Mode))
		return gateway.CronDispatchResult{}, fmt.Errorf("unsupported cron mode %q", strings.TrimSpace(job.Mode))
	}
}

func gatewayMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", gateway.CronModeSession:
		return gateway.CronModeSession
	case gateway.CronModeIsolated:
		return gateway.CronModeIsolated
	default:
		return ""
	}
}
