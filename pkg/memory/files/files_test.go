package files

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendDailyEntryAndSafeReadWindow(t *testing.T) {
	workspace := t.TempDir()
	fixedNow := time.Date(2026, 3, 3, 9, 30, 0, 0, time.UTC)
	manager := NewManager(workspace, time.UTC, func() time.Time { return fixedNow })

	window, err := manager.AppendDailyEntry("Task: test\nOutcome: done")
	if err != nil {
		t.Fatalf("AppendDailyEntry() error: %v", err)
	}
	if window.Path == "" {
		t.Fatalf("expected written path")
	}
	read, err := manager.SafeReadWindow(window.Path, window.StartLine, 4)
	if err != nil {
		t.Fatalf("SafeReadWindow() error: %v", err)
	}
	if read.Text == "" {
		t.Fatalf("expected non-empty read text")
	}
}

func TestResolveMemoryPathRestrictsScope(t *testing.T) {
	workspace := t.TempDir()
	manager := NewManager(workspace, time.UTC, nil)

	if _, ok := manager.ResolveMemoryPath(filepath.Join(workspace, "notes.txt")); ok {
		t.Fatalf("expected notes.txt to be rejected")
	}
	if _, ok := manager.ResolveMemoryPath(filepath.Join(workspace, "MEMORY.md")); !ok {
		t.Fatalf("expected MEMORY.md to be allowed")
	}
	if _, ok := manager.ResolveMemoryPath(filepath.Join(workspace, "memory", "2026-03-03.md")); !ok {
		t.Fatalf("expected daily memory file to be allowed")
	}
}
