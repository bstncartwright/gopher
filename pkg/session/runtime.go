package session

import (
	"context"
	"fmt"
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
	defer close(rt.done)

	for {
		select {
		case <-rt.ctx.Done():
			return
		case req := <-rt.queue:
			err, terminal := m.handleRuntimeRequest(rt, req)
			req.resp <- err
			if terminal {
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
		return fmt.Errorf("%w: unknown runtime request kind", ErrInvalidEvent), false
	}
}

func (m *Manager) handleSendRequest(rt *sessionRuntime, req runtimeRequest) (error, bool) {
	e, err := m.canonicalizeEvent(rt, req.event)
	if err != nil {
		return err, false
	}

	if err := m.appendPersistedEvent(req.ctx, rt, e); err != nil {
		return m.failSession(rt, fmt.Errorf("append event: %w", err)), true
	}

	if !m.shouldTriggerAgent(e) || m.executor == nil {
		return nil, false
	}

	sessionSnapshot := cloneSession(rt.session)
	actorID, ok := m.selectFn(sessionSnapshot, e)
	if !ok {
		return nil, false
	}

	history, err := m.store.List(req.ctx, rt.sessionID)
	if err != nil {
		return m.failSession(rt, fmt.Errorf("list history: %w", err)), true
	}

	if err := m.setInFlight(req.ctx, rt, true); err != nil {
		return m.failSession(rt, fmt.Errorf("set in-flight: %w", err)), true
	}
	defer func() {
		_ = m.setInFlight(context.Background(), rt, false)
	}()

	output, err := m.executor.Step(rt.ctx, AgentInput{
		SessionID: rt.sessionID,
		ActorID:   actorID,
		History:   history,
	})
	if err != nil {
		return m.failSession(rt, fmt.Errorf("agent step: %w", err)), true
	}

	for _, produced := range output.Events {
		if produced.SessionID != "" && produced.SessionID != rt.sessionID {
			return m.failSession(rt, fmt.Errorf("%w: executor returned session %q for runtime %q", ErrInvalidEvent, produced.SessionID, rt.sessionID)), true
		}
		if strings.TrimSpace(string(produced.From)) == "" {
			produced.From = actorID
		}
		produced.SessionID = rt.sessionID

		canonical, err := m.canonicalizeEvent(rt, produced)
		if err != nil {
			return m.failSession(rt, fmt.Errorf("canonicalize agent event: %w", err)), true
		}
		if err := m.appendPersistedEvent(req.ctx, rt, canonical); err != nil {
			return m.failSession(rt, fmt.Errorf("append agent event: %w", err)), true
		}
	}

	return nil, false
}

func (m *Manager) handleCancelRequest(rt *sessionRuntime, req runtimeRequest) error {
	currentStatus := rt.session.Status
	if currentStatus != SessionActive {
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
		return err
	}
	if err := m.appendPersistedEvent(req.ctx, rt, cancelEvent); err != nil {
		return err
	}

	rt.inFlight = false
	_ = m.persistSessionRecord(context.Background(), rt, cancelEvent.Timestamp)
	m.setSessionStatus(rt, SessionPaused)
	rt.cancel()
	return nil
}

func (m *Manager) failSession(rt *sessionRuntime, cause error) error {
	msg := cause.Error()

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
