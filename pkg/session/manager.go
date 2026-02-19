package session

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type SessionManager interface {
	CreateSession(ctx context.Context, opts CreateSessionOptions) (*Session, error)
	GetSession(ctx context.Context, id SessionID) (*Session, error)
	SendEvent(ctx context.Context, e Event) error
	Subscribe(ctx context.Context, sessionID SessionID) (<-chan Event, error)
	CancelSession(ctx context.Context, id SessionID) error
}

type AgentSelector func(session *Session, trigger Event) ([]ActorID, bool)

type ManagerOptions struct {
	Store          EventStore
	Executor       AgentExecutor
	AgentSelector  AgentSelector
	Publisher      EventPublisher
	Now            func() time.Time
	RecoverOnStart bool
}

type Manager struct {
	store     EventStore
	registry  SessionRegistryStore
	executor  AgentExecutor
	selectFn  AgentSelector
	publisher EventPublisher
	now       func() time.Time

	counter uint64

	mu       sync.RWMutex
	sessions map[SessionID]*sessionRuntime
}

func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Store == nil {
		slog.Error("session_manager: event store is required")
		return nil, fmt.Errorf("%w: event store is required", ErrInvalidSession)
	}

	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	selectFn := opts.AgentSelector
	if selectFn == nil {
		selectFn = DefaultAgentSelector
	}

	slog.Debug("session_manager: creating new manager", "recover_on_start", opts.RecoverOnStart)
	manager := &Manager{
		store:     opts.Store,
		executor:  opts.Executor,
		selectFn:  selectFn,
		publisher: opts.Publisher,
		now:       nowFn,
		sessions:  make(map[SessionID]*sessionRuntime),
		registry:  nil,
	}
	if registry, ok := opts.Store.(SessionRegistryStore); ok {
		manager.registry = registry
	}
	if opts.RecoverOnStart {
		slog.Info("session_manager: starting recovery")
		if err := manager.Recover(context.Background()); err != nil {
			slog.Error("session_manager: recovery failed", "error", err)
			return nil, err
		}
		slog.Info("session_manager: recovery complete")
	}
	slog.Info("session_manager: manager created")
	return manager, nil
}

func DefaultAgentSelector(session *Session, _ Event) ([]ActorID, bool) {
	if session == nil {
		return nil, false
	}

	agentIDs := make([]string, 0, len(session.Participants))
	for actorID, participant := range session.Participants {
		if participant.Type == ActorAgent {
			agentIDs = append(agentIDs, string(actorID))
		}
	}
	if len(agentIDs) == 0 {
		return nil, false
	}
	sort.Strings(agentIDs)
	return []ActorID{ActorID(agentIDs[0])}, true
}

func (m *Manager) CreateSession(ctx context.Context, opts CreateSessionOptions) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	participants, err := normalizeParticipants(opts.Participants)
	if err != nil {
		slog.Error("session_manager: failed to normalize participants", "error", err)
		return nil, err
	}

	id := m.newSessionID()
	session := &Session{
		ID:           id,
		Participants: participants,
		CreatedAt:    m.now().UTC(),
		Status:       SessionActive,
	}
	rt := newSessionRuntime(session)

	slog.Info("session_manager: creating session",
		"session_id", id,
		"participants_count", len(participants),
	)

	m.mu.Lock()
	m.sessions[id] = rt
	m.mu.Unlock()

	go m.runSession(rt)

	createdEvent := Event{
		SessionID: id,
		From:      SystemActorID,
		Type:      EventControl,
		Payload: ControlPayload{
			Action: ControlActionSessionCreated,
			Metadata: map[string]any{
				"participants": participantsList(session.Participants),
			},
		},
	}
	if err := m.enqueue(ctx, rt, runtimeRequest{kind: runtimeRequestSend, event: createdEvent}); err != nil {
		slog.Error("session_manager: failed to enqueue created event",
			"session_id", id,
			"error", err,
		)
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		rt.cancel()
		<-rt.done
		return nil, err
	}

	slog.Info("session_manager: session created", "session_id", id)
	return cloneSession(session), nil
}

func (m *Manager) GetSession(ctx context.Context, id SessionID) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	slog.Debug("session_manager: getting session", "session_id", id)
	m.mu.RLock()
	rt, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		slog.Debug("session_manager: session not found", "session_id", id)
		return nil, ErrSessionNotFound
	}
	out := cloneSession(rt.session)
	m.mu.RUnlock()
	return out, nil
}

func (m *Manager) SendEvent(ctx context.Context, e Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	sessionID := e.SessionID
	if strings.TrimSpace(string(sessionID)) == "" {
		slog.Error("session_manager: event missing session ID")
		return fmt.Errorf("%w: session ID is required", ErrInvalidEvent)
	}

	slog.Debug("session_manager: sending event",
		"session_id", sessionID,
		"event_type", e.Type,
		"from", e.From,
	)

	m.mu.RLock()
	rt, ok := m.sessions[sessionID]
	if !ok {
		m.mu.RUnlock()
		slog.Warn("session_manager: session not found for event", "session_id", sessionID)
		return ErrSessionNotFound
	}
	status := rt.session.Status
	m.mu.RUnlock()
	if status != SessionActive {
		slog.Warn("session_manager: session not active",
			"session_id", sessionID,
			"status", status,
		)
		return ErrSessionNotActive
	}

	return m.enqueue(ctx, rt, runtimeRequest{kind: runtimeRequestSend, event: e})
}

func (m *Manager) Subscribe(ctx context.Context, sessionID SessionID) (<-chan Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	slog.Debug("session_manager: subscribing to session", "session_id", sessionID)
	m.mu.RLock()
	_, ok := m.sessions[sessionID]
	m.mu.RUnlock()
	if !ok {
		slog.Warn("session_manager: session not found for subscribe", "session_id", sessionID)
		return nil, ErrSessionNotFound
	}

	return m.store.Stream(ctx, sessionID)
}

func (m *Manager) CancelSession(ctx context.Context, id SessionID) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	slog.Info("session_manager: cancelling session", "session_id", id)
	m.mu.RLock()
	rt, ok := m.sessions[id]
	if !ok {
		m.mu.RUnlock()
		slog.Warn("session_manager: session not found for cancel", "session_id", id)
		return ErrSessionNotFound
	}
	status := rt.session.Status
	m.mu.RUnlock()
	if status != SessionActive {
		slog.Warn("session_manager: session not active for cancel",
			"session_id", id,
			"status", status,
		)
		return ErrSessionNotActive
	}

	return m.enqueue(ctx, rt, runtimeRequest{
		kind:   runtimeRequestCancel,
		reason: "external_cancel",
	})
}

func (m *Manager) newSessionID() SessionID {
	seq := atomic.AddUint64(&m.counter, 1)
	ts := m.now().UTC().UnixNano()
	return SessionID(fmt.Sprintf("sess-%d-%d", ts, seq))
}

func (m *Manager) enqueue(ctx context.Context, rt *sessionRuntime, req runtimeRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	req.ctx = ctx
	req.resp = make(chan error, 1)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-rt.done:
		return ErrSessionNotActive
	case rt.queue <- req:
	}

	select {
	case err := <-req.resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-rt.done:
		return ErrSessionNotActive
	}
}

func normalizeParticipants(input []Participant) (map[ActorID]Participant, error) {
	if len(input) == 0 {
		return nil, fmt.Errorf("%w: at least one participant is required", ErrInvalidSession)
	}

	out := make(map[ActorID]Participant, len(input))
	for _, participant := range input {
		id := ActorID(strings.TrimSpace(string(participant.ID)))
		if id == "" {
			return nil, fmt.Errorf("%w: participant ID is required", ErrInvalidSession)
		}
		if !validActorType(participant.Type) {
			return nil, fmt.Errorf("%w: participant %q has invalid actor type", ErrInvalidSession, id)
		}
		if _, exists := out[id]; exists {
			return nil, fmt.Errorf("%w: duplicate participant %q", ErrInvalidSession, id)
		}
		copied := Participant{
			ID:       id,
			Type:     participant.Type,
			Metadata: cloneStringMap(participant.Metadata),
		}
		out[id] = copied
	}

	return out, nil
}

func participantsList(participants map[ActorID]Participant) []Participant {
	keys := make([]string, 0, len(participants))
	for id := range participants {
		keys = append(keys, string(id))
	}
	sort.Strings(keys)

	out := make([]Participant, 0, len(keys))
	for _, id := range keys {
		out = append(out, cloneParticipant(participants[ActorID(id)]))
	}
	return out
}

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}

	participants := make(map[ActorID]Participant, len(session.Participants))
	for id, participant := range session.Participants {
		participants[id] = cloneParticipant(participant)
	}

	return &Session{
		ID:           session.ID,
		Participants: participants,
		CreatedAt:    session.CreatedAt,
		Status:       session.Status,
	}
}

func cloneParticipant(participant Participant) Participant {
	return Participant{
		ID:       participant.ID,
		Type:     participant.Type,
		Metadata: cloneStringMap(participant.Metadata),
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
