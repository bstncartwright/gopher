package ingest

import (
	"context"
	"strings"
	"sync"

	"github.com/bstncartwright/gopher/pkg/memory"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type SessionPublisherOptions struct {
	Manager    memory.MemoryManager
	Extractor  *Extractor
	FlushEvery int
	MaxBuffer  int
}

type SessionPublisher struct {
	manager    memory.MemoryManager
	extractor  *Extractor
	flushEvery int
	maxBuffer  int

	mu       sync.Mutex
	sessions map[sessionrt.SessionID]*sessionState
}

type sessionState struct {
	events  []sessionrt.Event
	agentID string
}

var _ sessionrt.EventPublisher = (*SessionPublisher)(nil)

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

	storeCtx := ctx
	if storeCtx == nil || storeCtx.Err() != nil {
		storeCtx = context.Background()
	}
	records := p.extractor.ExtractSession(string(event.SessionID), agentID, eventsSnapshot)
	for _, record := range records {
		_ = p.manager.Store(storeCtx, record)
	}
	return nil
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
