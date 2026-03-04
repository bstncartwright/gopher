package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

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
		if displayName := normalizeDisplayName(record.DisplayName); displayName != "" {
			replayed.DisplayName = displayName
		} else if replayed.DisplayName == "" {
			replayed.DisplayName = defaultSessionDisplayName(replayed.CreatedAt, replayed.Participants)
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
			if err := m.clearRecoveredInFlight(ctx, rt); err != nil {
				return fmt.Errorf("clear in-flight state for recovered session %s: %w", record.SessionID, err)
			}
		}

		go m.runSession(rt)
	}

	return nil
}

func (m *Manager) clearRecoveredInFlight(ctx context.Context, rt *sessionRuntime) error {
	rt.inFlight = false
	return m.persistSessionRecord(ctx, rt, m.now().UTC())
}
