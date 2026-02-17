package retrieval

import (
	"context"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
)

type candidateStore struct {
	records []memory.MemoryRecord
}

func (s *candidateStore) List(_ context.Context, _ memory.MemoryQuery) ([]memory.MemoryRecord, error) {
	out := make([]memory.MemoryRecord, len(s.records))
	copy(out, s.records)
	return out, nil
}

func TestHybridRetrieverRanksBySimilarityAndAffinity(t *testing.T) {
	now := time.Now().UTC()
	store := &candidateStore{records: []memory.MemoryRecord{
		{
			ID:         "a",
			Type:       memory.MemoryEpisodic,
			Scope:      memory.AgentScope("agent-1"),
			SessionID:  "session-1",
			AgentID:    "agent-1",
			Content:    "docker deploy workflow",
			Embedding:  []float32{1, 0},
			Importance: 0.7,
			Timestamp:  now,
		},
		{
			ID:         "b",
			Type:       memory.MemoryEpisodic,
			Scope:      memory.ScopeGlobal,
			SessionID:  "session-x",
			AgentID:    "",
			Content:    "unrelated note",
			Embedding:  []float32{0, 1},
			Importance: 0.9,
			Timestamp:  now,
		},
	}}

	retriever := NewHybridRetriever(HybridRetrieverOptions{
		Now: func() time.Time { return now },
	})
	out, err := retriever.Retrieve(context.Background(), store, memory.MemoryQuery{
		SessionID:      "session-1",
		AgentID:        "agent-1",
		Limit:          1,
		QueryEmbedding: []float32{1, 0},
		Keywords:       []string{"deploy"},
	})
	if err != nil {
		t.Fatalf("Retrieve() error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected one record, got %d", len(out))
	}
	if out[0].ID != "a" {
		t.Fatalf("expected record a, got %s", out[0].ID)
	}
}
