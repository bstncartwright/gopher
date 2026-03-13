package memory

import (
	"context"
	"fmt"
	"log/slog"
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
	Store            Store
	Retriever        Retriever
	Embedder         Embedder
	Now              func() time.Time
	FailOpenRetrieve bool
	FailOpenStore    bool
}

type Manager struct {
	store            Store
	retriever        Retriever
	embedder         Embedder
	now              func() time.Time
	failOpenRetrieve bool
	failOpenStore    bool
	counter          atomic.Uint64
}

var _ MemoryManager = (*Manager)(nil)

func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.Store == nil {
		slog.Error("memory_manager: memory store is required")
		return nil, fmt.Errorf("memory store is required")
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	manager := &Manager{
		store:            opts.Store,
		retriever:        opts.Retriever,
		embedder:         opts.Embedder,
		now:              nowFn,
		failOpenRetrieve: opts.FailOpenRetrieve,
		failOpenStore:    opts.FailOpenStore,
	}
	slog.Info("memory_manager: initialized", "retriever_enabled", opts.Retriever != nil, "embedder_enabled", opts.Embedder != nil, "fail_open_store", opts.FailOpenStore, "fail_open_retrieve", opts.FailOpenRetrieve)
	return manager, nil
}

func (m *Manager) Store(ctx context.Context, record MemoryRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	record = NormalizeRecord(record, m.now().UTC())
	if record.ID == "" {
		record.ID = m.newRecordID(record.Timestamp)
	}
	slog.Debug("memory_manager: storing record", "record_id", record.ID, "scope", record.Scope, "has_embedding", len(record.Embedding) > 0, "content_len", len(strings.TrimSpace(record.Content)))

	if len(record.Embedding) == 0 && m.embedder != nil && strings.TrimSpace(record.Content) != "" {
		embedding, err := m.embedder.Embed(ctx, record.Content)
		if err != nil {
			if m.failOpenStore {
				slog.Warn("memory_manager: embedding failed; failing open on store", "record_id", record.ID, "error", err)
				return nil
			}
			slog.Error("memory_manager: failed to embed record content", "record_id", record.ID, "error", err)
			return fmt.Errorf("embed memory content: %w", err)
		}
		record.Embedding = embedding
		slog.Debug("memory_manager: embedded record content", "record_id", record.ID, "embedding_dims", len(embedding))
	}

	if err := m.store.Store(ctx, record); err != nil {
		if m.failOpenStore {
			slog.Warn("memory_manager: store failed; failing open", "record_id", record.ID, "error", err)
			return nil
		}
		slog.Error("memory_manager: failed to persist record", "record_id", record.ID, "error", err)
		return err
	}
	slog.Info("memory_manager: stored record", "record_id", record.ID, "scope", record.Scope)
	return nil
}

func (m *Manager) Retrieve(ctx context.Context, query MemoryQuery) ([]MemoryRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query = NormalizeQuery(query)
	slog.Debug("memory_manager: retrieving records", "session_id", query.SessionID, "agent_id", query.AgentID, "scopes", len(query.Scopes), "limit", query.Limit, "keywords", len(query.Keywords), "has_query_embedding", len(query.QueryEmbedding) > 0)

	if len(query.QueryEmbedding) == 0 && m.embedder != nil {
		combined := strings.TrimSpace(strings.Join([]string{query.Topic, strings.Join(query.Keywords, " ")}, " "))
		if combined != "" {
			embedding, err := m.embedder.Embed(ctx, combined)
			if err == nil {
				query.QueryEmbedding = embedding
				slog.Debug("memory_manager: embedded retrieval query", "embedding_dims", len(embedding))
			} else if !m.failOpenRetrieve {
				slog.Error("memory_manager: failed to embed retrieval query", "error", err)
				return nil, fmt.Errorf("embed retrieval query: %w", err)
			}
		}
	}

	if m.retriever != nil {
		records, err := m.retriever.Retrieve(ctx, m.store, query)
		if err != nil {
			if m.failOpenRetrieve {
				slog.Warn("memory_manager: retrieval failed; failing open", "error", err)
				return nil, nil
			}
			slog.Error("memory_manager: retriever failed", "error", err)
			return nil, err
		}
		slog.Info("memory_manager: retrieved records via retriever", "count", len(records))
		return cloneRecords(records, query.Limit), nil
	}

	records, err := m.store.List(ctx, query)
	if err != nil {
		if m.failOpenRetrieve {
			slog.Warn("memory_manager: store list failed; failing open", "error", err)
			return nil, nil
		}
		slog.Error("memory_manager: failed to list records", "error", err)
		return nil, err
	}
	slog.Info("memory_manager: retrieved records via store", "count", len(records))
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
