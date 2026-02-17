package session

import (
	"context"
	"time"
)

type EventPublisher interface {
	PublishEvent(ctx context.Context, event Event) error
}

func (m *Manager) appendPersistedEvent(ctx context.Context, rt *sessionRuntime, event Event) error {
	if err := m.store.Append(ctx, event); err != nil {
		return err
	}

	nextStatus := applyStatusTransition(rt.session.Status, event)
	if nextStatus != rt.session.Status {
		m.setSessionStatus(rt, nextStatus)
	}
	if err := m.persistSessionRecord(ctx, rt, event.Timestamp); err != nil {
		return err
	}
	if err := m.publishEvent(ctx, event); err != nil {
		return err
	}
	return nil
}

func (m *Manager) persistSessionRecord(ctx context.Context, rt *sessionRuntime, updatedAt time.Time) error {
	if m.registry == nil {
		return nil
	}
	if updatedAt.IsZero() {
		updatedAt = m.now().UTC()
	}
	record := SessionRecord{
		SessionID: rt.sessionID,
		Status:    rt.session.Status,
		CreatedAt: rt.session.CreatedAt,
		UpdatedAt: updatedAt,
		LastSeq:   rt.nextSeq,
		InFlight:  rt.inFlight,
	}
	return m.registry.UpsertSession(ctx, record)
}

func (m *Manager) setInFlight(ctx context.Context, rt *sessionRuntime, inFlight bool) error {
	rt.inFlight = inFlight
	return m.persistSessionRecord(ctx, rt, m.now().UTC())
}

func (m *Manager) publishEvent(ctx context.Context, event Event) error {
	if m.publisher == nil {
		return nil
	}
	return m.publisher.PublishEvent(ctx, event)
}
