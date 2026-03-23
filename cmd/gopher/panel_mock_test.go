package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildMockPanelFixtureSeedsData(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "panel-mock")

	fixture, err := buildMockPanelFixture("127.0.0.1:39555", dataDir, "")
	if err != nil {
		t.Fatalf("buildMockPanelFixture() error: %v", err)
	}
	defer fixture.cleanup()

	if fixture.server == nil {
		t.Fatalf("expected mock panel server")
	}
	if fixture.listenAddr != "127.0.0.1:39555" {
		t.Fatalf("listen addr = %q, want %q", fixture.listenAddr, "127.0.0.1:39555")
	}
	if _, err := os.Stat(filepath.Join(fixture.controlDir, "session_index.json")); err != nil {
		t.Fatalf("stat control index: %v", err)
	}
	if _, err := os.Stat(fixture.cronStorePath); err != nil {
		t.Fatalf("stat cron store: %v", err)
	}

	records, err := fixture.store.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(records) < 8 {
		t.Fatalf("expected seeded mock sessions, got %d", len(records))
	}

	found := false
	for _, record := range records {
		if string(record.SessionID) == "sess-control-plane-spike" {
			found = true
			if !record.InFlight {
				t.Fatalf("expected sess-control-plane-spike to be marked in flight")
			}
		}
	}
	if !found {
		t.Fatalf("expected seeded work session in registry")
	}
}
