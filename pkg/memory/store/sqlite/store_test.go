package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
)

func TestStoreRoundTripAndFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := NewStore(StoreOptions{Path: path})
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	records := []memory.MemoryRecord{
		{
			ID:         "r1",
			Type:       memory.MemoryEpisodic,
			Scope:      memory.AgentScope("agent-1"),
			SessionID:  "s1",
			AgentID:    "agent-1",
			Content:    "deploy pipeline fixed",
			Metadata:   map[string]string{"kind": "session_summary"},
			Embedding:  []float32{1, 0, 0},
			Importance: 0.8,
			Timestamp:  now,
		},
		{
			ID:         "r2",
			Type:       memory.MemoryTool,
			Scope:      memory.AgentScope("agent-1"),
			SessionID:  "s2",
			AgentID:    "agent-1",
			Content:    "Tool go test finished with status=ok",
			Metadata:   map[string]string{"tool": "go test"},
			Embedding:  []float32{0, 1, 0},
			Importance: 0.6,
			Timestamp:  now.Add(-time.Minute),
		},
		{
			ID:         "r3",
			Type:       memory.MemorySemantic,
			Scope:      memory.ScopeGlobal,
			SessionID:  "",
			AgentID:    "",
			Content:    "Use UTC timestamps",
			Metadata:   map[string]string{"kind": "fact"},
			Embedding:  []float32{0, 0, 1},
			Importance: 0.9,
			Timestamp:  now.Add(-2 * time.Minute),
		},
	}
	for _, record := range records {
		if err := store.Store(context.Background(), record); err != nil {
			t.Fatalf("Store(%s) error: %v", record.ID, err)
		}
	}

	result, err := store.List(context.Background(), memory.MemoryQuery{
		AgentID:  "agent-1",
		Keywords: []string{"deploy"},
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(result) < 1 {
		t.Fatalf("expected at least one result, got %d", len(result))
	}
	if result[0].ID != "r1" {
		t.Fatalf("expected r1, got %s", result[0].ID)
	}

	result, err = store.List(context.Background(), memory.MemoryQuery{
		Types:  []memory.MemoryType{memory.MemorySemantic},
		Scopes: []memory.MemoryScope{memory.ScopeGlobal},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("List() semantic error: %v", err)
	}
	if len(result) != 1 || result[0].ID != "r3" {
		t.Fatalf("expected global semantic r3, got %#v", result)
	}
}
