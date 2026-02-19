package agentcore

import (
	"context"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestSessionRuntimeAdapterRoutesUserToRunTurn(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"fs"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	finalAssistant := ai.NewAssistantMessage(agent.model)
	finalAssistant.StopReason = ai.StopReasonStop
	finalAssistant.Content = []ai.ContentBlock{
		{Type: ai.ContentTypeText, Text: "adapter-ok"},
	}
	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{assistant: finalAssistant},
		},
	}

	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: NewSessionRuntimeAdapter(agent),
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: sessionrt.ActorID(agent.ID), Type: sessionrt.ActorAgent},
			{ID: "user:me", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	if err := manager.SendEvent(context.Background(), sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "say adapter-ok"},
	}); err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected at least creation + user + agent events, got %d", len(events))
	}

	foundUser := false
	foundAgent := false
	for _, event := range events {
		if event.Type != sessionrt.EventMessage {
			continue
		}
		msg, ok := event.Payload.(sessionrt.Message)
		if !ok {
			t.Fatalf("expected session message payload type, got %T", event.Payload)
		}
		if msg.Role == sessionrt.RoleUser {
			foundUser = true
		}
		if msg.Role == sessionrt.RoleAgent && msg.Content == "adapter-ok" {
			foundAgent = true
		}
	}
	if !foundUser {
		t.Fatalf("expected user message event in session history")
	}
	if !foundAgent {
		t.Fatalf("expected agent response event in session history")
	}
}

func TestWithTurnTimeoutAddsDeadlineWhenMissing(t *testing.T) {
	prev := sessionRuntimeTurnTimeout
	sessionRuntimeTurnTimeout = time.Second
	defer func() { sessionRuntimeTurnTimeout = prev }()

	ctx, cancel := withTurnTimeout(context.Background())
	defer cancel()

	if _, ok := ctx.Deadline(); !ok {
		t.Fatalf("expected deadline to be added")
	}
}

func TestWithTurnTimeoutPreservesExistingDeadline(t *testing.T) {
	prev := sessionRuntimeTurnTimeout
	sessionRuntimeTurnTimeout = time.Second
	defer func() { sessionRuntimeTurnTimeout = prev }()

	base, baseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer baseCancel()
	baseDeadline, _ := base.Deadline()

	ctx, cancel := withTurnTimeout(base)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("expected existing deadline to be preserved")
	}
	if !deadline.Equal(baseDeadline) {
		t.Fatalf("deadline changed: got %s want %s", deadline, baseDeadline)
	}
}

func TestSessionRuntimeAdapterDeltaCaptureOptions(t *testing.T) {
	config := defaultConfig()
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.CaptureThinkingDeltas = true

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "final"}}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{
				assistant: assistant,
				events: []ai.AssistantMessageEvent{
					{Type: ai.EventTextDelta, Delta: "fi"},
					{Type: ai.EventThinkingDelta, Delta: "private"},
					{Type: ai.EventTextDelta, Delta: "nal"},
				},
			},
		},
	}

	input := sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "agent:test",
		History: []sessionrt.Event{
			{
				Type:    sessionrt.EventMessage,
				Payload: sessionrt.Message{Role: sessionrt.RoleUser, Content: "run"},
			},
		},
	}

	defaultAdapter := NewSessionRuntimeAdapter(agent)
	defaultOut, err := defaultAdapter.Step(context.Background(), input)
	if err != nil {
		t.Fatalf("default adapter Step() error: %v", err)
	}
	if hasEventType(defaultOut.Events, sessionrt.EventAgentDelta) {
		t.Fatalf("default adapter should not emit agent deltas")
	}
	if hasEventType(defaultOut.Events, sessionrt.EventAgentThinkingDelta) {
		t.Fatalf("default adapter should not emit thinking deltas")
	}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{
				assistant: assistant,
				events: []ai.AssistantMessageEvent{
					{Type: ai.EventTextDelta, Delta: "fi"},
					{Type: ai.EventThinkingDelta, Delta: "private"},
					{Type: ai.EventTextDelta, Delta: "nal"},
				},
			},
		},
	}

	optAdapter := NewSessionRuntimeAdapterWithOptions(agent, SessionRuntimeAdapterOptions{
		CaptureDeltas:   true,
		CaptureThinking: true,
	})
	optOut, err := optAdapter.Step(context.Background(), input)
	if err != nil {
		t.Fatalf("options adapter Step() error: %v", err)
	}
	if !hasEventType(optOut.Events, sessionrt.EventAgentDelta) {
		t.Fatalf("expected options adapter to emit agent delta events")
	}
	if !hasEventType(optOut.Events, sessionrt.EventAgentThinkingDelta) {
		t.Fatalf("expected options adapter to emit thinking delta events")
	}
}

func hasEventType(events []sessionrt.Event, target sessionrt.EventType) bool {
	for _, event := range events {
		if event.Type == target {
			return true
		}
	}
	return false
}
