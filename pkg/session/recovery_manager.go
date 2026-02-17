package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

const inFlightRecoveryReason = "gateway_recovered_inflight_aborted"

func (m *Manager) Recover(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if m.registry == nil {
		return nil
	}

	records, err := m.registry.ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SessionID < records[j].SessionID
	})

	for _, record := range records {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		events, err := m.store.List(ctx, record.SessionID)
		if err != nil {
			if errors.Is(err, ErrSessionNotFound) {
				continue
			}
			return fmt.Errorf("load events for %s: %w", record.SessionID, err)
		}
		replayed, err := Replay(events)
		if err != nil {
			return fmt.Errorf("replay session %s: %w", record.SessionID, err)
		}

		rt := newSessionRuntime(replayed)
		rt.inFlight = record.InFlight
		if record.LastSeq > 0 {
			rt.nextSeq = record.LastSeq
		} else if len(events) > 0 {
			rt.nextSeq = events[len(events)-1].Seq
		}

		m.mu.Lock()
		if _, exists := m.sessions[record.SessionID]; exists {
			m.mu.Unlock()
			continue
		}
		m.sessions[record.SessionID] = rt
		m.mu.Unlock()

		if rt.inFlight && rt.session.Status == SessionActive {
			if err := m.markRecoveredInFlightFailure(ctx, rt); err != nil {
				return fmt.Errorf("recover in-flight session %s: %w", record.SessionID, err)
			}
		}

		go m.runSession(rt)
	}

	return nil
}

func (m *Manager) markRecoveredInFlightFailure(ctx context.Context, rt *sessionRuntime) error {
	rt.inFlight = false

	errEvent, err := m.canonicalizeEvent(rt, Event{
		SessionID: rt.sessionID,
		From:      SystemActorID,
		Type:      EventError,
		Payload: ErrorPayload{
			Message: "in-flight work aborted during gateway restart",
		},
	})
	if err != nil {
		return err
	}
	if err := m.appendPersistedEvent(ctx, rt, errEvent); err != nil {
		return err
	}

	failedEvent, err := m.canonicalizeEvent(rt, Event{
		SessionID: rt.sessionID,
		From:      SystemActorID,
		Type:      EventControl,
		Payload: ControlPayload{
			Action: ControlActionSessionFailed,
			Reason: inFlightRecoveryReason,
		},
	})
	if err != nil {
		return err
	}
	if err := m.appendPersistedEvent(ctx, rt, failedEvent); err != nil {
		return err
	}

	return nil
}
