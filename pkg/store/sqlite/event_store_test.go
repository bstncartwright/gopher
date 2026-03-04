package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestEventStorePersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "events.db")
	store, err := NewEventStore(EventStoreOptions{Path: dbPath})
	if err != nil {
		t.Fatalf("NewEventStore() error: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if err := store.Append(context.Background(), sessionrt.Event{
		ID:        "s1-000001",
		SessionID: "s1",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCreated},
		Timestamp: now,
		Seq:       1,
	}); err != nil {
		t.Fatalf("Append(created) error: %v", err)
	}
	if err := store.Append(context.Background(), sessionrt.Event{
		ID:        "s1-000002",
		SessionID: "s1",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "hello"},
		Timestamp: now.Add(time.Second),
		Seq:       2,
	}); err != nil {
		t.Fatalf("Append(message) error: %v", err)
	}

	record, err := store.GetSessionRecord(context.Background(), "s1")
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	if record.LastSeq != 2 {
		t.Fatalf("expected last seq 2, got %d", record.LastSeq)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	reopened, err := NewEventStore(EventStoreOptions{Path: dbPath})
	if err != nil {
		t.Fatalf("NewEventStore(reopen) error: %v", err)
	}
	defer reopened.Close()

	events, err := reopened.List(context.Background(), "s1")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events after reopen, got %d", len(events))
	}

	records, err := reopened.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(records) != 1 || records[0].LastSeq != 2 {
		t.Fatalf("expected session registry with last seq 2")
	}
}

func TestEventStoreIdempotentAndMonotonic(t *testing.T) {
	store, err := NewEventStore(EventStoreOptions{Path: filepath.Join(t.TempDir(), "events.db")})
	if err != nil {
		t.Fatalf("NewEventStore() error: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	first := sessionrt.Event{
		ID:        "s1-000001",
		SessionID: "s1",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCreated},
		Timestamp: now,
		Seq:       1,
	}
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatalf("Append(first) error: %v", err)
	}
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatalf("Append(duplicate) should be idempotent, got %v", err)
	}

	gap := sessionrt.Event{
		ID:        "s1-000003",
		SessionID: "s1",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "gap"},
		Timestamp: now.Add(time.Second),
		Seq:       3,
	}
	if err := store.Append(context.Background(), gap); err == nil {
		t.Fatalf("expected sequence gap append to fail")
	}

	events, err := store.List(context.Background(), "s1")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected only one persisted event, got %d", len(events))
	}
}

func TestEventStoreStreamAndSessionUpsert(t *testing.T) {
	store, err := NewEventStore(EventStoreOptions{Path: filepath.Join(t.TempDir(), "events.db"), StreamBuffer: 1})
	if err != nil {
		t.Fatalf("NewEventStore() error: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	err = store.Append(ctx, sessionrt.Event{
		ID:        "s1-000001",
		SessionID: "s1",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCreated},
		Timestamp: time.Now().UTC(),
		Seq:       1,
	})
	if err != nil {
		t.Fatalf("seed append error: %v", err)
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := store.Stream(streamCtx, "s1")
	if err != nil {
		t.Fatalf("Stream() error: %v", err)
	}

	err = store.Append(ctx, sessionrt.Event{
		ID:        "s1-000002",
		SessionID: "s1",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "hello"},
		Timestamp: time.Now().UTC(),
		Seq:       2,
	})
	if err != nil {
		t.Fatalf("append(stream event) error: %v", err)
	}

	select {
	case event, ok := <-ch:
		if !ok {
			t.Fatalf("expected stream event, channel closed")
		}
		if event.Seq != 2 {
			t.Fatalf("expected stream seq 2, got %d", event.Seq)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for stream event")
	}

	record, err := store.GetSessionRecord(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	record.InFlight = true
	if err := store.UpsertSession(ctx, record); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}
	updated, err := store.GetSessionRecord(ctx, "s1")
	if err != nil {
		t.Fatalf("GetSessionRecord(updated) error: %v", err)
	}
	if !updated.InFlight {
		t.Fatalf("expected in_flight flag to persist")
	}

	_, err = store.GetSessionRecord(ctx, "missing")
	if !errors.Is(err, sessionrt.ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound for missing session, got %v", err)
	}
}

func TestEventStoreStreamBackpressureKeepsLatestEvents(t *testing.T) {
	store, err := NewEventStore(EventStoreOptions{
		Path:         filepath.Join(t.TempDir(), "events.db"),
		StreamBuffer: 1,
	})
	if err != nil {
		t.Fatalf("NewEventStore() error: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if err := store.Append(context.Background(), sessionrt.Event{
		ID:        "s1-000001",
		SessionID: "s1",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCreated},
		Timestamp: now,
		Seq:       1,
	}); err != nil {
		t.Fatalf("seed append error: %v", err)
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
		seq := uint64(i + 2)
		if err := store.Append(context.Background(), sessionrt.Event{
			ID:        sessionrt.EventID(fmt.Sprintf("s1-%06d", seq)),
			SessionID: "s1",
			From:      "user:me",
			Type:      sessionrt.EventMessage,
			Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "m"},
			Timestamp: now.Add(time.Duration(i+1) * time.Second),
			Seq:       seq,
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

	firstSlow, ok := <-slowCh
	if !ok {
		t.Fatalf("expected slow stream to remain open")
	}
	if firstSlow.Seq != 4 {
		t.Fatalf("first slow event seq = %d, want 4", firstSlow.Seq)
	}

	if err := store.Append(context.Background(), sessionrt.Event{
		ID:        "s1-000005",
		SessionID: "s1",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "m"},
		Timestamp: now.Add(4 * time.Second),
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

func TestEventStoreExtractsDisplayNameFromCreatedEvent(t *testing.T) {
	store, err := NewEventStore(EventStoreOptions{Path: filepath.Join(t.TempDir(), "events.db")})
	if err != nil {
		t.Fatalf("NewEventStore() error: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	err = store.Append(context.Background(), sessionrt.Event{
		ID:        "s2-000001",
		SessionID: "s2",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload: sessionrt.ControlPayload{
			Action: sessionrt.ControlActionSessionCreated,
			Metadata: map[string]any{
				"display_name": "Ops Room",
			},
		},
		Timestamp: now,
		Seq:       1,
	})
	if err != nil {
		t.Fatalf("Append(created) error: %v", err)
	}

	record, err := store.GetSessionRecord(context.Background(), "s2")
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	if record.DisplayName != "Ops Room" {
		t.Fatalf("display_name = %q, want Ops Room", record.DisplayName)
	}
}
