package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
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
		m.restoreSessionRuntime(rt, record)
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

		go m.runSession(rt)

		if rt.session.Status == SessionActive {
			if err := m.prepareRecoveredSession(ctx, rt, events); err != nil {
				return fmt.Errorf("prepare recovered session %s: %w", record.SessionID, err)
			}
		}
	}

	return nil
}

func (m *Manager) restoreSessionRuntime(rt *sessionRuntime, record SessionRecord) {
	rt.inFlight = record.InFlight
	rt.pendingResume = record.PendingResume
	rt.resumeTriggerSeq = record.ResumeTriggerSeq
	rt.resumeActorIDs = cloneActorIDs(record.ResumeActorIDs)
	rt.resumeReason = strings.TrimSpace(record.ResumeReason)
	rt.resumeRecordedAt = cloneTimePtr(record.ResumeRecordedAt)
	rt.resumeEnqueuedAt = cloneTimePtr(record.ResumeEnqueuedAt)
}

func (m *Manager) prepareRecoveredSession(ctx context.Context, rt *sessionRuntime, events []Event) error {
	if rt.inFlight {
		now := m.now().UTC()
		rt.inFlight = false
		rt.pendingResume = true
		if len(rt.resumeActorIDs) == 0 {
			rt.resumeActorIDs = sortedAgentParticipants(rt.session)
		}
		if rt.resumeReason == "" {
			rt.resumeReason = interruptedResumeReason
		}
		if rt.resumeRecordedAt == nil {
			rt.resumeRecordedAt = &now
		}
		rt.resumeEnqueuedAt = nil
		if err := m.persistSessionRecord(ctx, rt, now); err != nil {
			return err
		}
	}
	if !rt.pendingResume {
		return nil
	}
	return m.resumeRecoveredSession(ctx, rt, events)
}

func (m *Manager) resumeRecoveredSession(ctx context.Context, rt *sessionRuntime, events []Event) error {
	actorIDs := normalizeResumeActorIDs(rt.resumeActorIDs)
	if len(actorIDs) == 0 {
		actorIDs = sortedAgentParticipants(rt.session)
	}
	if len(actorIDs) == 0 {
		return nil
	}
	now := m.now().UTC()
	var latestEnqueued *time.Time
	existingByActor := make(map[ActorID]Event, len(actorIDs))
	for _, actorID := range actorIDs {
		existing, found := findExistingRecoveryEvent(events, actorID, rt.resumeTriggerSeq, rt.resumeReason)
		if found {
			existingByActor[actorID] = existing
			if latestEnqueued == nil || existing.Timestamp.After(*latestEnqueued) {
				ts := existing.Timestamp.UTC()
				latestEnqueued = &ts
			}
			continue
		}
		latestEnqueued = &now
	}

	rt.resumeActorIDs = cloneActorIDs(actorIDs)
	rt.resumeEnqueuedAt = cloneTimePtr(latestEnqueued)
	if err := m.persistSessionRecord(ctx, rt, now); err != nil {
		return err
	}

	for _, actorID := range actorIDs {
		if existing, found := existingByActor[actorID]; found {
			if err := m.enqueue(ctx, rt, runtimeRequest{kind: runtimeRequestReplay, event: existing}); err != nil {
				return err
			}
			continue
		}
		prompt := buildRecoveryPrompt(rt.resumeTriggerSeq, rt.resumeReason)
		if err := m.enqueue(ctx, rt, runtimeRequest{
			kind: runtimeRequestSend,
			event: Event{
				SessionID: rt.sessionID,
				From:      SystemActorID,
				Type:      EventMessage,
				Payload: Message{
					Role:          RoleAgent,
					Content:       prompt,
					TargetActorID: actorID,
				},
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func findExistingRecoveryEvent(events []Event, actorID ActorID, triggerSeq uint64, reason string) (Event, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		if isRecoveryPromptEvent(events[i], actorID, triggerSeq, reason) {
			return events[i], true
		}
	}
	return Event{}, false
}
