package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bstncartwright/gopher/pkg/memory"
)

type StoreOptions struct {
	Path           string
	CandidateLimit int
}

type Store struct {
	db             *sql.DB
	candidateLimit int
}

var _ memory.Store = (*Store)(nil)

func NewStore(opts StoreOptions) (*Store, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if err := ensureParentDir(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db, candidateLimit: opts.CandidateLimit}
	if store.candidateLimit <= 0 {
		store.candidateLimit = 64
	}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Store(ctx context.Context, record memory.MemoryRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	now := time.Now().UTC()
	record = memory.NormalizeRecord(record, now)
	if record.ID == "" {
		return fmt.Errorf("memory record ID is required")
	}

	metadata := record.Metadata
	if metadata == nil {
		metadata = map[string]string{}
	}
	embedding := record.Embedding
	if embedding == nil {
		embedding = []float32{}
	}

	metadataBlob, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshal memory metadata: %w", err)
	}
	embeddingBlob, err := json.Marshal(embedding)
	if err != nil {
		return fmt.Errorf("marshal memory embedding: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_records (
			id, type, scope, session_id, agent_id, content, metadata, embedding, importance, timestamp
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id)
		DO UPDATE SET
			type = excluded.type,
			scope = excluded.scope,
			session_id = excluded.session_id,
			agent_id = excluded.agent_id,
			content = excluded.content,
			metadata = excluded.metadata,
			embedding = excluded.embedding,
			importance = excluded.importance,
			timestamp = excluded.timestamp
	`,
		record.ID,
		int(record.Type),
		string(record.Scope),
		record.SessionID,
		record.AgentID,
		record.Content,
		metadataBlob,
		embeddingBlob,
		record.Importance,
		record.Timestamp.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("upsert memory record: %w", err)
	}
	return nil
}

func (s *Store) List(ctx context.Context, query memory.MemoryQuery) ([]memory.MemoryRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	query = memory.NormalizeQuery(query)
	limit := s.candidateLimit
	if query.Limit > 0 {
		candidateLimit := query.Limit * 8
		if candidateLimit > limit {
			limit = candidateLimit
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 1024 {
		limit = 1024
	}

	clauses := make([]string, 0, 8)
	args := make([]any, 0, 16)

	if len(query.Types) > 0 {
		clauses = append(clauses, "type IN ("+placeholders(len(query.Types))+")")
		for _, t := range query.Types {
			args = append(args, int(t))
		}
	}
	if len(query.Scopes) > 0 {
		clauses = append(clauses, "scope IN ("+placeholders(len(query.Scopes))+")")
		for _, scope := range query.Scopes {
			args = append(args, string(scope))
		}
	}
	if query.TimeRange != nil {
		if !query.TimeRange.Start.IsZero() {
			clauses = append(clauses, "timestamp >= ?")
			args = append(args, query.TimeRange.Start.UTC().UnixMilli())
		}
		if !query.TimeRange.End.IsZero() {
			clauses = append(clauses, "timestamp <= ?")
			args = append(args, query.TimeRange.End.UTC().UnixMilli())
		}
	}
	if query.AgentID != "" {
		clauses = append(clauses, "(agent_id = ? OR agent_id = '')")
		args = append(args, query.AgentID)
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}

	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, type, scope, session_id, agent_id, content, metadata, embedding, importance, timestamp
		FROM memory_records
		`+where+`
		ORDER BY timestamp DESC, id ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query memory records: %w", err)
	}
	defer rows.Close()

	records := make([]memory.MemoryRecord, 0, limit)
	for rows.Next() {
		var (
			record         memory.MemoryRecord
			typeInt        int
			metadataBlob   []byte
			embeddingBlob  []byte
			timestampMilli int64
		)
		if err := rows.Scan(
			&record.ID,
			&typeInt,
			&record.Scope,
			&record.SessionID,
			&record.AgentID,
			&record.Content,
			&metadataBlob,
			&embeddingBlob,
			&record.Importance,
			&timestampMilli,
		); err != nil {
			return nil, fmt.Errorf("scan memory row: %w", err)
		}
		record.Type = memory.MemoryType(typeInt)
		record.Timestamp = time.UnixMilli(timestampMilli).UTC()
		record.Metadata = map[string]string{}
		if len(metadataBlob) > 0 {
			if err := json.Unmarshal(metadataBlob, &record.Metadata); err != nil {
				return nil, fmt.Errorf("decode memory metadata: %w", err)
			}
		}
		record.Embedding = nil
		if len(embeddingBlob) > 0 {
			if err := json.Unmarshal(embeddingBlob, &record.Embedding); err != nil {
				return nil, fmt.Errorf("decode memory embedding: %w", err)
			}
		}
		record = memory.NormalizeRecord(record, record.Timestamp)
		if query.TimeRange != nil && !query.TimeRange.Contains(record.Timestamp) {
			continue
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate memory rows: %w", err)
	}
	return records, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memory_records (
			id TEXT PRIMARY KEY,
			type INTEGER NOT NULL,
			scope TEXT NOT NULL,
			session_id TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			content TEXT NOT NULL,
			metadata BLOB NOT NULL,
			embedding BLOB NOT NULL,
			importance REAL NOT NULL,
			timestamp INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create memory_records table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_records_timestamp
		ON memory_records(timestamp DESC)
	`); err != nil {
		return fmt.Errorf("create timestamp index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_records_scope
		ON memory_records(scope, timestamp DESC)
	`); err != nil {
		return fmt.Errorf("create scope index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_records_agent
		ON memory_records(agent_id, timestamp DESC)
	`); err != nil {
		return fmt.Errorf("create agent index: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_records_type
		ON memory_records(type, timestamp DESC)
	`); err != nil {
		return fmt.Errorf("create type index: %w", err)
	}
	return nil
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	parts := make([]string, count)
	for i := 0; i < count; i++ {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func ensureParentDir(path string) error {
	if path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil
	}
	dir := filepath.Dir(path)
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sqlite directory %s: %w", dir, err)
	}
	return nil
}
