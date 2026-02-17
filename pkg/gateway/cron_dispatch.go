package gateway

import (
	"context"
	"fmt"
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

func (d *SessionCronDispatcher) Dispatch(ctx context.Context, job CronJob, _ time.Time) error {
	return d.manager.SendEvent(ctx, sessionrt.Event{
		SessionID: sessionrt.SessionID(job.SessionID),
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: job.Message,
		},
	})
}
