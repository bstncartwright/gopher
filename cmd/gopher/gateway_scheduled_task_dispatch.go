package main

import (
	"context"
	"fmt"
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
		return nil, fmt.Errorf("session manager is required")
	}
	sessionDispatcher, err := gateway.NewSessionCronDispatcher(manager)
	if err != nil {
		return nil, err
	}
	return &scheduledTaskCronDispatcher{
		manager:    manager,
		delegation: delegation,
		session:    sessionDispatcher,
	}, nil
}

func (d *scheduledTaskCronDispatcher) Dispatch(ctx context.Context, job gateway.CronJob, firedAt time.Time) (gateway.CronDispatchResult, error) {
	switch gatewayMode(job.Mode) {
	case gateway.CronModeSession:
		return d.session.Dispatch(ctx, job, firedAt)
	case gateway.CronModeIsolated:
		if d.delegation == nil {
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
			return gateway.CronDispatchResult{}, err
		}
		return gateway.CronDispatchResult{
			Status:      gateway.CronRunStatusRunning,
			Summary:     strings.TrimSpace(result.Announcement),
			ActiveRunID: strings.TrimSpace(result.SessionID),
		}, nil
	default:
		return gateway.CronDispatchResult{}, fmt.Errorf("unsupported cron mode %q", strings.TrimSpace(job.Mode))
	}
}

func (d *scheduledTaskCronDispatcher) Poll(ctx context.Context, job gateway.CronJob, _ time.Time) (gateway.CronDispatchResult, error) {
	switch gatewayMode(job.Mode) {
	case gateway.CronModeSession:
		return gateway.CronDispatchResult{Status: gateway.CronRunStatusCompleted}, nil
	case gateway.CronModeIsolated:
		if d.delegation == nil {
			return gateway.CronDispatchResult{}, fmt.Errorf("delegation service is unavailable")
		}
		summary, err := d.delegation.GetDelegationSummary(ctx, agentcore.DelegationSummaryRequest{
			SourceSessionID: job.SessionID,
			DelegationID:    job.ActiveRunID,
		})
		if err != nil {
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
				return gateway.CronDispatchResult{}, err
			}
			result := gateway.CronDispatchResult{
				Status:  status,
				Summary: text,
			}
			if status == gateway.CronRunStatusFailed {
				result.Error = text
			}
			return result, nil
		default:
			return gateway.CronDispatchResult{}, fmt.Errorf("unsupported delegation status %q", summary.Status)
		}
	default:
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
