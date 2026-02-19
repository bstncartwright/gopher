package session

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type runtimeRequestKind int

const (
	runtimeRequestSend runtimeRequestKind = iota
	runtimeRequestCancel
)

type runtimeRequest struct {
	ctx    context.Context
	kind   runtimeRequestKind
	event  Event
	reason string
	resp   chan error
}

type sessionRuntime struct {
	sessionID SessionID
	session   *Session
	inFlight  bool

	ctx    context.Context
	cancel context.CancelFunc
	queue  chan runtimeRequest
	done   chan struct{}

	nextSeq uint64
}

func newSessionRuntime(session *Session) *sessionRuntime {
	ctx, cancel := context.WithCancel(context.Background())
	return &sessionRuntime{
		sessionID: session.ID,
		session:   session,
		ctx:       ctx,
		cancel:    cancel,
		queue:     make(chan runtimeRequest, 32),
		done:      make(chan struct{}),
	}
}

func (m *Manager) runSession(rt *sessionRuntime) {
	slog.Debug("session_runtime: starting session runtime", "session_id", rt.sessionID)
	defer close(rt.done)

	for {
		select {
		case <-rt.ctx.Done():
			slog.Debug("session_runtime: context done, stopping", "session_id", rt.sessionID)
			return
		case req := <-rt.queue:
			slog.Debug("session_runtime: processing request",
				"session_id", rt.sessionID,
				"request_kind", req.kind,
			)
			err, terminal := m.handleRuntimeRequest(rt, req)
			req.resp <- err
			if terminal {
				slog.Debug("session_runtime: terminal request, stopping", "session_id", rt.sessionID)
				return
			}
		}
	}
}

func (m *Manager) handleRuntimeRequest(rt *sessionRuntime, req runtimeRequest) (error, bool) {
	switch req.kind {
	case runtimeRequestSend:
		return m.handleSendRequest(rt, req)
	case runtimeRequestCancel:
		return m.handleCancelRequest(rt, req), true
	default:
		slog.Error("session_runtime: unknown request kind",
			"session_id", rt.sessionID,
			"request_kind", req.kind,
		)
		return fmt.Errorf("%w: unknown runtime request kind", ErrInvalidEvent), false
	}
}

func (m *Manager) handleSendRequest(rt *sessionRuntime, req runtimeRequest) (error, bool) {
	e, err := m.canonicalizeEvent(rt, req.event)
	if err != nil {
		slog.Error("session_runtime: failed to canonicalize event",
			"session_id", rt.sessionID,
			"event_type", req.event.Type,
			"error", err,
		)
		return err, false
	}

	slog.Debug("session_runtime: appending persisted event",
		"session_id", rt.sessionID,
		"event_type", e.Type,
		"event_seq", e.Seq,
	)
	if err := m.appendPersistedEvent(req.ctx, rt, e); err != nil {
		slog.Error("session_runtime: failed to append event",
			"session_id", rt.sessionID,
			"error", err,
		)
		return m.failSession(rt, fmt.Errorf("append event: %w", err)), true
	}

	if !m.shouldTriggerAgent(e) || m.executor == nil {
		return nil, false
	}

	sessionSnapshot := cloneSession(rt.session)
	selectedActorIDs, ok := m.selectFn(sessionSnapshot, e)
	if !ok {
		slog.Debug("session_runtime: agent selector returned no actors",
			"session_id", rt.sessionID,
			"event_type", e.Type,
		)
		return nil, false
	}
	actorIDs := resolveSelectedAgentActors(sessionSnapshot, selectedActorIDs)
	if len(actorIDs) == 0 {
		slog.Debug("session_runtime: no resolved agent actors",
			"session_id", rt.sessionID,
		)
		return nil, false
	}

	slog.Info("session_runtime: triggering agent execution",
		"session_id", rt.sessionID,
		"actor_ids", actorIDs,
		"event_type", e.Type,
	)

	if err := m.setInFlight(req.ctx, rt, true); err != nil {
		slog.Error("session_runtime: failed to set in-flight",
			"session_id", rt.sessionID,
			"error", err,
		)
		return m.failSession(rt, fmt.Errorf("set in-flight: %w", err)), true
	}
	defer func() {
		_ = m.setInFlight(context.Background(), rt, false)
	}()

	for _, actorID := range actorIDs {
		slog.Debug("session_runtime: loading history for actor",
			"session_id", rt.sessionID,
			"actor_id", actorID,
		)
		history, err := m.store.List(req.ctx, rt.sessionID)
		if err != nil {
			slog.Error("session_runtime: failed to list history",
				"session_id", rt.sessionID,
				"actor_id", actorID,
				"error", err,
			)
			return m.failSession(rt, fmt.Errorf("list history: %w", err)), true
		}

		slog.Debug("session_runtime: executing agent step",
			"session_id", rt.sessionID,
			"actor_id", actorID,
			"history_count", len(history),
		)
		output, err := m.executor.Step(rt.ctx, AgentInput{
			SessionID: rt.sessionID,
			ActorID:   actorID,
			History:   history,
		})
		if err != nil {
			slog.Error("session_runtime: agent step failed",
				"session_id", rt.sessionID,
				"actor_id", actorID,
				"error", err,
			)
			return m.recordAgentStepError(rt, actorID, err), false
		}

		slog.Info("session_runtime: agent step complete",
			"session_id", rt.sessionID,
			"actor_id", actorID,
			"output_events_count", len(output.Events),
		)

		for _, produced := range output.Events {
			if produced.SessionID != "" && produced.SessionID != rt.sessionID {
				slog.Error("session_runtime: executor returned wrong session",
					"session_id", rt.sessionID,
					"produced_session_id", produced.SessionID,
				)
				return m.failSession(rt, fmt.Errorf("%w: executor returned session %q for runtime %q", ErrInvalidEvent, produced.SessionID, rt.sessionID)), true
			}
			if strings.TrimSpace(string(produced.From)) == "" {
				produced.From = actorID
			}
			produced.SessionID = rt.sessionID

			canonical, err := m.canonicalizeEvent(rt, produced)
			if err != nil {
				slog.Error("session_runtime: failed to canonicalize agent event",
					"session_id", rt.sessionID,
					"error", err,
				)
				return m.failSession(rt, fmt.Errorf("canonicalize agent event: %w", err)), true
			}
			if err := m.appendPersistedEvent(req.ctx, rt, canonical); err != nil {
				slog.Error("session_runtime: failed to append agent event",
					"session_id", rt.sessionID,
					"error", err,
				)
				return m.failSession(rt, fmt.Errorf("append agent event: %w", err)), true
			}
		}
	}

	return nil, false
}

func (m *Manager) handleCancelRequest(rt *sessionRuntime, req runtimeRequest) error {
	slog.Info("session_runtime: handling cancel request",
		"session_id", rt.sessionID,
		"reason", req.reason,
	)
	currentStatus := rt.session.Status
	if currentStatus != SessionActive {
		slog.Warn("session_runtime: session not active for cancel",
			"session_id", rt.sessionID,
			"status", currentStatus,
		)
		return ErrSessionNotActive
	}

	cancelEvent, err := m.canonicalizeEvent(rt, Event{
		SessionID: rt.sessionID,
		From:      SystemActorID,
		Type:      EventControl,
		Payload: ControlPayload{
			Action: ControlActionSessionCancelled,
			Reason: req.reason,
		},
	})
	if err != nil {
		slog.Error("session_runtime: failed to canonicalize cancel event",
			"session_id", rt.sessionID,
			"error", err,
		)
		return err
	}
	if err := m.appendPersistedEvent(req.ctx, rt, cancelEvent); err != nil {
		slog.Error("session_runtime: failed to append cancel event",
			"session_id", rt.sessionID,
			"error", err,
		)
		return err
	}

	rt.inFlight = false
	_ = m.persistSessionRecord(context.Background(), rt, cancelEvent.Timestamp)
	m.setSessionStatus(rt, SessionPaused)
	rt.cancel()
	slog.Info("session_runtime: session cancelled", "session_id", rt.sessionID)
	return nil
}

func (m *Manager) failSession(rt *sessionRuntime, cause error) error {
	msg := cause.Error()
	slog.Error("session_runtime: failing session",
		"session_id", rt.sessionID,
		"cause", msg,
	)

	errEvent, err := m.canonicalizeEvent(rt, Event{
		SessionID: rt.sessionID,
		From:      SystemActorID,
		Type:      EventError,
		Payload: ErrorPayload{
			Message: msg,
		},
	})
	if err == nil {
		_ = m.appendPersistedEvent(context.Background(), rt, errEvent)
	}

	failedEvent, err := m.canonicalizeEvent(rt, Event{
		SessionID: rt.sessionID,
		From:      SystemActorID,
		Type:      EventControl,
		Payload: ControlPayload{
			Action: ControlActionSessionFailed,
			Reason: msg,
		},
	})
	if err == nil {
		_ = m.appendPersistedEvent(context.Background(), rt, failedEvent)
	}

	rt.inFlight = false
	_ = m.persistSessionRecord(context.Background(), rt, m.now().UTC())
	m.setSessionStatus(rt, SessionFailed)
	rt.cancel()
	return cause
}

func (m *Manager) recordAgentStepError(rt *sessionRuntime, actorID ActorID, cause error) error {
	errWithContext := fmt.Errorf("agent step: %w", cause)
	slog.Error("session_runtime: recording agent step error",
		"session_id", rt.sessionID,
		"actor_id", actorID,
		"error", errWithContext,
	)
	errEvent, err := m.canonicalizeEvent(rt, Event{
		SessionID: rt.sessionID,
		From:      actorID,
		Type:      EventError,
		Payload: ErrorPayload{
			Message: errWithContext.Error(),
		},
	})
	if err == nil {
		_ = m.appendPersistedEvent(context.Background(), rt, errEvent)
	}
	return errWithContext
}

func (m *Manager) shouldTriggerAgent(event Event) bool {
	if event.Type != EventMessage {
		return false
	}
	msg, ok := messageFromPayload(event.Payload)
	if !ok {
		return false
	}
	return msg.Role == RoleUser
}

func resolveSelectedAgentActors(session *Session, selected []ActorID) []ActorID {
	if session == nil || len(selected) == 0 {
		return nil
	}
	out := make([]ActorID, 0, len(selected))
	seen := make(map[ActorID]struct{}, len(selected))
	for _, actorID := range selected {
		actorID = ActorID(strings.TrimSpace(string(actorID)))
		if strings.TrimSpace(string(actorID)) == "" {
			continue
		}
		if _, exists := seen[actorID]; exists {
			continue
		}
		participant, exists := session.Participants[actorID]
		if !exists || participant.Type != ActorAgent {
			continue
		}
		seen[actorID] = struct{}{}
		out = append(out, actorID)
	}
	return out
}

func (m *Manager) canonicalizeEvent(rt *sessionRuntime, event Event) (Event, error) {
	if strings.TrimSpace(string(event.SessionID)) == "" {
		event.SessionID = rt.sessionID
	}
	if event.SessionID != rt.sessionID {
		return Event{}, fmt.Errorf("%w: event session %q does not match runtime session %q", ErrInvalidEvent, event.SessionID, rt.sessionID)
	}
	if strings.TrimSpace(string(event.Type)) == "" {
		return Event{}, fmt.Errorf("%w: event type is required", ErrInvalidEvent)
	}

	if strings.TrimSpace(string(event.From)) == "" {
		switch event.Type {
		case EventControl, EventError:
			event.From = SystemActorID
		default:
			return Event{}, fmt.Errorf("%w: event source actor is required", ErrInvalidEvent)
		}
	}

	normalizedPayload, err := normalizeEventPayload(event.Type, event.Payload)
	if err != nil {
		return Event{}, err
	}

	rt.nextSeq++
	event.Seq = rt.nextSeq
	event.Timestamp = m.now().UTC()
	event.ID = EventID(fmt.Sprintf("%s-%06d", rt.sessionID, event.Seq))
	event.Payload = normalizedPayload
	return event, nil
}

func (m *Manager) setSessionStatus(rt *sessionRuntime, status SessionStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.sessions[rt.sessionID]
	if !ok {
		return
	}
	stored.session.Status = status
}
