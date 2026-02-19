package session

import (
	"context"
	"errors"
	"testing"
)

type recordingExecutor struct {
	calls  int
	inputs []AgentInput
	output AgentOutput
	err    error
}

func (r *recordingExecutor) Step(_ context.Context, input AgentInput) (AgentOutput, error) {
	r.calls++
	r.inputs = append(r.inputs, input)
	if r.err != nil {
		return AgentOutput{}, r.err
	}
	return r.output, nil
}

func TestRuntimeUserMessageTriggersDeterministicAgentStep(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	exec := &recordingExecutor{
		output: AgentOutput{
			Events: []Event{
				{
					Type:    EventMessage,
					Payload: Message{Role: RoleAgent, Content: "response"},
				},
			},
		},
	}
	manager, err := NewManager(ManagerOptions{
		Store:    store,
		Executor: exec,
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), CreateSessionOptions{
		Participants: []Participant{
			{ID: "agent:z", Type: ActorAgent},
			{ID: "user:me", Type: ActorHuman},
			{ID: "agent:a", Type: ActorAgent},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	err = manager.SendEvent(context.Background(), Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "ping"},
	})
	if err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}

	if exec.calls != 1 {
		t.Fatalf("expected exactly one executor call, got %d", exec.calls)
	}
	if len(exec.inputs) != 1 {
		t.Fatalf("expected one executor input, got %d", len(exec.inputs))
	}
	if exec.inputs[0].ActorID != "agent:a" {
		t.Fatalf("expected lexicographically-first agent actor, got %q", exec.inputs[0].ActorID)
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events (created, user message, agent message), got %d", len(events))
	}
	for idx, event := range events {
		wantSeq := uint64(idx + 1)
		if event.Seq != wantSeq {
			t.Fatalf("expected seq %d, got %d", wantSeq, event.Seq)
		}
	}

	if events[1].Type != EventMessage {
		t.Fatalf("expected user message event at index 1, got %q", events[1].Type)
	}
	userMsg, ok := messageFromPayload(events[1].Payload)
	if !ok || userMsg.Role != RoleUser {
		t.Fatalf("expected role user at index 1")
	}

	if events[2].Type != EventMessage {
		t.Fatalf("expected agent message event at index 2, got %q", events[2].Type)
	}
	agentMsg, ok := messageFromPayload(events[2].Payload)
	if !ok || agentMsg.Role != RoleAgent {
		t.Fatalf("expected role agent at index 2")
	}
	if events[2].From != "agent:a" {
		t.Fatalf("expected emitted event from selected agent, got %q", events[2].From)
	}
}

func TestRuntimeExecutorErrorKeepsSessionActive(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	exec := &recordingExecutor{err: errors.New("executor boom")}
	manager, err := NewManager(ManagerOptions{
		Store:    store,
		Executor: exec,
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), CreateSessionOptions{
		Participants: []Participant{
			{ID: "agent:a", Type: ActorAgent},
			{ID: "user:me", Type: ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	err = manager.SendEvent(context.Background(), Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "trigger"},
	})
	if err == nil {
		t.Fatalf("expected SendEvent() to fail when executor returns error")
	}

	loaded, err := manager.GetSession(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if loaded.Status != SessionActive {
		t.Fatalf("expected active status, got %v", loaded.Status)
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected created + user message + error events, got %d", len(events))
	}
	last := events[len(events)-1]
	if last.Type != EventError {
		t.Fatalf("expected final event type error, got %q", last.Type)
	}
}

func TestRuntimeUserMessageCanTriggerMultipleAgents(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	exec := &recordingExecutor{
		output: AgentOutput{
			Events: []Event{
				{
					Type:    EventMessage,
					Payload: Message{Role: RoleAgent, Content: "response"},
				},
			},
		},
	}
	manager, err := NewManager(ManagerOptions{
		Store:    store,
		Executor: exec,
		AgentSelector: func(_ *Session, _ Event) ([]ActorID, bool) {
			return []ActorID{"agent:a", "agent:z"}, true
		},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), CreateSessionOptions{
		Participants: []Participant{
			{ID: "agent:z", Type: ActorAgent},
			{ID: "user:me", Type: ActorHuman},
			{ID: "agent:a", Type: ActorAgent},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	err = manager.SendEvent(context.Background(), Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "ping"},
	})
	if err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}

	if exec.calls != 2 {
		t.Fatalf("expected exactly two executor calls, got %d", exec.calls)
	}
	if len(exec.inputs) != 2 {
		t.Fatalf("expected two executor inputs, got %d", len(exec.inputs))
	}
	if exec.inputs[0].ActorID != "agent:a" || exec.inputs[1].ActorID != "agent:z" {
		t.Fatalf("actor IDs = [%q %q], want [agent:a agent:z]", exec.inputs[0].ActorID, exec.inputs[1].ActorID)
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events (created, user message, two agent messages), got %d", len(events))
	}
	if events[2].From != "agent:a" || events[3].From != "agent:z" {
		t.Fatalf("event sources = [%q %q], want [agent:a agent:z]", events[2].From, events[3].From)
	}
}
