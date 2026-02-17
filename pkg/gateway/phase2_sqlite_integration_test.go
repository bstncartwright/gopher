package gateway

import (
	"context"
	"path/filepath"
	"testing"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	sqlitestore "github.com/bstncartwright/gopher/pkg/store/sqlite"
)

func TestGatewayRecoveryWithSQLiteStore(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitestore.NewEventStore(sqlitestore.EventStoreOptions{Path: filepath.Join(t.TempDir(), "phase2.db")})
	if err != nil {
		t.Fatalf("NewEventStore() error: %v", err)
	}
	defer store.Close()

	exec := &staticExecutor{text: "ack"}

	managerA, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, Executor: exec})
	if err != nil {
		t.Fatalf("NewManager(A) error: %v", err)
	}
	created, err := managerA.CreateSession(ctx, sessionrt.CreateSessionOptions{Participants: []sessionrt.Participant{
		{ID: "agent:a", Type: sessionrt.ActorAgent},
		{ID: "user:me", Type: sessionrt.ActorHuman},
	}})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	if err := managerA.SendEvent(ctx, sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "one"},
	}); err != nil {
		t.Fatalf("SendEvent(A) error: %v", err)
	}

	managerB, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, Executor: exec, RecoverOnStart: true})
	if err != nil {
		t.Fatalf("NewManager(B) error: %v", err)
	}
	if err := managerB.SendEvent(ctx, sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "two"},
	}); err != nil {
		t.Fatalf("SendEvent(B) error: %v", err)
	}

	events, err := store.List(ctx, created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 events across restart, got %d", len(events))
	}
	for i, event := range events {
		want := uint64(i + 1)
		if event.Seq != want {
			t.Fatalf("event %d seq = %d, want %d", i, event.Seq, want)
		}
	}
}
