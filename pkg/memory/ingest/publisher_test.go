package ingest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type recordingManager struct {
	mu       sync.Mutex
	records  []memory.MemoryRecord
	storeErr error
}

func (m *recordingManager) Store(_ context.Context, record memory.MemoryRecord) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, record)
	return nil
}

func (m *recordingManager) Retrieve(_ context.Context, _ memory.MemoryQuery) ([]memory.MemoryRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]memory.MemoryRecord, len(m.records))
	copy(out, m.records)
	return out, nil
}

func TestSessionPublisherFlushesOnTerminalControl(t *testing.T) {
	manager := &recordingManager{}
	publisher := NewSessionPublisher(SessionPublisherOptions{Manager: manager, FlushEvery: 100})
	now := time.Now().UTC()

	events := []sessionrt.Event{
		{
			SessionID: "sess-1",
			From:      "user:me",
			Type:      sessionrt.EventMessage,
			Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "remember: keep lint strict"},
			Seq:       1,
			Timestamp: now,
		},
		{
			SessionID: "sess-1",
			From:      "agent-1",
			Type:      sessionrt.EventMessage,
			Payload:   sessionrt.Message{Role: sessionrt.RoleAgent, Content: "acknowledged"},
			Seq:       2,
			Timestamp: now,
		},
		{
			SessionID: "sess-1",
			From:      sessionrt.SystemActorID,
			Type:      sessionrt.EventControl,
			Payload: sessionrt.ControlPayload{
				Action: sessionrt.ControlActionSessionCompleted,
			},
			Seq:       3,
			Timestamp: now,
		},
	}

	for _, event := range events {
		if err := publisher.PublishEvent(context.Background(), event); err != nil {
			t.Fatalf("PublishEvent() error: %v", err)
		}
	}

	stored, err := manager.Retrieve(context.Background(), memory.MemoryQuery{Limit: 50})
	if err != nil {
		t.Fatalf("Retrieve() error: %v", err)
	}
	if len(stored) == 0 {
		t.Fatalf("expected flushed memory records")
	}
}

func TestSessionPublisherFlushSession(t *testing.T) {
	manager := &recordingManager{}
	publisher := NewSessionPublisher(SessionPublisherOptions{Manager: manager, FlushEvery: 1000})
	now := time.Now().UTC()

	if err := publisher.PublishEvent(context.Background(), sessionrt.Event{
		SessionID: "sess-flush",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "remember: use tests"},
		Seq:       1,
		Timestamp: now,
	}); err != nil {
		t.Fatalf("PublishEvent() error: %v", err)
	}
	if err := publisher.FlushSession(context.Background(), "sess-flush"); err != nil {
		t.Fatalf("FlushSession() error: %v", err)
	}
	stored, _ := manager.Retrieve(context.Background(), memory.MemoryQuery{Limit: 10})
	if len(stored) == 0 {
		t.Fatalf("expected memory records after explicit flush")
	}
}

func TestSessionPublisherOnStoreErrorCallback(t *testing.T) {
	manager := &recordingManager{storeErr: errors.New("write failed")}
	var called bool
	publisher := NewSessionPublisher(SessionPublisherOptions{
		Manager:    manager,
		FlushEvery: 1,
		OnStoreError: func(_ context.Context, _ sessionrt.Event, _ memory.MemoryRecord, err error) {
			if err != nil {
				called = true
			}
		},
	})
	now := time.Now().UTC()
	if err := publisher.PublishEvent(context.Background(), sessionrt.Event{
		SessionID: "sess-error",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "remember: callback"},
		Seq:       1,
		Timestamp: now,
	}); err != nil {
		t.Fatalf("PublishEvent() error: %v", err)
	}
	if !called {
		t.Fatalf("expected OnStoreError callback to run")
	}
}
