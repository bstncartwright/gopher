package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

type eventStoreWithoutRegistry struct {
	base *InMemoryEventStore
}

func (s eventStoreWithoutRegistry) Append(ctx context.Context, e Event) error {
	return s.base.Append(ctx, e)
}

func (s eventStoreWithoutRegistry) List(ctx context.Context, sessionID SessionID) ([]Event, error) {
	return s.base.List(ctx, sessionID)
}

func (s eventStoreWithoutRegistry) Stream(ctx context.Context, sessionID SessionID) (<-chan Event, error) {
	return s.base.Stream(ctx, sessionID)
}

func TestManagerCreateAndGetSession(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	manager, err := NewManager(ManagerOptions{Store: store})
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
	if created.Status != SessionActive {
		t.Fatalf("expected active status, got %v", created.Status)
	}
	if len(created.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %d", len(created.Participants))
	}

	loaded, err := manager.GetSession(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if loaded.ID != created.ID {
		t.Fatalf("expected same session ID, got %q", loaded.ID)
	}
	loaded.Participants["user:me"] = Participant{ID: "user:other", Type: ActorHuman}

	loadedAgain, err := manager.GetSession(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSession() second error: %v", err)
	}
	if loadedAgain.Participants["user:me"].ID != "user:me" {
		t.Fatalf("expected participant copy isolation")
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected creation event to be recorded")
	}
	if events[0].Type != EventControl {
		t.Fatalf("expected first event type control, got %q", events[0].Type)
	}
	ctrl, ok := controlFromPayload(events[0].Payload)
	if !ok {
		t.Fatalf("expected control payload")
	}
	if ctrl.Action != ControlActionSessionCreated {
		t.Fatalf("expected action %q, got %q", ControlActionSessionCreated, ctrl.Action)
	}
}

func TestManagerUnknownSessionOperations(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	manager, err := NewManager(ManagerOptions{Store: store})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	_, err = manager.GetSession(context.Background(), "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound from GetSession, got %v", err)
	}

	err = manager.SendEvent(context.Background(), Event{
		SessionID: "missing",
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "hello"},
	})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound from SendEvent, got %v", err)
	}

	_, err = manager.Subscribe(context.Background(), "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound from Subscribe, got %v", err)
	}

	err = manager.CancelSession(context.Background(), "missing")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound from CancelSession, got %v", err)
	}
}

func TestManagerCancelSessionRejectsFurtherEvents(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	manager, err := NewManager(ManagerOptions{Store: store})
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

	if err := manager.CancelSession(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelSession() error: %v", err)
	}

	loaded, err := manager.GetSession(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if loaded.Status != SessionPaused {
		t.Fatalf("expected paused status after cancel, got %v", loaded.Status)
	}

	err = manager.SendEvent(context.Background(), Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "hello"},
	})
	if !errors.Is(err, ErrSessionNotActive) {
		t.Fatalf("expected ErrSessionNotActive, got %v", err)
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected at least creation + cancel events, got %d", len(events))
	}
	last := events[len(events)-1]
	if last.Type != EventControl {
		t.Fatalf("expected final event type control, got %q", last.Type)
	}
	ctrl, ok := controlFromPayload(last.Payload)
	if !ok {
		t.Fatalf("expected control payload")
	}
	if ctrl.Action != ControlActionSessionCancelled {
		t.Fatalf("expected cancel control action, got %q", ctrl.Action)
	}
}

func TestManagerSessionRecordAccessorsWithRegistry(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	manager, err := NewManager(ManagerOptions{Store: store})
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

	record, err := manager.GetSessionRecord(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	updatedAt := record.UpdatedAt.Add(-5 * time.Minute)
	record.UpdatedAt = updatedAt
	if err := manager.UpsertSessionRecord(context.Background(), record); err != nil {
		t.Fatalf("UpsertSessionRecord() error: %v", err)
	}
	loaded, err := manager.GetSessionRecord(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord() second error: %v", err)
	}
	if !loaded.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("updated_at = %s, want %s", loaded.UpdatedAt, updatedAt)
	}
}

func TestManagerSessionRecordAccessorsWithoutRegistry(t *testing.T) {
	base := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	store := eventStoreWithoutRegistry{base: base}
	manager, err := NewManager(ManagerOptions{Store: store})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	if _, err := manager.GetSessionRecord(context.Background(), "sess-missing"); err == nil {
		t.Fatalf("expected GetSessionRecord to fail without registry")
	}
	if err := manager.UpsertSessionRecord(context.Background(), SessionRecord{SessionID: "sess-missing"}); err == nil {
		t.Fatalf("expected UpsertSessionRecord to fail without registry")
	}
}

func TestManagerCreateSessionAssignsDefaultDisplayName(t *testing.T) {
	now := time.Date(2026, time.March, 3, 12, 34, 56, 0, time.UTC)
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	manager, err := NewManager(ManagerOptions{
		Store: store,
		Now: func() time.Time {
			return now
		},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), CreateSessionOptions{
		Participants: []Participant{
			{ID: "agent:writer", Type: ActorAgent},
			{ID: "user:me", Type: ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	want := "Session with writer and me (2026-03-03 12:34 UTC)"
	if created.DisplayName != want {
		t.Fatalf("display name = %q, want %q", created.DisplayName, want)
	}

	record, err := manager.GetSessionRecord(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	if record.DisplayName != want {
		t.Fatalf("record display name = %q, want %q", record.DisplayName, want)
	}
}

func TestManagerCreateSessionUsesProvidedDisplayName(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	manager, err := NewManager(ManagerOptions{Store: store})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), CreateSessionOptions{
		Participants: []Participant{
			{ID: "agent:writer", Type: ActorAgent},
			{ID: "user:me", Type: ActorHuman},
		},
		DisplayName: "  Writer Room  ",
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if created.DisplayName != "Writer Room" {
		t.Fatalf("display name = %q, want Writer Room", created.DisplayName)
	}
}
