package agentcore

import (
	"context"
	"testing"

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
