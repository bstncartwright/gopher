package gateway

import (
	"context"
	"encoding/json"
	"fmt"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type SessionEventPublisher struct {
	Fabric fabricts.Fabric
}

var _ sessionrt.EventPublisher = (*SessionEventPublisher)(nil)

func (p *SessionEventPublisher) PublishEvent(ctx context.Context, event sessionrt.Event) error {
	if p == nil || p.Fabric == nil {
		return nil
	}
	blob, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal session event: %w", err)
	}
	return p.Fabric.Publish(ctx, fabricts.Message{
		Subject: fabricts.SessionEventsSubject(string(event.SessionID)),
		Data:    blob,
	})
}
