package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestControlActionApplierProcessesPendingActions(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	created, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{{ID: "milo", Type: sessionrt.ActorAgent}},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	applier := newControlActionApplier(manager, dataDir, nil)
	pendingDir := filepath.Join(dataDir, "control", "actions", "pending")
	if err := os.MkdirAll(pendingDir, 0o755); err != nil {
		t.Fatalf("mkdir pending dir: %v", err)
	}

	writePendingAction(t, filepath.Join(pendingDir, "01-pause.json"), controlAction{
		ID:        "a1",
		Type:      "pause_session",
		SessionID: string(created.ID),
	})
	applier.processPending(ctx)

	paused, err := manager.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSession(paused) error: %v", err)
	}
	if paused.Status != sessionrt.SessionPaused {
		t.Fatalf("session status = %v, want paused", paused.Status)
	}

	writePendingAction(t, filepath.Join(pendingDir, "02-resume.json"), controlAction{
		ID:        "a2",
		Type:      "resume_session",
		SessionID: string(created.ID),
	})
	applier.processPending(ctx)
	appliedPath := filepath.Join(dataDir, "control", "actions", "applied.jsonl")
	blob, err := os.ReadFile(appliedPath)
	if err != nil {
		t.Fatalf("read applied ledger: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(blob)), "\n")
	if len(lines) < 2 {
		t.Fatalf("applied ledger lines = %d, want at least 2", len(lines))
	}
	last := map[string]any{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last ledger line: %v", err)
	}
	if got, _ := last["action"].(string); got != "resume_session" {
		t.Fatalf("last action = %q, want resume_session", got)
	}
	if got, _ := last["replacement_session_id"].(string); strings.TrimSpace(got) == "" {
		t.Fatalf("expected replacement_session_id in resume ledger entry")
	}
}

func TestControlSessionWatcherBuildsSummaryAndDelegationIndex(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	waitingSession, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{{ID: "milo", Type: sessionrt.ActorAgent}},
	})
	if err != nil {
		t.Fatalf("CreateSession(waiting) error: %v", err)
	}
	if err := manager.SendEvent(ctx, sessionrt.Event{
		SessionID: waitingSession.ID,
		From:      "milo",
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: "[[WAITING_ON_HUMAN|retry or abort?]]",
		},
	}); err != nil {
		t.Fatalf("SendEvent(waiting) error: %v", err)
	}

	delegatedSession, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{{ID: "worker", Type: sessionrt.ActorAgent}},
	})
	if err != nil {
		t.Fatalf("CreateSession(delegated) error: %v", err)
	}

	delegationPath := filepath.Join(dataDir, "control", "delegations.jsonl")
	appendJSONLRecord(delegationPath, map[string]any{
		"delegation_id":     string(delegatedSession.ID),
		"source_session_id": string(waitingSession.ID),
		"target_agent_id":   "worker",
	})

	watcher := newControlSessionWatcher(store, dataDir, nil)
	if err := watcher.rebuildIndex(ctx); err != nil {
		t.Fatalf("rebuildIndex() error: %v", err)
	}

	indexPath := filepath.Join(dataDir, "control", "session_index.json")
	index := map[string]any{}
	readJSON(t, indexPath, &index)
	summary, _ := index["summary"].(map[string]any)
	if summary == nil {
		t.Fatalf("missing summary section")
	}
	if int(summary["waiting"].(float64)) < 1 {
		t.Fatalf("waiting summary count = %v, want >= 1", summary["waiting"])
	}
	if int(summary["delegated"].(float64)) < 1 {
		t.Fatalf("delegated summary count = %v, want >= 1", summary["delegated"])
	}
}

func TestControlSessionWatcherOmitsStaleSessionsByDefault(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	stale, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{{ID: "milo", Type: sessionrt.ActorAgent}},
	})
	if err != nil {
		t.Fatalf("CreateSession(stale) error: %v", err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := store.UpsertSession(ctx, sessionrt.SessionRecord{
		SessionID: stale.ID,
		Status:    sessionrt.SessionActive,
		CreatedAt: old.Add(-24 * time.Hour),
		UpdatedAt: old,
		LastSeq:   1,
	}); err != nil {
		t.Fatalf("UpsertSession(stale) error: %v", err)
	}

	watcher := newControlSessionWatcher(store, dataDir, nil)
	if err := watcher.rebuildIndex(ctx); err != nil {
		t.Fatalf("rebuildIndex() error: %v", err)
	}

	indexPath := filepath.Join(dataDir, "control", "session_index.json")
	index := map[string]any{}
	readJSON(t, indexPath, &index)
	rawSessions, _ := index["sessions"].([]any)
	if len(rawSessions) != 0 {
		t.Fatalf("session index should omit stale sessions by default, got %d", len(rawSessions))
	}
	summary, _ := index["summary"].(map[string]any)
	if summary == nil {
		t.Fatalf("missing summary section")
	}
	if got := int(summary["active"].(float64)); got != 0 {
		t.Fatalf("active summary count = %d, want 0", got)
	}
}

func writePendingAction(t *testing.T, path string, action controlAction) {
	t.Helper()
	blob, err := json.Marshal(action)
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatalf("write action %s: %v", path, err)
	}
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(blob, out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
