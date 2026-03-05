package gateway

import (
	"context"
	"fmt"
	"strings"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type SessionCronDispatcher struct {
	manager sessionrt.SessionManager
}

func NewSessionCronDispatcher(manager sessionrt.SessionManager) (*SessionCronDispatcher, error) {
	if manager == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	return &SessionCronDispatcher{manager: manager}, nil
}

func (d *SessionCronDispatcher) Dispatch(ctx context.Context, job CronJob, firedAt time.Time) (CronDispatchResult, error) {
	if normalizeCronMode(job.Mode) == CronModeIsolated {
		return CronDispatchResult{}, fmt.Errorf("session cron dispatcher does not support isolated mode")
	}
	content := BuildScheduledTaskPrompt(job, firedAt, CronModeSession)
	err := d.manager.SendEvent(ctx, sessionrt.Event{
		SessionID: sessionrt.SessionID(job.SessionID),
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: content,
		},
	})
	if err != nil {
		return CronDispatchResult{}, err
	}
	return CronDispatchResult{Status: CronRunStatusCompleted}, nil
}

func (d *SessionCronDispatcher) Poll(_ context.Context, _ CronJob, _ time.Time) (CronDispatchResult, error) {
	return CronDispatchResult{Status: CronRunStatusCompleted}, nil
}

func BuildScheduledTaskPrompt(job CronJob, firedAt time.Time, mode string) string {
	lines := []string{
		"[scheduled task]",
		"task_id: " + strings.TrimSpace(job.ID),
	}
	if title := strings.TrimSpace(job.Title); title != "" {
		lines = append(lines, "title: "+title)
	}
	lines = append(lines,
		"scheduled_for: "+formatScheduledFor(job),
		"fired_at: "+firedAt.UTC().Format(time.RFC3339),
		"mode: "+normalizeCronMode(mode),
		"",
		"Instructions:",
		strings.TrimSpace(job.Message),
	)
	return strings.Join(lines, "\n")
}

func BuildScheduledTaskResult(job CronJob, status, summary string) string {
	lines := []string{
		"[scheduled task result]",
		"task_id: " + strings.TrimSpace(job.ID),
	}
	if title := strings.TrimSpace(job.Title); title != "" {
		lines = append(lines, "title: "+title)
	}
	lines = append(lines,
		"run_status: "+strings.ToLower(strings.TrimSpace(status)),
		"summary: "+strings.TrimSpace(summary),
	)
	return strings.Join(lines, "\n")
}

func formatScheduledFor(job CronJob) string {
	if job.NextRunAt == nil || job.NextRunAt.IsZero() {
		return ""
	}
	location, err := loadCronLocation(job.Timezone)
	if err != nil {
		return job.NextRunAt.UTC().Format(time.RFC3339)
	}
	return job.NextRunAt.In(location).Format("2006-01-02 15:04:05 MST")
}
