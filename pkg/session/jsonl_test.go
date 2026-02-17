package session

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestJSONLPersistenceMatchesInMemoryHistory(t *testing.T) {
	logDir := t.TempDir()
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{JSONLDir: logDir})
	exec := &recordingExecutor{
		output: AgentOutput{
			Events: []Event{
				{
					Type:    EventMessage,
					Payload: Message{Role: RoleAgent, Content: "ack"},
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
			{ID: "agent:a", Type: ActorAgent},
			{ID: "user:me", Type: ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := manager.SendEvent(context.Background(), Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "ping"},
	}); err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}

	inMemoryEvents, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(inMemoryEvents) == 0 {
		t.Fatalf("expected in-memory events")
	}

	path := SessionJSONLPath(logDir, created.ID)
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open jsonl file: %v", err)
	}
	defer file.Close()

	jsonlEvents := make([]Event, 0, len(inMemoryEvents))
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("unmarshal jsonl event: %v", err)
		}
		jsonlEvents = append(jsonlEvents, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan jsonl: %v", err)
	}

	if len(jsonlEvents) != len(inMemoryEvents) {
		t.Fatalf("expected %d jsonl events, got %d", len(inMemoryEvents), len(jsonlEvents))
	}
	for i := range inMemoryEvents {
		if jsonlEvents[i].Seq != inMemoryEvents[i].Seq {
			t.Fatalf("event %d seq mismatch: jsonl=%d mem=%d", i, jsonlEvents[i].Seq, inMemoryEvents[i].Seq)
		}
		if jsonlEvents[i].Type != inMemoryEvents[i].Type {
			t.Fatalf("event %d type mismatch: jsonl=%q mem=%q", i, jsonlEvents[i].Type, inMemoryEvents[i].Type)
		}
		if jsonlEvents[i].SessionID != inMemoryEvents[i].SessionID {
			t.Fatalf("event %d session mismatch: jsonl=%q mem=%q", i, jsonlEvents[i].SessionID, inMemoryEvents[i].SessionID)
		}
	}

	replayed, err := Replay(jsonlEvents)
	if err != nil {
		t.Fatalf("Replay(jsonlEvents) error: %v", err)
	}
	if replayed.ID != created.ID {
		t.Fatalf("expected replayed session ID %q, got %q", created.ID, replayed.ID)
	}
	if replayed.Status != SessionActive {
		t.Fatalf("expected active replayed session status, got %v", replayed.Status)
	}
}
