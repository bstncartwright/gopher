package ingest

import (
	"context"
	"strings"
	"sync"

	"github.com/bstncartwright/gopher/pkg/memory"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type SessionPublisherOptions struct {
	Manager      memory.MemoryManager
	Extractor    *Extractor
	FlushEvery   int
	MaxBuffer    int
	OnStoreError func(ctx context.Context, event sessionrt.Event, record memory.MemoryRecord, err error)
}

type SessionPublisher struct {
	manager    memory.MemoryManager
	extractor  *Extractor
	flushEvery int
	maxBuffer  int
	onStoreErr func(ctx context.Context, event sessionrt.Event, record memory.MemoryRecord, err error)

	mu       sync.Mutex
	sessions map[sessionrt.SessionID]*sessionState
}

type sessionState struct {
	events  []sessionrt.Event
	agentID string
}

var _ sessionrt.EventPublisher = (*SessionPublisher)(nil)
var _ SessionFlusher = (*SessionPublisher)(nil)

type SessionFlusher interface {
	FlushSession(ctx context.Context, sessionID sessionrt.SessionID) error
}

func NewSessionPublisher(opts SessionPublisherOptions) *SessionPublisher {
	flushEvery := opts.FlushEvery
	if flushEvery <= 0 {
		flushEvery = 32
	}
	maxBuffer := opts.MaxBuffer
	if maxBuffer <= 0 {
		maxBuffer = 512
	}
	extractor := opts.Extractor
	if extractor == nil {
		extractor = NewExtractor(ExtractorOptions{})
	}
	return &SessionPublisher{
		manager:    opts.Manager,
		extractor:  extractor,
		flushEvery: flushEvery,
		maxBuffer:  maxBuffer,
		onStoreErr: opts.OnStoreError,
		sessions:   make(map[sessionrt.SessionID]*sessionState),
	}
}

func (p *SessionPublisher) PublishEvent(ctx context.Context, event sessionrt.Event) error {
	if p == nil || p.manager == nil {
		return nil
	}
	if event.SessionID == "" {
		return nil
	}

	p.mu.Lock()
	state := p.sessions[event.SessionID]
	if state == nil {
		state = &sessionState{events: make([]sessionrt.Event, 0, p.flushEvery*2)}
		p.sessions[event.SessionID] = state
	}
	state.events = append(state.events, cloneEvent(event))
	if len(state.events) > p.maxBuffer {
		start := len(state.events) - p.maxBuffer
		trimmed := make([]sessionrt.Event, p.maxBuffer)
		copy(trimmed, state.events[start:])
		state.events = trimmed
	}

	if state.agentID == "" {
		if msg, ok := payloadToMessage(event.Payload); ok && msg.Role == sessionrt.RoleAgent {
			state.agentID = string(event.From)
		}
	}

	eventsSnapshot := append([]sessionrt.Event(nil), state.events...)
	agentID := strings.TrimSpace(state.agentID)
	shouldFlush := len(state.events)%p.flushEvery == 0 || isImportantMemoryTrigger(event)
	isTerminal := isTerminalControlEvent(event)
	if isTerminal {
		delete(p.sessions, event.SessionID)
	}
	p.mu.Unlock()

	if !shouldFlush {
		return nil
	}

	_ = p.flushSnapshot(ctx, event, string(event.SessionID), agentID, eventsSnapshot)
	return nil
}

func (p *SessionPublisher) FlushSession(ctx context.Context, sessionID sessionrt.SessionID) error {
	if p == nil || p.manager == nil {
		return nil
	}
	if sessionID == "" {
		return nil
	}
	p.mu.Lock()
	state := p.sessions[sessionID]
	if state == nil || len(state.events) == 0 {
		p.mu.Unlock()
		return nil
	}
	eventsSnapshot := append([]sessionrt.Event(nil), state.events...)
	agentID := strings.TrimSpace(state.agentID)
	p.mu.Unlock()
	return p.flushSnapshot(ctx, sessionrt.Event{SessionID: sessionID}, string(sessionID), agentID, eventsSnapshot)
}

func (p *SessionPublisher) flushSnapshot(ctx context.Context, trigger sessionrt.Event, sessionID string, agentID string, eventsSnapshot []sessionrt.Event) error {
	storeCtx := ctx
	if storeCtx == nil || storeCtx.Err() != nil {
		storeCtx = context.Background()
	}
	records := p.extractor.ExtractSession(sessionID, agentID, eventsSnapshot)
	var firstErr error
	for _, record := range records {
		if err := p.manager.Store(storeCtx, record); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if p.onStoreErr != nil {
				p.onStoreErr(storeCtx, trigger, record, err)
			}
		}
	}
	return firstErr
}

func isImportantMemoryTrigger(event sessionrt.Event) bool {
	if event.Type == sessionrt.EventError {
		return true
	}
	if event.Type == sessionrt.EventControl {
		control, ok := payloadToControl(event.Payload)
		if !ok {
			return false
		}
		switch control.Action {
		case sessionrt.ControlActionSessionCompleted, sessionrt.ControlActionSessionCancelled, sessionrt.ControlActionSessionFailed:
			return true
		}
	}
	return false
}

func isTerminalControlEvent(event sessionrt.Event) bool {
	if event.Type != sessionrt.EventControl {
		return false
	}
	control, ok := payloadToControl(event.Payload)
	if !ok {
		return false
	}
	switch control.Action {
	case sessionrt.ControlActionSessionCompleted, sessionrt.ControlActionSessionCancelled, sessionrt.ControlActionSessionFailed:
		return true
	default:
		return false
	}
}

func payloadToControl(payload any) (sessionrt.ControlPayload, bool) {
	switch value := payload.(type) {
	case sessionrt.ControlPayload:
		return value, true
	case map[string]any:
		action, _ := value["action"].(string)
		reason, _ := value["reason"].(string)
		if strings.TrimSpace(action) == "" {
			return sessionrt.ControlPayload{}, false
		}
		return sessionrt.ControlPayload{Action: action, Reason: reason}, true
	default:
		return sessionrt.ControlPayload{}, false
	}
}

func cloneEvent(in sessionrt.Event) sessionrt.Event {
	out := in
	if payloadMap, ok := in.Payload.(map[string]any); ok {
		out.Payload = cloneAnyMap(payloadMap)
	}
	return out
}
