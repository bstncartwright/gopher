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

func TestInMemoryEventStoreStreamDisconnectsSlowSubscriber(t *testing.T) {
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

	// Slow stream should have been disconnected once its channel filled.
	firstSlow, ok := <-slowCh
	if !ok {
		t.Fatalf("expected slow stream to still have buffered event before close")
	}
	if firstSlow.SessionID != "s1" {
		t.Fatalf("unexpected slow stream event session ID %q", firstSlow.SessionID)
	}
	_, stillOpen := <-slowCh
	if stillOpen {
		t.Fatalf("expected slow stream channel to close after backpressure")
	}

}
