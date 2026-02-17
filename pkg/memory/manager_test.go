package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

type inMemoryStore struct {
	records []MemoryRecord
	listErr error
}

func (s *inMemoryStore) Store(_ context.Context, record MemoryRecord) error {
	s.records = append(s.records, record)
	return nil
}

func (s *inMemoryStore) List(_ context.Context, query MemoryQuery) ([]MemoryRecord, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	query = NormalizeQuery(query)
	if query.Limit > len(s.records) {
		query.Limit = len(s.records)
	}
	out := make([]MemoryRecord, 0, query.Limit)
	for i := 0; i < query.Limit; i++ {
		out = append(out, s.records[i])
	}
	return out, nil
}

func TestManagerStoreAutoIDsAndEmbeddings(t *testing.T) {
	store := &inMemoryStore{}
	manager, err := NewManager(ManagerOptions{
		Store:    store,
		Embedder: NewHashEmbedder(16),
		Now: func() time.Time {
			return time.Unix(1700000000, 0).UTC()
		},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	err = manager.Store(context.Background(), MemoryRecord{
		Type:    MemoryEpisodic,
		Content: "user asked for deploy steps",
	})
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}

	if len(store.records) != 1 {
		t.Fatalf("expected one stored record, got %d", len(store.records))
	}
	record := store.records[0]
	if record.ID == "" {
		t.Fatalf("expected generated record ID")
	}
	if record.Timestamp.IsZero() {
		t.Fatalf("expected generated timestamp")
	}
	if len(record.Embedding) == 0 {
		t.Fatalf("expected embedded content")
	}
}

func TestManagerRetrieveFailOpen(t *testing.T) {
	store := &inMemoryStore{listErr: errors.New("db down")}
	manager, err := NewManager(ManagerOptions{Store: store, FailOpen: true})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	records, err := manager.Retrieve(context.Background(), MemoryQuery{Limit: 5})
	if err != nil {
		t.Fatalf("Retrieve() error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected empty records on fail-open, got %d", len(records))
	}
}
