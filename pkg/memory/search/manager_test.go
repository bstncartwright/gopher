package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
	"github.com/bstncartwright/gopher/pkg/memory/embedding"
	memfiles "github.com/bstncartwright/gopher/pkg/memory/files"
)

func TestSearchFallsBackToFTSWhenEmbeddingsUnavailable(t *testing.T) {
	workspace := t.TempDir()
	memoryDir := filepath.Join(workspace, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "2026-03-02.md"), []byte("# Daily Memory - 2026-03-02\n\n- Ran migration checks before deploy.\n"), 0o644); err != nil {
		t.Fatalf("write daily file: %v", err)
	}

	mgr, err := NewManager(ManagerOptions{
		Workspace:     workspace,
		DBPath:        filepath.Join(workspace, "memory", "memory.db"),
		Files:         memfiles.NewManager(workspace, time.UTC, nil),
		Provider:      embedding.New(embedding.Options{Name: "none"}),
		Enabled:       true,
		HybridEnabled: true,
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	defer mgr.Close()

	if err := mgr.Sync(context.Background(), true); err != nil {
		t.Fatalf("Sync() error: %v", err)
	}
	status, err := mgr.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error: %v", err)
	}
	if !status.FTSAvailable {
		t.Skip("fts unavailable in test environment")
	}

	resp, err := mgr.Search(context.Background(), memory.MemorySearchRequest{Query: "migration checks", MaxResults: 3})
	if err != nil {
		t.Fatalf("Search() error: %v", err)
	}
	if resp.Mode != "fts-only" {
		t.Fatalf("mode = %q, want fts-only", resp.Mode)
	}
	if len(resp.Results) == 0 {
		t.Fatalf("expected at least one result")
	}
}

func TestReadReturnsEmptyForNonMemoryPath(t *testing.T) {
	workspace := t.TempDir()
	mgr, err := NewManager(ManagerOptions{
		Workspace: workspace,
		DBPath:    filepath.Join(workspace, "memory", "memory.db"),
		Files:     memfiles.NewManager(workspace, time.UTC, nil),
		Provider:  embedding.New(embedding.Options{Name: "none"}),
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	defer mgr.Close()

	resp, err := mgr.Read(context.Background(), memory.MemoryReadRequest{Path: filepath.Join(workspace, "README.md"), From: 1, Lines: 10})
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if resp.Text != "" {
		t.Fatalf("expected empty text for non-memory path")
	}
}
