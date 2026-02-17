package agentcore

import (
	"context"
	"sync"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type runTurnSessionAdapter struct {
	agent *Agent

	mu       sync.Mutex
	sessions map[sessionrt.SessionID]*Session
}

func newRunTurnSessionAdapter(agent *Agent) *runTurnSessionAdapter {
	return &runTurnSessionAdapter{
		agent:    agent,
		sessions: make(map[sessionrt.SessionID]*Session),
	}
}

func (a *runTurnSessionAdapter) Step(ctx context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	userMsg, ok := latestUserMessage(input.History)
	if !ok {
		return sessionrt.AgentOutput{}, nil
	}

	a.mu.Lock()
	s, exists := a.sessions[input.SessionID]
	if !exists {
		s = a.agent.NewSession()
		s.ID = string(input.SessionID)
		a.sessions[input.SessionID] = s
	}
	a.mu.Unlock()

	result, err := a.agent.RunTurn(ctx, s, TurnInput{UserMessage: userMsg.Content})
	if err != nil {
		return sessionrt.AgentOutput{}, err
	}

	out := make([]sessionrt.Event, 0, len(result.Events))
	for _, event := range result.Events {
		switch event.Type {
		case EventTypeAgentMsg:
			text, _ := stringPayloadField(event.Payload, "text")
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventMessage,
				Payload: sessionrt.Message{Role: sessionrt.RoleAgent, Content: text},
			})
		case EventTypeToolCall:
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventToolCall,
				Payload: clonePayloadMap(event.Payload),
			})
		case EventTypeToolResult:
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventToolResult,
				Payload: clonePayloadMap(event.Payload),
			})
		case EventTypeError:
			msg, _ := stringPayloadField(event.Payload, "message")
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventError,
				Payload: sessionrt.ErrorPayload{Message: msg},
			})
		}
	}

	return sessionrt.AgentOutput{Events: out}, nil
}

func latestUserMessage(events []sessionrt.Event) (sessionrt.Message, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessionrt.EventMessage {
			continue
		}
		switch payload := event.Payload.(type) {
		case sessionrt.Message:
			if payload.Role == sessionrt.RoleUser {
				return payload, true
			}
		case map[string]any:
			roleRaw, ok := payload["role"].(string)
			if !ok || roleRaw != string(sessionrt.RoleUser) {
				continue
			}
			content, ok := payload["content"].(string)
			if !ok {
				continue
			}
			return sessionrt.Message{Role: sessionrt.RoleUser, Content: content}, true
		}
	}
	return sessionrt.Message{}, false
}

func stringPayloadField(payload any, key string) (string, bool) {
	value, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	text, ok := value[key].(string)
	if !ok {
		return "", false
	}
	return text, true
}

func clonePayloadMap(payload any) map[string]any {
	src, ok := payload.(map[string]any)
	if !ok || src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

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
		Executor: newRunTurnSessionAdapter(agent),
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
