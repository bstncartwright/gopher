package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestFileEventStorePersistsEventsAndSessionRegistry(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileEventStore(FileEventStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}

	now := time.Now().UTC()
	first := sessionrt.Event{
		ID:        "s1-000001",
		SessionID: "s1",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload: sessionrt.ControlPayload{
			Action: sessionrt.ControlActionSessionCreated,
		},
		Timestamp: now,
		Seq:       1,
	}
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatalf("Append(first) error: %v", err)
	}

	second := sessionrt.Event{
		ID:        "s1-000002",
		SessionID: "s1",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "hello"},
		Timestamp: now.Add(time.Second),
		Seq:       2,
	}
	if err := store.Append(context.Background(), second); err != nil {
		t.Fatalf("Append(second) error: %v", err)
	}

	events, err := store.List(context.Background(), "s1")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	records, err := store.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 session record, got %d", len(records))
	}
	if records[0].LastSeq != 2 {
		t.Fatalf("expected last seq 2, got %d", records[0].LastSeq)
	}
	if records[0].Status != sessionrt.SessionActive {
		t.Fatalf("expected active status, got %v", records[0].Status)
	}

	reopened, err := NewFileEventStore(FileEventStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("NewFileEventStore(reopen) error: %v", err)
	}
	reopenedEvents, err := reopened.List(context.Background(), "s1")
	if err != nil {
		t.Fatalf("reopened List() error: %v", err)
	}
	if len(reopenedEvents) != len(events) {
		t.Fatalf("expected %d reopened events, got %d", len(events), len(reopenedEvents))
	}
	reopenedRecords, err := reopened.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("reopened ListSessions() error: %v", err)
	}
	if len(reopenedRecords) != 1 || reopenedRecords[0].LastSeq != 2 {
		t.Fatalf("expected persisted session record with last seq 2")
	}
}

func TestFileEventStoreIdempotentDuplicateAndSequenceGap(t *testing.T) {
	store, err := NewFileEventStore(FileEventStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}

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
		t.Fatalf("Append(duplicate) should be idempotent, got: %v", err)
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
		t.Fatalf("expected only 1 persisted event, got %d", len(events))
	}
}

func TestFileEventStoreListSessionsWithLargeEventLine(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileEventStore(FileEventStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}

	now := time.Now().UTC()
	first := sessionrt.Event{
		ID:        "large-000001",
		SessionID: "large-session",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCreated},
		Timestamp: now,
		Seq:       1,
	}
	if err := store.Append(context.Background(), first); err != nil {
		t.Fatalf("Append(first) error: %v", err)
	}

	second := sessionrt.Event{
		ID:        "large-000002",
		SessionID: "large-session",
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: strings.Repeat("x", 5*1024*1024),
		},
		Timestamp: now.Add(time.Second),
		Seq:       2,
	}
	if err := store.Append(context.Background(), second); err != nil {
		t.Fatalf("Append(second) error: %v", err)
	}

	reopened, err := NewFileEventStore(FileEventStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("NewFileEventStore(reopen) error: %v", err)
	}

	records, err := reopened.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 session record, got %d", len(records))
	}
	if records[0].LastSeq != 2 {
		t.Fatalf("expected last seq 2, got %d", records[0].LastSeq)
	}
}

func TestFileEventStoreExtractsDisplayNameFromCreatedEvent(t *testing.T) {
	store, err := NewFileEventStore(FileEventStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}

	now := time.Now().UTC()
	if err := store.Append(context.Background(), sessionrt.Event{
		ID:        "s2-000001",
		SessionID: "s2",
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload: sessionrt.ControlPayload{
			Action: sessionrt.ControlActionSessionCreated,
			Metadata: map[string]any{
				"display_name": "Planning Room",
			},
		},
		Timestamp: now,
		Seq:       1,
	}); err != nil {
		t.Fatalf("Append(created) error: %v", err)
	}

	records, err := store.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 session record, got %d", len(records))
	}
	if records[0].DisplayName != "Planning Room" {
		t.Fatalf("display_name = %q, want Planning Room", records[0].DisplayName)
	}
}

func TestFileEventStoreLoadsLegacySessionRegistryWithoutResumeFields(t *testing.T) {
	dir := t.TempDir()
	legacy := `[{"session_id":"sess-legacy","display_name":"Legacy","status":0,"created_at":"2026-02-17T10:00:00Z","updated_at":"2026-02-17T10:05:00Z","last_seq":3,"in_flight":true}]`
	if err := os.WriteFile(filepath.Join(dir, sessionsRegistryFilename), []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	store, err := NewFileEventStore(FileEventStoreOptions{Dir: dir})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}
	record, err := store.GetSessionRecord(context.Background(), "sess-legacy")
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	if !record.InFlight {
		t.Fatalf("expected legacy in_flight=true")
	}
	if record.PendingResume || record.ResumeTriggerSeq != 0 || len(record.ResumeActorIDs) != 0 || record.ResumeRecordedAt != nil || record.ResumeEnqueuedAt != nil {
		t.Fatalf("expected legacy resume fields to default empty, got %#v", record)
	}
}

func TestFileEventStoreStreamBackpressureKeepsLatestEvents(t *testing.T) {
	store, err := NewFileEventStore(FileEventStoreOptions{Dir: t.TempDir(), StreamBuffer: 1})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}

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
