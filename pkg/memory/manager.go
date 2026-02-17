package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type CandidateStore interface {
	List(ctx context.Context, query MemoryQuery) ([]MemoryRecord, error)
}

type Store interface {
	CandidateStore
	Store(ctx context.Context, record MemoryRecord) error
}

type Retriever interface {
	Retrieve(ctx context.Context, store CandidateStore, query MemoryQuery) ([]MemoryRecord, error)
}

type MemoryManager interface {
	Store(ctx context.Context, record MemoryRecord) error
	Retrieve(ctx context.Context, query MemoryQuery) ([]MemoryRecord, error)
}

type ManagerOptions struct {
	Store     Store
	Retriever Retriever
	Embedder  Embedder
	Now       func() time.Time
	FailOpen  bool
}

type Manager struct {
	store     Store
	retriever Retriever
	embedder  Embedder
	now       func() time.Time
	failOpen  bool
	counter   atomic.Uint64
}

var _ MemoryManager = (*Manager)(nil)

func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Store == nil {
		return nil, fmt.Errorf("memory store is required")
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Manager{
		store:     opts.Store,
		retriever: opts.Retriever,
		embedder:  opts.Embedder,
		now:       nowFn,
		failOpen:  opts.FailOpen,
	}, nil
}

func (m *Manager) Store(ctx context.Context, record MemoryRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	record = NormalizeRecord(record, m.now().UTC())
	if record.ID == "" {
		record.ID = m.newRecordID(record.Timestamp)
	}

	if len(record.Embedding) == 0 && m.embedder != nil && strings.TrimSpace(record.Content) != "" {
		embedding, err := m.embedder.Embed(ctx, record.Content)
		if err != nil {
			if m.failOpen {
				return nil
			}
			return fmt.Errorf("embed memory content: %w", err)
		}
		record.Embedding = embedding
	}

	if err := m.store.Store(ctx, record); err != nil {
		if m.failOpen {
			return nil
		}
		return err
	}
	return nil
}

func (m *Manager) Retrieve(ctx context.Context, query MemoryQuery) ([]MemoryRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query = NormalizeQuery(query)

	if len(query.QueryEmbedding) == 0 && m.embedder != nil {
		combined := strings.TrimSpace(strings.Join([]string{query.Topic, strings.Join(query.Keywords, " ")}, " "))
		if combined != "" {
			embedding, err := m.embedder.Embed(ctx, combined)
			if err == nil {
				query.QueryEmbedding = embedding
			} else if !m.failOpen {
				return nil, fmt.Errorf("embed retrieval query: %w", err)
			}
		}
	}

	if m.retriever != nil {
		records, err := m.retriever.Retrieve(ctx, m.store, query)
		if err != nil {
			if m.failOpen {
				return nil, nil
			}
			return nil, err
		}
		return cloneRecords(records, query.Limit), nil
	}

	records, err := m.store.List(ctx, query)
	if err != nil {
		if m.failOpen {
			return nil, nil
		}
		return nil, err
	}
	return cloneRecords(records, query.Limit), nil
}

func (m *Manager) newRecordID(ts time.Time) string {
	if ts.IsZero() {
		ts = m.now().UTC()
	}
	seq := m.counter.Add(1)
	return fmt.Sprintf("mem-%d-%d", ts.UnixNano(), seq)
}

func cloneRecords(in []MemoryRecord, limit int) []MemoryRecord {
	if len(in) == 0 {
		return nil
	}
	if limit <= 0 || limit > len(in) {
		limit = len(in)
	}
	out := make([]MemoryRecord, 0, limit)
	for i := 0; i < limit; i++ {
		record := in[i]
		record.Metadata = cloneStringMap(record.Metadata)
		record.Embedding = cloneEmbedding(record.Embedding)
		out = append(out, record)
	}
	return out
}

func SortByTimestampDesc(records []MemoryRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Timestamp.Equal(records[j].Timestamp) {
			return records[i].ID < records[j].ID
		}
		return records[i].Timestamp.After(records[j].Timestamp)
	})
}
