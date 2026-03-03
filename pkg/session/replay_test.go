package session

import (
	"context"
	"testing"
	"time"
)

func TestReplayRebuildsSessionState(t *testing.T) {
	now := time.Now().UTC()
	events := []Event{
		{
			ID:        "s1-000001",
			SessionID: "s1",
			From:      "system",
			Type:      EventControl,
			Payload: ControlPayload{
				Action: ControlActionSessionCreated,
				Metadata: map[string]any{
					"participants": []Participant{
						{ID: "agent:a", Type: ActorAgent},
						{ID: "user:me", Type: ActorHuman},
					},
				},
			},
			Timestamp: now,
			Seq:       1,
		},
		{
			ID:        "s1-000002",
			SessionID: "s1",
			From:      "user:me",
			Type:      EventMessage,
			Payload:   Message{Role: RoleUser, Content: "hello"},
			Timestamp: now.Add(1 * time.Second),
			Seq:       2,
		},
		{
			ID:        "s1-000003",
			SessionID: "s1",
			From:      "system",
			Type:      EventControl,
			Payload: ControlPayload{
				Action: ControlActionSessionCancelled,
				Reason: "manual",
			},
			Timestamp: now.Add(2 * time.Second),
			Seq:       3,
		},
	}

	replayed, err := Replay(events)
	if err != nil {
		t.Fatalf("Replay() error: %v", err)
	}
	if replayed.ID != "s1" {
		t.Fatalf("expected session ID s1, got %q", replayed.ID)
	}
	if replayed.Status != SessionPaused {
		t.Fatalf("expected paused status, got %v", replayed.Status)
	}
	if len(replayed.Participants) != 2 {
		t.Fatalf("expected 2 participants, got %d", len(replayed.Participants))
	}
}

func TestReplayDetectsSequenceGap(t *testing.T) {
	now := time.Now().UTC()
	_, err := Replay([]Event{
		{
			ID:        "s1-000001",
			SessionID: "s1",
			From:      "system",
			Type:      EventControl,
			Payload:   ControlPayload{Action: ControlActionSessionCreated},
			Timestamp: now,
			Seq:       1,
		},
		{
			ID:        "s1-000003",
			SessionID: "s1",
			From:      "system",
			Type:      EventControl,
			Payload:   ControlPayload{Action: ControlActionSessionCancelled},
			Timestamp: now.Add(1 * time.Second),
			Seq:       3,
		},
	})
	if err == nil {
		t.Fatalf("expected Replay() to fail on sequence gap")
	}
}

func TestReplayFromStore(t *testing.T) {
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

	replayed, err := ReplayFromStore(context.Background(), store, created.ID)
	if err != nil {
		t.Fatalf("ReplayFromStore() error: %v", err)
	}
	if replayed.Status != SessionPaused {
		t.Fatalf("expected paused status, got %v", replayed.Status)
	}
	if len(replayed.Participants) != 2 {
		t.Fatalf("expected participant metadata from creation event")
	}
}

func TestReplayRestoresDisplayNameFromCreatedMetadata(t *testing.T) {
	now := time.Now().UTC()
	replayed, err := Replay([]Event{
		{
			ID:        "s2-000001",
			SessionID: "s2",
			From:      "system",
			Type:      EventControl,
			Payload: ControlPayload{
				Action: ControlActionSessionCreated,
				Metadata: map[string]any{
					"display_name": "Planner Session",
				},
			},
			Timestamp: now,
			Seq:       1,
		},
	})
	if err != nil {
		t.Fatalf("Replay() error: %v", err)
	}
	if replayed.DisplayName != "Planner Session" {
		t.Fatalf("display name = %q, want Planner Session", replayed.DisplayName)
	}
}
