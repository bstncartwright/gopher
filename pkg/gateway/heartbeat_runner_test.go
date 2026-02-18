package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type recordingSessionManager struct {
	mu     sync.Mutex
	events []sessionrt.Event
}

func (m *recordingSessionManager) CreateSession(context.Context, sessionrt.CreateSessionOptions) (*sessionrt.Session, error) {
	return nil, nil
}

func (m *recordingSessionManager) GetSession(context.Context, sessionrt.SessionID) (*sessionrt.Session, error) {
	return nil, nil
}

func (m *recordingSessionManager) SendEvent(_ context.Context, e sessionrt.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *recordingSessionManager) Subscribe(context.Context, sessionrt.SessionID) (<-chan sessionrt.Event, error) {
	ch := make(chan sessionrt.Event)
	close(ch)
	return ch, nil
}

func (m *recordingSessionManager) CancelSession(context.Context, sessionrt.SessionID) error {
	return nil
}

func (m *recordingSessionManager) sentEvents() []sessionrt.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]sessionrt.Event, len(m.events))
	copy(out, m.events)
	return out
}

func TestHeartbeatRunnerProcessDueDispatchesPoll(t *testing.T) {
	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	manager := &recordingSessionManager{}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		routes:        map[string]conversationRoute{},
		processing:    map[string]int{},
		heartbeats:    map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!dm:one", "sess-1")
	pipeline.setConversationRoute("!dm:one", "agent:a", "@agent:a")

	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  manager,
		Pipeline: pipeline,
		Schedules: []HeartbeatSchedule{{
			AgentID:     "agent:a",
			Every:       time.Minute,
			Prompt:      "hb",
			AckMaxChars: 123,
		}},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}

	runner.nextRun["agent:a"] = now
	runner.processDue(context.Background())

	events := manager.sentEvents()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	if events[0].SessionID != "sess-1" {
		t.Fatalf("session id = %q, want sess-1", events[0].SessionID)
	}
	payload, ok := events[0].Payload.(sessionrt.Message)
	if !ok {
		t.Fatalf("payload type = %T, want session.Message", events[0].Payload)
	}
	if payload.Role != sessionrt.RoleUser || payload.Content != "hb" {
		t.Fatalf("payload = %#v, want user hb", payload)
	}

	state, ok := pipeline.heartbeats["!dm:one"]
	if !ok {
		t.Fatalf("expected pending heartbeat state")
	}
	if state.pending != 1 || state.ackMaxChars != 123 {
		t.Fatalf("heartbeat state = %#v, want pending=1 ack_max=123", state)
	}
}

func TestHeartbeatRunnerProcessDueSkipsBusyConversations(t *testing.T) {
	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	manager := &recordingSessionManager{}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		routes:        map[string]conversationRoute{},
		processing:    map[string]int{"!dm:one": 1},
		heartbeats:    map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!dm:one", "sess-1")
	pipeline.setConversationRoute("!dm:one", "agent:a", "@agent:a")

	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  manager,
		Pipeline: pipeline,
		Schedules: []HeartbeatSchedule{{
			AgentID: "agent:a",
			Every:   time.Minute,
			Prompt:  "hb",
		}},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}

	runner.nextRun["agent:a"] = now
	runner.processDue(context.Background())

	if len(manager.sentEvents()) != 0 {
		t.Fatalf("expected no heartbeat dispatch while conversation is processing")
	}
}
