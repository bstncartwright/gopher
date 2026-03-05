package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type cronAckExecutor struct{}

func (e *cronAckExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: "ack",
				},
			},
		},
	}, nil
}

func TestSessionCronDispatcherInjectsUserMessageToSession(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &cronAckExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	session, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "agent:a", Type: sessionrt.ActorAgent},
			{ID: "human:a", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	stream, err := manager.Subscribe(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}

	dispatcher, err := NewSessionCronDispatcher(manager)
	if err != nil {
		t.Fatalf("NewSessionCronDispatcher() error: %v", err)
	}
	if _, err := dispatcher.Dispatch(context.Background(), CronJob{
		ID:            "cron-1",
		SessionID:     string(session.ID),
		Title:         "Ping",
		Message:       "scheduled ping",
		Timezone:      "UTC",
		Mode:          CronModeSession,
		NotifyActorID: "agent:a",
	}, time.Now().UTC()); err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	sawAgentReply := false
	for time.Now().Before(deadline) {
		select {
		case event := <-stream:
			if event.Type != sessionrt.EventMessage {
				continue
			}
			msg, ok := event.Payload.(sessionrt.Message)
			if !ok {
				continue
			}
			if msg.Role == sessionrt.RoleAgent && msg.Content == "ack" {
				sawAgentReply = true
			}
		default:
			time.Sleep(10 * time.Millisecond)
		}
		if sawAgentReply {
			break
		}
	}
	if !sawAgentReply {
		t.Fatalf("expected injected user message to trigger agent reply")
	}

	events, err := store.List(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	var injected string
	for _, event := range events {
		if event.Type != sessionrt.EventMessage {
			continue
		}
		msg, ok := event.Payload.(sessionrt.Message)
		if !ok || msg.Role != sessionrt.RoleUser {
			continue
		}
		injected = msg.Content
	}
	if !strings.Contains(injected, "[scheduled task]") {
		t.Fatalf("expected scheduled task wrapper, got %q", injected)
	}
	if !strings.Contains(injected, "task_id: cron-1") {
		t.Fatalf("expected task id in wrapper, got %q", injected)
	}
	if !strings.Contains(injected, "mode: session") {
		t.Fatalf("expected session mode in wrapper, got %q", injected)
	}
	if !strings.Contains(injected, "Instructions:\nscheduled ping") {
		t.Fatalf("expected instructions block, got %q", injected)
	}
}
