package gateway

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFileCronStorePersistsJobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")

	store, err := NewFileCronStore(path)
	if err != nil {
		t.Fatalf("NewFileCronStore() error: %v", err)
	}
	now := time.Now().UTC()
	next := now.Add(time.Minute)
	job := CronJob{
		ID:        "cron-1",
		SessionID: "sess-1",
		Message:   "ping",
		CronExpr:  "* * * * *",
		Timezone:  "UTC",
		Enabled:   true,
		CreatedBy: "agent:test",
		CreatedAt: now,
		UpdatedAt: now,
		NextRunAt: &next,
	}
	if err := store.Create(job); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	reloaded, err := NewFileCronStore(path)
	if err != nil {
		t.Fatalf("NewFileCronStore(reload) error: %v", err)
	}
	got, ok := reloaded.Get("cron-1")
	if !ok {
		t.Fatalf("expected persisted job")
	}
	if got.SessionID != "sess-1" || got.Message != "ping" {
		t.Fatalf("reloaded job mismatch: %#v", got)
	}
	if got.NextRunAt == nil || got.NextRunAt.IsZero() {
		t.Fatalf("expected next_run_at to persist")
	}
}

func TestFileCronStoreDeleteRollsBackWhenPersistFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")

	store, err := NewFileCronStore(path)
	if err != nil {
		t.Fatalf("NewFileCronStore() error: %v", err)
	}
	now := time.Now().UTC()
	next := now.Add(time.Minute)
	job := CronJob{
		ID:        "cron-rollback",
		SessionID: "sess-1",
		Message:   "ping",
		CronExpr:  "* * * * *",
		Timezone:  "UTC",
		Enabled:   true,
		CreatedBy: "agent:test",
		CreatedAt: now,
		UpdatedAt: now,
		NextRunAt: &next,
	}
	if err := store.Create(job); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	// Force persist failure by pointing at an invalid parent path.
	store.filePath = filepath.Join("/dev/null", "jobs.json")

	if ok := store.Delete("cron-rollback"); ok {
		t.Fatalf("Delete() = true, want false when persist fails")
	}
	if _, exists := store.Get("cron-rollback"); !exists {
		t.Fatalf("job should remain in memory after failed delete persist")
	}
}
