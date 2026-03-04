package session

import (
	"context"
	"testing"
	"time"
)

func TestInMemoryEventStoreListReturnsCopy(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{})
	event := Event{
		ID:        "e1",
		SessionID: "s1",
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "hello"},
		Timestamp: time.Now().UTC(),
		Seq:       1,
	}
	if err := store.Append(context.Background(), event); err != nil {
		t.Fatalf("Append() error: %v", err)
	}

	listOne, err := store.List(context.Background(), "s1")
	if err != nil {
		t.Fatalf("List() first error: %v", err)
	}
	if len(listOne) != 1 {
		t.Fatalf("expected one event, got %d", len(listOne))
	}
	listOne[0].Type = EventControl

	listTwo, err := store.List(context.Background(), "s1")
	if err != nil {
		t.Fatalf("List() second error: %v", err)
	}
	if listTwo[0].Type != EventMessage {
		t.Fatalf("expected original event type to remain message, got %q", listTwo[0].Type)
	}
}

func TestInMemoryEventStoreStreamBackpressureKeepsLatestEvents(t *testing.T) {
	store := NewInMemoryEventStore(InMemoryEventStoreOptions{StreamBuffer: 1})
	if err := store.Append(context.Background(), Event{
		ID:        "seed",
		SessionID: "s1",
		From:      "system",
		Type:      EventControl,
		Payload:   ControlPayload{Action: ControlActionSessionCreated},
		Timestamp: time.Now().UTC(),
		Seq:       1,
	}); err != nil {
		t.Fatalf("seed Append() error: %v", err)
	}

	ctxFast, cancelFast := context.WithCancel(context.Background())
	defer cancelFast()
	fastCh, err := store.Stream(ctxFast, "s1")
	if err != nil {
		t.Fatalf("Stream(fast) error: %v", err)
	}

	ctxSlow, cancelSlow := context.WithCancel(context.Background())
	defer cancelSlow()
	slowCh, err := store.Stream(ctxSlow, "s1")
	if err != nil {
		t.Fatalf("Stream(slow) error: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := store.Append(context.Background(), Event{
			ID:        EventID("e" + string(rune('2'+i))),
			SessionID: "s1",
			From:      "user:me",
			Type:      EventMessage,
			Payload:   Message{Role: RoleUser, Content: "m"},
			Timestamp: time.Now().UTC(),
			Seq:       uint64(i + 2),
		}); err != nil {
			t.Fatalf("Append(%d) error: %v", i, err)
		}
		select {
		case _, ok := <-fastCh:
			if !ok {
				t.Fatalf("fast stream closed unexpectedly")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for fast stream event %d", i)
		}
	}

	// Slow stream should stay connected and retain the latest buffered event.
	firstSlow, ok := <-slowCh
	if !ok {
		t.Fatalf("expected slow stream to remain open")
	}
	if firstSlow.Seq != 4 {
		t.Fatalf("first slow event seq = %d, want 4 (latest buffered event)", firstSlow.Seq)
	}

	if err := store.Append(context.Background(), Event{
		ID:        "e5",
		SessionID: "s1",
		From:      "user:me",
		Type:      EventMessage,
		Payload:   Message{Role: RoleUser, Content: "m"},
		Timestamp: time.Now().UTC(),
		Seq:       5,
	}); err != nil {
		t.Fatalf("Append(4) error: %v", err)
	}
	select {
	case _, ok := <-fastCh:
		if !ok {
			t.Fatalf("fast stream closed unexpectedly on fourth event")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for fast stream event 4")
	}
	select {
	case nextSlow, ok := <-slowCh:
		if !ok {
			t.Fatalf("expected slow stream to remain open after backpressure")
		}
		if nextSlow.Seq != 5 {
			t.Fatalf("next slow event seq = %d, want 5", nextSlow.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for next slow stream event")
	}

}
