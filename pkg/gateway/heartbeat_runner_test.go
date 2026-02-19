package gateway

import (
	"context"
	"sync"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type recordingSessionManager struct {
	mu        sync.Mutex
	events    []sessionrt.Event
	sessions  map[sessionrt.SessionID]*sessionrt.Session
	sendError error
}

func (m *recordingSessionManager) CreateSession(context.Context, sessionrt.CreateSessionOptions) (*sessionrt.Session, error) {
	return nil, nil
}

func (m *recordingSessionManager) GetSession(_ context.Context, id sessionrt.SessionID) (*sessionrt.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[id]
	if !ok {
		return nil, nil
	}
	return cloneSessionRecord(session), nil
}

func (m *recordingSessionManager) SendEvent(_ context.Context, e sessionrt.Event) error {
	if m.sendError != nil {
		return m.sendError
	}
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

func cloneSessionRecord(in *sessionrt.Session) *sessionrt.Session {
	if in == nil {
		return nil
	}
	participants := make(map[sessionrt.ActorID]sessionrt.Participant, len(in.Participants))
	for actorID, participant := range in.Participants {
		participants[actorID] = participant
	}
	return &sessionrt.Session{
		ID:           in.ID,
		Participants: participants,
		CreatedAt:    in.CreatedAt,
		Status:       in.Status,
	}
}

func TestHeartbeatRunnerProcessDueDispatchesPoll(t *testing.T) {
	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	manager := &recordingSessionManager{
		sessions: map[sessionrt.SessionID]*sessionrt.Session{
			"sess-1": {
				ID: "sess-1",
				Participants: map[sessionrt.ActorID]sessionrt.Participant{
					"agent:a": {ID: "agent:a", Type: sessionrt.ActorAgent},
				},
				Status: sessionrt.SessionActive,
			},
		},
	}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		recipByAgent:  map[sessionrt.ActorID]string{},
		routes:        map[string]conversationRoute{},
		processing:    map[string]int{},
		heartbeats:    map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!dm:one", "sess-1")
	pipeline.setConversationRoute("!dm:one", "agent:a", "@agent:a", ConversationModeDM)

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
	if payload.TargetActorID != "agent:a" {
		t.Fatalf("target actor id = %q, want agent:a", payload.TargetActorID)
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
	manager := &recordingSessionManager{
		sessions: map[sessionrt.SessionID]*sessionrt.Session{
			"sess-1": {
				ID: "sess-1",
				Participants: map[sessionrt.ActorID]sessionrt.Participant{
					"agent:a": {ID: "agent:a", Type: sessionrt.ActorAgent},
				},
				Status: sessionrt.SessionActive,
			},
		},
	}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		recipByAgent:  map[sessionrt.ActorID]string{},
		routes:        map[string]conversationRoute{},
		processing:    map[string]int{"!dm:one": 1},
		heartbeats:    map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!dm:one", "sess-1")
	pipeline.setConversationRoute("!dm:one", "agent:a", "@agent:a", ConversationModeDM)

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

func TestHeartbeatRunnerProcessDueSkipsDuringSleepingHoursWhenTimezoneConfigured(t *testing.T) {
	now := time.Date(2026, 2, 18, 7, 0, 0, 0, time.UTC) // 02:00 in America/New_York.
	manager := &recordingSessionManager{
		sessions: map[sessionrt.SessionID]*sessionrt.Session{
			"sess-1": {
				ID: "sess-1",
				Participants: map[sessionrt.ActorID]sessionrt.Participant{
					"agent:a": {ID: "agent:a", Type: sessionrt.ActorAgent},
				},
				Status: sessionrt.SessionActive,
			},
		},
	}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		recipByAgent:  map[sessionrt.ActorID]string{},
		routes:        map[string]conversationRoute{},
		processing:    map[string]int{},
		heartbeats:    map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!dm:one", "sess-1")
	pipeline.setConversationRoute("!dm:one", "agent:a", "@agent:a", ConversationModeDM)

	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  manager,
		Pipeline: pipeline,
		Schedules: []HeartbeatSchedule{{
			AgentID:     "agent:a",
			Every:       time.Minute,
			Prompt:      "hb",
			AckMaxChars: 123,
			Timezone:    "America/New_York",
		}},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}

	runner.nextRun["agent:a"] = now
	runner.processDue(context.Background())

	if len(manager.sentEvents()) != 0 {
		t.Fatalf("expected no heartbeat dispatch during configured sleeping hours")
	}
	if _, ok := pipeline.heartbeats["!dm:one"]; ok {
		t.Fatalf("did not expect pending heartbeat state when dispatch is skipped")
	}
}

func TestHeartbeatRunnerProcessDueDispatchesOutsideSleepingHoursWhenTimezoneConfigured(t *testing.T) {
	now := time.Date(2026, 2, 18, 16, 0, 0, 0, time.UTC) // 11:00 in America/New_York.
	manager := &recordingSessionManager{
		sessions: map[sessionrt.SessionID]*sessionrt.Session{
			"sess-1": {
				ID: "sess-1",
				Participants: map[sessionrt.ActorID]sessionrt.Participant{
					"agent:a": {ID: "agent:a", Type: sessionrt.ActorAgent},
				},
				Status: sessionrt.SessionActive,
			},
		},
	}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		recipByAgent:  map[sessionrt.ActorID]string{},
		routes:        map[string]conversationRoute{},
		processing:    map[string]int{},
		heartbeats:    map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!dm:one", "sess-1")
	pipeline.setConversationRoute("!dm:one", "agent:a", "@agent:a", ConversationModeDM)

	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  manager,
		Pipeline: pipeline,
		Schedules: []HeartbeatSchedule{{
			AgentID:     "agent:a",
			Every:       time.Minute,
			Prompt:      "hb",
			AckMaxChars: 123,
			Timezone:    "America/New_York",
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
}

func TestHeartbeatRunnerDispatchesOnlyWhenScheduledAgentIsParticipant(t *testing.T) {
	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	manager := &recordingSessionManager{
		sessions: map[sessionrt.SessionID]*sessionrt.Session{
			"sess-room": {
				ID: "sess-room",
				Participants: map[sessionrt.ActorID]sessionrt.Participant{
					"agent:a": {ID: "agent:a", Type: sessionrt.ActorAgent},
					"agent:b": {ID: "agent:b", Type: sessionrt.ActorAgent},
				},
				Status: sessionrt.SessionActive,
			},
		},
	}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		recipByAgent: map[sessionrt.ActorID]string{
			"agent:a": "@agent:a",
			"agent:b": "@agent:b",
		},
		routes:     map[string]conversationRoute{},
		processing: map[string]int{},
		heartbeats: map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!room:one", "sess-room")

	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  manager,
		Pipeline: pipeline,
		Schedules: []HeartbeatSchedule{
			{AgentID: "agent:b", Every: time.Minute, Prompt: "hb-b"},
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}
	runner.nextRun["agent:b"] = now
	runner.processDue(context.Background())

	events := manager.sentEvents()
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
	payload, ok := events[0].Payload.(sessionrt.Message)
	if !ok {
		t.Fatalf("payload type = %T, want session.Message", events[0].Payload)
	}
	if payload.TargetActorID != "agent:b" {
		t.Fatalf("target actor id = %q, want agent:b", payload.TargetActorID)
	}
}

func TestHeartbeatRunnerDispatchesPerAgentSchedulesForSameSession(t *testing.T) {
	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	manager := &recordingSessionManager{
		sessions: map[sessionrt.SessionID]*sessionrt.Session{
			"sess-room": {
				ID: "sess-room",
				Participants: map[sessionrt.ActorID]sessionrt.Participant{
					"agent:a": {ID: "agent:a", Type: sessionrt.ActorAgent},
					"agent:b": {ID: "agent:b", Type: sessionrt.ActorAgent},
				},
				Status: sessionrt.SessionActive,
			},
		},
	}
	pipeline := &DMPipeline{
		agentID:       "agent:a",
		conversations: NewConversationSessionMap(),
		recipByAgent: map[sessionrt.ActorID]string{
			"agent:a": "@agent:a",
			"agent:b": "@agent:b",
		},
		routes:     map[string]conversationRoute{},
		processing: map[string]int{},
		heartbeats: map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!room:one", "sess-room")

	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  manager,
		Pipeline: pipeline,
		Schedules: []HeartbeatSchedule{
			{AgentID: "agent:a", Every: time.Minute, Prompt: "hb-a"},
			{AgentID: "agent:b", Every: time.Minute, Prompt: "hb-b"},
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}
	runner.nextRun["agent:a"] = now
	runner.nextRun["agent:b"] = now
	runner.processDue(context.Background())

	events := manager.sentEvents()
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2", len(events))
	}
	targets := map[sessionrt.ActorID]struct{}{}
	for _, event := range events {
		payload, ok := event.Payload.(sessionrt.Message)
		if !ok {
			t.Fatalf("payload type = %T, want session.Message", event.Payload)
		}
		targets[payload.TargetActorID] = struct{}{}
	}
	if _, ok := targets["agent:a"]; !ok {
		t.Fatalf("missing target agent:a")
	}
	if _, ok := targets["agent:b"]; !ok {
		t.Fatalf("missing target agent:b")
	}
}

func TestHeartbeatRunnerSkipsWhenScheduledAgentNotInRoomMembership(t *testing.T) {
	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	manager := &recordingSessionManager{
		sessions: map[sessionrt.SessionID]*sessionrt.Session{
			"sess-room": {
				ID: "sess-room",
				Participants: map[sessionrt.ActorID]sessionrt.Participant{
					"agent:b": {ID: "agent:b", Type: sessionrt.ActorAgent},
				},
				Status: sessionrt.SessionActive,
			},
		},
	}
	pipeline := &DMPipeline{
		agentID:       "agent:b",
		transport:     &fakeTransport{managed: map[string][]string{"!room:one": {"@agent:a"}}},
		conversations: NewConversationSessionMap(),
		recipByAgent: map[sessionrt.ActorID]string{
			"agent:b": "@agent:b",
		},
		routes:     map[string]conversationRoute{},
		processing: map[string]int{},
		heartbeats: map[string]heartbeatState{},
	}
	pipeline.conversations.Set("!room:one", "sess-room")

	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  manager,
		Pipeline: pipeline,
		Schedules: []HeartbeatSchedule{
			{AgentID: "agent:b", Every: time.Minute, Prompt: "hb-b"},
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}
	runner.nextRun["agent:b"] = now
	runner.processDue(context.Background())

	if len(manager.sentEvents()) != 0 {
		t.Fatalf("expected no heartbeat dispatch when scheduled agent is not in room membership")
	}
}

func TestHeartbeatRunnerAllowsEmptySchedules(t *testing.T) {
	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager:  &recordingSessionManager{sessions: map[sessionrt.SessionID]*sessionrt.Session{}},
		Pipeline: &DMPipeline{conversations: NewConversationSessionMap(), routes: map[string]conversationRoute{}},
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}
	if len(runner.schedulesSnapshot()) != 0 {
		t.Fatalf("schedule count = %d, want 0", len(runner.schedulesSnapshot()))
	}
}

func TestHeartbeatRunnerUpsertAndRemoveSchedule(t *testing.T) {
	now := time.Date(2026, 2, 18, 12, 0, 0, 0, time.UTC)
	runner, err := NewHeartbeatRunner(HeartbeatRunnerOptions{
		Manager: &recordingSessionManager{
			sessions: map[sessionrt.SessionID]*sessionrt.Session{},
		},
		Pipeline: &DMPipeline{
			conversations: NewConversationSessionMap(),
			routes:        map[string]conversationRoute{},
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}

	if err := runner.UpsertSchedule(HeartbeatSchedule{
		AgentID: "agent:b",
		Every:   2 * time.Minute,
		Prompt:  "hb-b",
	}); err != nil {
		t.Fatalf("UpsertSchedule() error: %v", err)
	}
	if err := runner.UpsertSchedule(HeartbeatSchedule{
		AgentID: "agent:a",
		Every:   time.Minute,
		Prompt:  "hb-a",
	}); err != nil {
		t.Fatalf("UpsertSchedule() error: %v", err)
	}

	schedules := runner.schedulesSnapshot()
	if len(schedules) != 2 {
		t.Fatalf("schedule count = %d, want 2", len(schedules))
	}
	if schedules[0].AgentID != "agent:a" || schedules[1].AgentID != "agent:b" {
		t.Fatalf("schedule order = [%s %s], want [agent:a agent:b]", schedules[0].AgentID, schedules[1].AgentID)
	}
	if got := runner.nextRunFor("agent:a", time.Time{}); !got.Equal(now.Add(time.Minute)) {
		t.Fatalf("next run = %s, want %s", got, now.Add(time.Minute))
	}
	if removed := runner.RemoveSchedule("agent:a"); !removed {
		t.Fatalf("expected RemoveSchedule to return true")
	}
	if got := runner.nextRunFor("agent:a", time.Time{}); !got.IsZero() {
		t.Fatalf("next run after remove = %s, want zero", got)
	}
}
