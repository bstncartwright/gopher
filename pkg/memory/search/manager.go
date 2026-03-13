package search

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bstncartwright/gopher/pkg/memory"
	"github.com/bstncartwright/gopher/pkg/memory/embedding"
	"github.com/bstncartwright/gopher/pkg/memory/files"
	"github.com/bstncartwright/gopher/pkg/memory/queryexpansion"
)

const (
	defaultMaxResults          = 6
	defaultMinScore            = 0.35
	defaultVectorWeight        = 0.7
	defaultTextWeight          = 0.3
	defaultCandidateMultiplier = 4
	defaultMMRLambda           = 0.7
	defaultHalfLifeDays        = 30
	defaultChunkTokens         = 400
	defaultChunkOverlap        = 80
	defaultSyncInterval        = 30 * time.Second
)

type ManagerOptions struct {
	Workspace string
	DBPath    string
	Files     *files.Manager
	Provider  embedding.Provider

	Enabled bool
	Sources []string

	MaxResults int
	MinScore   float64

	HybridEnabled       bool
	VectorWeight        float64
	TextWeight          float64
	CandidateMultiplier int

	MMREnabled bool
	MMRLambda  float64

	TemporalDecayEnabled bool
	TemporalHalfLifeDays int
	ChunkTokens          int
	ChunkOverlap         int
	SyncInterval         time.Duration
}

type Manager struct {
	workspace string
	dbPath    string
	db        *sql.DB
	files     *files.Manager
	provider  embedding.Provider

	enabled bool
	sources map[string]struct{}

	maxResults int
	minScore   float64

	hybridEnabled       bool
	vectorWeight        float64
	textWeight          float64
	candidateMultiplier int

	mmrEnabled bool
	mmrLambda  float64

	temporalDecayEnabled bool
	halfLifeDays         int
	chunkTokens          int
	chunkOverlap         int
	syncInterval         time.Duration

	mu                sync.RWMutex
	lastSync          time.Time
	dirty             bool
	ftsAvailable      bool
	providerError     string
	fallbackReason    string
	unavailableReason string

	syncMu sync.Mutex
}

var _ memory.MemorySearchManager = (*Manager)(nil)

func NewManager(opts ManagerOptions) (*Manager, error) {
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace == "" {
		slog.Error("memory_search_manager: workspace is required")
		return nil, fmt.Errorf("workspace is required")
	}
	workspace = filepath.Clean(workspace)
	dbPath := strings.TrimSpace(opts.DBPath)
	if dbPath == "" {
		dbPath = filepath.Join(workspace, "memory", "memory.db")
	}
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(workspace, dbPath)
	}

	if opts.Files == nil {
		opts.Files = files.NewManager(workspace, nil, nil)
	}
	if opts.Provider == nil {
		opts.Provider = embedding.New(embedding.Options{Name: "none"})
	}

	m := &Manager{
		workspace:            workspace,
		dbPath:               dbPath,
		files:                opts.Files,
		provider:             opts.Provider,
		enabled:              opts.Enabled,
		sources:              normalizeSources(opts.Sources),
		maxResults:           valueInt(opts.MaxResults, defaultMaxResults),
		minScore:             valueFloat(opts.MinScore, defaultMinScore),
		hybridEnabled:        opts.HybridEnabled,
		vectorWeight:         valueFloat(opts.VectorWeight, defaultVectorWeight),
		textWeight:           valueFloat(opts.TextWeight, defaultTextWeight),
		candidateMultiplier:  valueInt(opts.CandidateMultiplier, defaultCandidateMultiplier),
		mmrEnabled:           opts.MMREnabled,
		mmrLambda:            valueFloat(opts.MMRLambda, defaultMMRLambda),
		temporalDecayEnabled: opts.TemporalDecayEnabled,
		halfLifeDays:         valueInt(opts.TemporalHalfLifeDays, defaultHalfLifeDays),
		chunkTokens:          valueInt(opts.ChunkTokens, defaultChunkTokens),
		chunkOverlap:         valueInt(opts.ChunkOverlap, defaultChunkOverlap),
		syncInterval:         opts.SyncInterval,
		dirty:                true,
	}
	if m.syncInterval <= 0 {
		m.syncInterval = defaultSyncInterval
	}
	if !m.enabled {
		slog.Info("memory_search_manager: initialized in disabled mode", "workspace", workspace, "db_path", dbPath)
		return m, nil
	}
	if err := os.MkdirAll(filepath.Dir(m.dbPath), 0o755); err != nil {
		slog.Error("memory_search_manager: failed to create db directory", "db_path", m.dbPath, "error", err)
		return nil, fmt.Errorf("create memory db directory: %w", err)
	}
	db, err := sql.Open("sqlite", m.dbPath)
	if err != nil {
		slog.Error("memory_search_manager: failed to open sqlite db", "db_path", m.dbPath, "error", err)
		return nil, fmt.Errorf("open memory sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := configureSQLiteConnection(context.Background(), db); err != nil {
		slog.Error("memory_search_manager: failed to configure sqlite connection", "db_path", m.dbPath, "error", err)
		_ = db.Close()
		return nil, err
	}
	m.db = db
	if err := m.initSchema(context.Background()); err != nil {
		slog.Error("memory_search_manager: failed to initialize schema", "db_path", m.dbPath, "error", err)
		_ = db.Close()
		return nil, err
	}
	if err := m.probeEmbedding(context.Background()); err != nil {
		m.providerError = strings.TrimSpace(err.Error())
		slog.Warn("memory_search_manager: embedding provider unavailable during init", "workspace", workspace, "error", err)
	}
	slog.Info("memory_search_manager: initialized", "workspace", workspace, "db_path", m.dbPath, "sources", len(m.sources), "hybrid_enabled", m.hybridEnabled)
	return m, nil
}

func (m *Manager) Close() error {
	if m == nil || m.db == nil {
		return nil
	}
	slog.Debug("memory_search_manager: closing database", "db_path", m.dbPath)
	return m.db.Close()
}

func (m *Manager) Search(ctx context.Context, req memory.MemorySearchRequest) (memory.MemorySearchResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !m.enabled {
		slog.Debug("memory_search_manager: search skipped because manager disabled", "workspace", m.workspace)
		return memory.MemorySearchResponse{Disabled: true, Mode: "disabled"}, nil
	}
	if err := m.Sync(ctx, false); err != nil {
		slog.Warn("memory_search_manager: sync failed before search", "workspace", m.workspace, "error", err)
		m.setFallbackReason("sync_failed: " + err.Error())
	}

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = m.maxResults
	}
	if maxResults > 64 {
		maxResults = 64
	}
	minScore := req.MinScore
	if minScore <= 0 {
		minScore = m.minScore
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		slog.Debug("memory_search_manager: empty query", "workspace", m.workspace)
		return memory.MemorySearchResponse{Mode: "fts-only", Results: nil}, nil
	}
	slog.Debug("memory_search_manager: executing search", "workspace", m.workspace, "query_len", len(query), "max_results", maxResults, "min_score", minScore)

	status, err := m.Status(ctx)
	if err != nil {
		slog.Error("memory_search_manager: failed to load status before search", "workspace", m.workspace, "error", err)
		return memory.MemorySearchResponse{}, err
	}

	mode := "fts-only"
	vectorCandidates := make([]scoredChunk, 0)
	queryVector := []float32(nil)
	vectorEnabled := m.hybridEnabled && status.EmbeddingAvailable
	if vectorEnabled {
		vectors, err := m.provider.EmbedBatch(ctx, []string{query})
		if err != nil || len(vectors) == 0 || len(vectors[0]) == 0 {
			vectorEnabled = false
			if err != nil {
				slog.Warn("memory_search_manager: query embedding unavailable", "workspace", m.workspace, "error", err)
				m.setFallbackReason("embedding_unavailable: " + err.Error())
			}
		} else {
			queryVector = vectors[0]
			slog.Debug("memory_search_manager: query embedding generated", "workspace", m.workspace, "embedding_dims", len(queryVector))
		}
	}
	if vectorEnabled {
		vectorCandidates, err = m.searchVector(ctx, queryVector, maxResults*m.candidateMultiplier)
		if err != nil {
			vectorCandidates = nil
			vectorEnabled = false
			slog.Warn("memory_search_manager: vector search failed", "workspace", m.workspace, "error", err)
			m.setFallbackReason("vector_search_failed: " + err.Error())
		}
	}

	textEnabled := status.FTSAvailable
	textCandidates := make([]scoredChunk, 0)
	if textEnabled {
		textCandidates, err = m.searchFTS(ctx, query, maxResults*m.candidateMultiplier)
		if err != nil {
			textCandidates = nil
			textEnabled = false
			slog.Warn("memory_search_manager: fts search failed", "workspace", m.workspace, "error", err)
			m.setFallbackReason("fts_search_failed: " + err.Error())
		}
	}

	if !vectorEnabled && !textEnabled {
		reason := "memory search unavailable: embeddings and fts are both unavailable"
		slog.Warn("memory_search_manager: search unavailable", "workspace", m.workspace, "reason", reason)
		m.setUnavailableReason(reason)
		status, _ = m.Status(ctx)
		return memory.MemorySearchResponse{
			Mode:              "unavailable",
			Provider:          status.Provider,
			Model:             status.Model,
			Unavailable:       true,
			Warning:           "memory search is unavailable",
			Action:            "run `gopher doctor memory` and `gopher memory index --force`",
			Error:             reason,
			UnavailableReason: reason,
		}, nil
	}

	if vectorEnabled && textEnabled {
		mode = "hybrid"
	} else if vectorEnabled {
		mode = "vector-only"
	}

	merged := mergeHybrid(vectorCandidates, textCandidates, m.vectorWeight, m.textWeight)
	for i := range merged {
		if m.temporalDecayEnabled {
			merged[i].Score *= temporalDecayFactor(merged[i].FileDate, m.halfLifeDays)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		if merged[i].Path != merged[j].Path {
			return merged[i].Path < merged[j].Path
		}
		return merged[i].StartLine < merged[j].StartLine
	})

	strict := filterByScore(merged, minScore)
	selected := strict
	if len(selected) == 0 {
		selected = filterByScore(merged, minScore*0.6)
	}
	if m.mmrEnabled && len(selected) > 1 {
		selected = rerankMMR(selected, maxResults, m.mmrLambda)
	}
	if len(selected) > maxResults {
		selected = selected[:maxResults]
	}

	results := make([]memory.MemorySearchResult, 0, len(selected))
	for _, item := range selected {
		citation := fmt.Sprintf("%s:%d-%d", item.Path, item.StartLine, item.EndLine)
		results = append(results, memory.MemorySearchResult{
			ID:        fmt.Sprintf("chunk-%d", item.ID),
			Path:      item.Path,
			StartLine: item.StartLine,
			EndLine:   item.EndLine,
			Score:     item.Score,
			Snippet:   clipSnippet(item.Content, 420),
			Source:    item.Source,
			Citation:  citation,
			Metadata: map[string]string{
				"source":   item.Source,
				"path":     item.Path,
				"citation": citation,
			},
		})
	}

	status, _ = m.Status(ctx)
	resp := memory.MemorySearchResponse{
		Results:           results,
		Mode:              mode,
		Provider:          status.Provider,
		Model:             status.Model,
		FallbackReason:    status.FallbackReason,
		UnavailableReason: status.UnavailableReason,
	}
	if mode != "hybrid" {
		if mode == "fts-only" {
			resp.Warning = "embeddings unavailable; using FTS-only fallback"
		} else if mode == "vector-only" {
			resp.Warning = "fts unavailable; using vector-only retrieval"
		}
	}
	return resp, nil
}

func (m *Manager) Read(_ context.Context, req memory.MemoryReadRequest) (memory.MemoryReadResponse, error) {
	if !m.enabled {
		return memory.MemoryReadResponse{}, nil
	}
	window, err := m.files.SafeReadWindow(req.Path, req.From, req.Lines)
	if err != nil {
		return memory.MemoryReadResponse{}, err
	}
	return memory.MemoryReadResponse{
		Path:      window.Path,
		StartLine: window.StartLine,
		EndLine:   window.EndLine,
		Text:      window.Text,
	}, nil
}

func (m *Manager) Status(ctx context.Context) (memory.MemorySearchStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	base := memory.MemorySearchStatus{
		Enabled:            m.enabled,
		Provider:           m.provider.Provider(),
		Model:              m.provider.Model(),
		FTSAvailable:       m.ftsAvailable,
		Dirty:              m.dirty,
		LastSync:           m.lastSync,
		FallbackReason:     m.fallbackReason,
		UnavailableReason:  m.unavailableReason,
		ProviderError:      m.providerError,
		EmbeddingAvailable: m.providerError == "" && m.provider.Provider() != "none",
	}
	m.mu.RUnlock()
	if !m.enabled {
		base.Mode = "disabled"
		return base, nil
	}
	if m.db == nil {
		base.Mode = "unavailable"
		base.UnavailableReason = "memory database is not initialized"
		return base, nil
	}
	var filesCount int
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM memory_files`).Scan(&filesCount)
	var chunksCount int
	_ = m.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM memory_chunks`).Scan(&chunksCount)
	base.Files = filesCount
	base.Chunks = chunksCount
	base.VectorAvailable = base.EmbeddingAvailable
	base.Mode = base.RetrievalMode()
	return base, nil
}

func (m *Manager) ProbeEmbedding(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return m.probeEmbedding(ctx)
}

func (m *Manager) probeEmbedding(ctx context.Context) error {
	if !m.hybridEnabled {
		m.mu.Lock()
		m.providerError = ""
		m.mu.Unlock()
		return nil
	}
	err := m.provider.AvailabilityProbe(ctx)
	m.mu.Lock()
	if err != nil {
		m.providerError = strings.TrimSpace(err.Error())
	} else {
		m.providerError = ""
	}
	m.mu.Unlock()
	return err
}

func (m *Manager) Sync(ctx context.Context, force bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !m.enabled || m.db == nil {
		return nil
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()

	m.mu.RLock()
	nextSyncDue := m.lastSync.IsZero() || time.Since(m.lastSync) >= m.syncInterval
	dirty := m.dirty
	m.mu.RUnlock()
	if !force && !dirty && !nextSyncDue {
		return nil
	}

	filesToIndex, err := m.listCanonicalFiles()
	if err != nil {
		return err
	}
	if force {
		if _, err := m.db.ExecContext(ctx, `DELETE FROM memory_chunks_fts`); err != nil && m.ftsAvailable {
			return fmt.Errorf("clear fts index: %w", err)
		}
		if _, err := m.db.ExecContext(ctx, `DELETE FROM memory_chunks`); err != nil {
			return fmt.Errorf("clear chunk index: %w", err)
		}
		if _, err := m.db.ExecContext(ctx, `DELETE FROM memory_files`); err != nil {
			return fmt.Errorf("clear file index: %w", err)
		}
	}

	seen := make(map[string]struct{}, len(filesToIndex))
	for _, path := range filesToIndex {
		seen[path] = struct{}{}
		if err := m.indexFileIfChanged(ctx, path); err != nil {
			return err
		}
	}
	if err := m.removeDeletedFiles(ctx, seen); err != nil {
		return err
	}

	m.mu.Lock()
	m.lastSync = time.Now().UTC()
	m.dirty = false
	m.unavailableReason = ""
	m.mu.Unlock()
	return nil
}

func (m *Manager) initSchema(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memory_files (
			file_path TEXT PRIMARY KEY,
			source TEXT NOT NULL,
			content_hash TEXT NOT NULL,
			modified_unix INTEGER NOT NULL,
			indexed_unix INTEGER NOT NULL,
			line_count INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create memory_files table: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memory_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_path TEXT NOT NULL,
			start_line INTEGER NOT NULL,
			end_line INTEGER NOT NULL,
			content TEXT NOT NULL,
			source TEXT NOT NULL,
			file_date TEXT NOT NULL,
			embedding BLOB NOT NULL,
			updated_unix INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create memory_chunks table: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_chunks_file_path ON memory_chunks(file_path)
	`); err != nil {
		return fmt.Errorf("create memory_chunks file index: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_memory_chunks_source ON memory_chunks(source)
	`); err != nil {
		return fmt.Errorf("create memory_chunks source index: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS embedding_cache (
			cache_key TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			embedding BLOB NOT NULL,
			updated_unix INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create embedding_cache table: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS memory_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create memory_meta table: %w", err)
	}
	if _, err := m.db.ExecContext(ctx, `
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
		return fmt.Errorf("create legacy memory_records table: %w", err)
	}

	if _, err := m.db.ExecContext(ctx, `
		CREATE VIRTUAL TABLE IF NOT EXISTS memory_chunks_fts
		USING fts5(content, file_path UNINDEXED, source UNINDEXED, chunk_id UNINDEXED, tokenize='unicode61')
	`); err != nil {
		m.mu.Lock()
		m.ftsAvailable = false
		m.fallbackReason = "fts unavailable: " + strings.TrimSpace(err.Error())
		m.mu.Unlock()
		return nil
	}
	m.mu.Lock()
	m.ftsAvailable = true
	m.mu.Unlock()
	return nil
}

func (m *Manager) listCanonicalFiles() ([]string, error) {
	out := make([]string, 0, 32)
	if _, ok := m.sources["memory"]; ok {
		memoryDoc := m.files.MemoryDocPath()
		if info, err := os.Stat(memoryDoc); err == nil && !info.IsDir() {
			out = append(out, memoryDoc)
		}
		entries, err := os.ReadDir(m.files.MemoryRoot())
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				name := entry.Name()
				if !strings.HasSuffix(strings.ToLower(name), ".md") {
					continue
				}
				if !isDailyMemoryFile(name) {
					continue
				}
				out = append(out, filepath.Join(m.files.MemoryRoot(), name))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *Manager) indexFileIfChanged(ctx context.Context, path string) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read memory file %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat memory file %s: %w", path, err)
	}
	contentHash := hashBytes(blob)
	var existingHash string
	_ = m.db.QueryRowContext(ctx, `SELECT content_hash FROM memory_files WHERE file_path = ?`, path).Scan(&existingHash)
	if existingHash == contentHash {
		return nil
	}

	chunks := splitIntoChunks(path, string(blob), m.chunkTokens, m.chunkOverlap)
	embeddings := make([][]float32, len(chunks))
	if m.hybridEnabled && m.provider != nil {
		texts := make([]string, 0, len(chunks))
		indexes := make([]int, 0, len(chunks))
		for i := range chunks {
			if strings.TrimSpace(chunks[i].Content) == "" {
				continue
			}
			texts = append(texts, chunks[i].Content)
			indexes = append(indexes, i)
		}
		if len(texts) > 0 {
			vectors, err := m.embedTextsWithCache(ctx, texts)
			if err == nil {
				for i := range vectors {
					embeddings[indexes[i]] = vectors[i]
				}
			} else {
				m.setFallbackReason("embedding index fallback: " + err.Error())
			}
		}
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin memory index tx: %w", err)
	}
	defer tx.Rollback()

	if err := deleteFileChunks(ctx, tx, path, m.ftsAvailable); err != nil {
		return err
	}

	for i, chunk := range chunks {
		embeddingBlob, _ := json.Marshal(embeddings[i])
		res, err := tx.ExecContext(ctx, `
			INSERT INTO memory_chunks (file_path, start_line, end_line, content, source, file_date, embedding, updated_unix)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, path, chunk.StartLine, chunk.EndLine, chunk.Content, chunk.Source, chunk.FileDate, embeddingBlob, time.Now().UTC().Unix())
		if err != nil {
			return fmt.Errorf("insert memory chunk: %w", err)
		}
		if m.ftsAvailable {
			chunkID, _ := res.LastInsertId()
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO memory_chunks_fts(content, file_path, source, chunk_id)
				VALUES (?, ?, ?, ?)
			`, chunk.Content, path, chunk.Source, chunkID); err != nil {
				return fmt.Errorf("insert fts chunk: %w", err)
			}
		}
	}
	lineCount := strings.Count(string(blob), "\n") + 1
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_files (file_path, source, content_hash, modified_unix, indexed_unix, line_count)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			source = excluded.source,
			content_hash = excluded.content_hash,
			modified_unix = excluded.modified_unix,
			indexed_unix = excluded.indexed_unix,
			line_count = excluded.line_count
	`, path, sourceForPath(path), contentHash, info.ModTime().UTC().Unix(), time.Now().UTC().Unix(), lineCount); err != nil {
		return fmt.Errorf("upsert memory_file row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit memory index tx: %w", err)
	}
	return nil
}

func deleteFileChunks(ctx context.Context, tx *sql.Tx, path string, ftsAvailable bool) error {
	if ftsAvailable {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM memory_chunks WHERE file_path = ?`, path)
		if err != nil {
			return fmt.Errorf("query existing chunk ids: %w", err)
		}
		chunkIDs := make([]int64, 0, 32)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err == nil {
				chunkIDs = append(chunkIDs, id)
			}
		}
		rows.Close()
		for _, id := range chunkIDs {
			if _, err := tx.ExecContext(ctx, `DELETE FROM memory_chunks_fts WHERE chunk_id = ?`, id); err != nil {
				return fmt.Errorf("delete existing fts chunk: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_chunks WHERE file_path = ?`, path); err != nil {
		return fmt.Errorf("delete existing chunks: %w", err)
	}
	return nil
}

func (m *Manager) removeDeletedFiles(ctx context.Context, seen map[string]struct{}) error {
	rows, err := m.db.QueryContext(ctx, `SELECT file_path FROM memory_files`)
	if err != nil {
		return fmt.Errorf("query indexed memory files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err := deleteFileChunks(ctx, tx, path, m.ftsAvailable); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_files WHERE file_path = ?`, path); err != nil {
			tx.Rollback()
			return fmt.Errorf("delete stale memory_file row: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (m *Manager) searchFTS(ctx context.Context, query string, limit int) ([]scoredChunk, error) {
	if limit <= 0 {
		limit = m.maxResults * m.candidateMultiplier
	}
	if limit < 8 {
		limit = 8
	}
	if !m.ftsAvailable {
		return nil, nil
	}
	ftsQuery := strings.TrimSpace(queryexpansion.BuildFTSQuery(query))
	if ftsQuery == "" {
		ftsQuery = strings.TrimSpace(query)
	}
	if ftsQuery == "" {
		return nil, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT c.id, c.file_path, c.start_line, c.end_line, c.content, c.source, c.file_date, bm25(memory_chunks_fts)
		FROM memory_chunks_fts
		JOIN memory_chunks c ON c.id = memory_chunks_fts.chunk_id
		WHERE memory_chunks_fts MATCH ?
		ORDER BY bm25(memory_chunks_fts)
		LIMIT ?
	`, ftsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]scoredChunk, 0, limit)
	for rows.Next() {
		var (
			item scoredChunk
			rank float64
		)
		if err := rows.Scan(&item.ID, &item.Path, &item.StartLine, &item.EndLine, &item.Content, &item.Source, &item.FileDate, &rank); err != nil {
			continue
		}
		if math.IsNaN(rank) || math.IsInf(rank, 0) {
			rank = 99
		}
		item.TextScore = 1.0 / (1.0 + math.Max(rank, 0))
		results = append(results, item)
	}
	return results, rows.Err()
}

func (m *Manager) searchVector(ctx context.Context, queryEmbedding []float32, limit int) ([]scoredChunk, error) {
	if len(queryEmbedding) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = m.maxResults * m.candidateMultiplier
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, file_path, start_line, end_line, content, source, file_date, embedding
		FROM memory_chunks
		LIMIT ?
	`, limit*8)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := make([]scoredChunk, 0, limit)
	for rows.Next() {
		var (
			item          scoredChunk
			embeddingBlob []byte
		)
		if err := rows.Scan(&item.ID, &item.Path, &item.StartLine, &item.EndLine, &item.Content, &item.Source, &item.FileDate, &embeddingBlob); err != nil {
			continue
		}
		if len(embeddingBlob) == 0 {
			continue
		}
		var vec []float32
		if err := json.Unmarshal(embeddingBlob, &vec); err != nil || len(vec) == 0 {
			continue
		}
		sim, err := cosine(queryEmbedding, vec)
		if err != nil {
			continue
		}
		item.VectorScore = normalizeSimilarity(sim)
		item.Embedding = vec
		results = append(results, item)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].VectorScore > results[j].VectorScore
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (m *Manager) embedTextsWithCache(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	provider := strings.TrimSpace(m.provider.Provider())
	model := strings.TrimSpace(m.provider.Model())
	cacheKeys := make([]string, len(texts))
	out := make([][]float32, len(texts))
	missing := make([]string, 0, len(texts))
	missingIndexes := make([]int, 0, len(texts))
	for i, text := range texts {
		key := fmt.Sprintf("%s:%s:%s", provider, model, hashBytes([]byte(text)))
		cacheKeys[i] = key
		row := m.db.QueryRowContext(ctx, `SELECT embedding FROM embedding_cache WHERE cache_key = ?`, key)
		var blob []byte
		if err := row.Scan(&blob); err == nil {
			var vec []float32
			if json.Unmarshal(blob, &vec) == nil {
				out[i] = vec
				continue
			}
		}
		missing = append(missing, text)
		missingIndexes = append(missingIndexes, i)
	}
	if len(missing) == 0 {
		return out, nil
	}
	vectors, err := m.provider.EmbedBatch(ctx, missing)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Unix()
	for i := range vectors {
		idx := missingIndexes[i]
		out[idx] = vectors[i]
		blob, _ := json.Marshal(vectors[i])
		_, _ = m.db.ExecContext(ctx, `
			INSERT INTO embedding_cache(cache_key, provider, model, embedding, updated_unix)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(cache_key) DO UPDATE SET
				embedding = excluded.embedding,
				updated_unix = excluded.updated_unix
		`, cacheKeys[idx], provider, model, blob, now)
	}
	return out, nil
}

func (m *Manager) setFallbackReason(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	m.mu.Lock()
	m.fallbackReason = reason
	m.mu.Unlock()
}

func configureSQLiteConnection(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("sqlite db is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("set sqlite busy_timeout pragma: %w", err)
	}
	return nil
}

func (m *Manager) setUnavailableReason(reason string) {
	reason = strings.TrimSpace(reason)
	m.mu.Lock()
	m.unavailableReason = reason
	m.mu.Unlock()
}

type scoredChunk struct {
	ID        int64
	Path      string
	StartLine int
	EndLine   int
	Content   string
	Source    string
	FileDate  string
	Embedding []float32

	VectorScore float64
	TextScore   float64
	Score       float64
}

func mergeHybrid(vector []scoredChunk, text []scoredChunk, vectorWeight float64, textWeight float64) []scoredChunk {
	byID := map[int64]*scoredChunk{}
	for i := range vector {
		item := vector[i]
		existing, ok := byID[item.ID]
		if !ok {
			copyItem := item
			existing = &copyItem
			byID[item.ID] = existing
		}
		existing.VectorScore = item.VectorScore
	}
	for i := range text {
		item := text[i]
		existing, ok := byID[item.ID]
		if !ok {
			copyItem := item
			existing = &copyItem
			byID[item.ID] = existing
		}
		existing.TextScore = item.TextScore
		if strings.TrimSpace(existing.Content) == "" {
			existing.Content = item.Content
			existing.Path = item.Path
			existing.StartLine = item.StartLine
			existing.EndLine = item.EndLine
			existing.Source = item.Source
			existing.FileDate = item.FileDate
		}
	}
	out := make([]scoredChunk, 0, len(byID))
	for _, item := range byID {
		score := vectorWeight*item.VectorScore + textWeight*item.TextScore
		if item.VectorScore > 0 && item.TextScore == 0 && textWeight == 0 {
			score = item.VectorScore
		}
		if item.TextScore > 0 && item.VectorScore == 0 && vectorWeight == 0 {
			score = item.TextScore
		}
		if score == 0 {
			score = item.VectorScore
			if score == 0 {
				score = item.TextScore
			}
		}
		item.Score = score
		out = append(out, *item)
	}
	return out
}

func filterByScore(items []scoredChunk, minScore float64) []scoredChunk {
	if len(items) == 0 {
		return nil
	}
	out := make([]scoredChunk, 0, len(items))
	for _, item := range items {
		if item.Score >= minScore {
			out = append(out, item)
		}
	}
	return out
}

func rerankMMR(items []scoredChunk, limit int, lambda float64) []scoredChunk {
	if len(items) == 0 {
		return nil
	}
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	if lambda <= 0 || lambda > 1 {
		lambda = defaultMMRLambda
	}
	remaining := append([]scoredChunk(nil), items...)
	selected := make([]scoredChunk, 0, limit)
	for len(remaining) > 0 && len(selected) < limit {
		bestIdx := 0
		bestScore := -math.MaxFloat64
		for i, candidate := range remaining {
			redundancy := 0.0
			for _, chosen := range selected {
				sim := lexicalSimilarity(candidate.Content, chosen.Content)
				if sim > redundancy {
					redundancy = sim
				}
			}
			mmr := lambda*candidate.Score - (1-lambda)*redundancy
			if mmr > bestScore {
				bestScore = mmr
				bestIdx = i
			}
		}
		selected = append(selected, remaining[bestIdx])
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}
	return selected
}

func lexicalSimilarity(a, b string) float64 {
	ta := tokenSet(a)
	tb := tokenSet(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	intersection := 0
	union := len(ta)
	for token := range tb {
		if _, ok := ta[token]; ok {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func tokenSet(text string) map[string]struct{} {
	parts := queryexpansion.Expand(text, 64)
	out := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		out[part] = struct{}{}
	}
	return out
}

func splitIntoChunks(path string, body string, chunkTokens int, overlapTokens int) []chunk {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return nil
	}
	chunks := make([]chunk, 0, len(lines)/4+1)
	source := sourceForPath(path)
	date := dateForPath(path)
	start := 0
	for start < len(lines) {
		end := start
		tokens := 0
		for end < len(lines) {
			tokens += len(queryexpansion.Expand(lines[end], 128))
			if tokens >= chunkTokens {
				end++
				break
			}
			end++
		}
		if end <= start {
			end = start + 1
		}
		if end > len(lines) {
			end = len(lines)
		}
		content := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if content != "" {
			chunks = append(chunks, chunk{
				StartLine: start + 1,
				EndLine:   end,
				Content:   content,
				Source:    source,
				FileDate:  date,
			})
		}
		nextStart := end
		if overlapTokens > 0 {
			back := end - 1
			overlap := 0
			for back >= start {
				overlap += len(queryexpansion.Expand(lines[back], 128))
				if overlap >= overlapTokens {
					break
				}
				back--
			}
			if back+1 < nextStart {
				nextStart = back + 1
			}
		}
		if nextStart <= start {
			nextStart = end
		}
		start = nextStart
	}
	return chunks
}

type chunk struct {
	StartLine int
	EndLine   int
	Content   string
	Source    string
	FileDate  string
}

func sourceForPath(path string) string {
	if isDailyMemoryFile(filepath.Base(path)) || strings.EqualFold(filepath.Base(path), "memory.md") {
		return "memory"
	}
	return "sessions"
}

func dateForPath(path string) string {
	base := filepath.Base(path)
	if isDailyMemoryFile(base) {
		return strings.TrimSuffix(base, ".md")
	}
	if info, err := os.Stat(path); err == nil {
		return info.ModTime().UTC().Format("2006-01-02")
	}
	return time.Now().UTC().Format("2006-01-02")
}

func isDailyMemoryFile(name string) bool {
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		return false
	}
	trim := strings.TrimSuffix(name, ".md")
	_, err := time.Parse("2006-01-02", trim)
	return err == nil
}

func temporalDecayFactor(date string, halfLifeDays int) float64 {
	if halfLifeDays <= 0 {
		halfLifeDays = defaultHalfLifeDays
	}
	if strings.TrimSpace(date) == "" {
		return 1
	}
	ts, err := time.Parse("2006-01-02", date)
	if err != nil {
		return 1
	}
	ageDays := time.Since(ts.UTC()).Hours() / 24.0
	if ageDays <= 0 {
		return 1
	}
	return math.Pow(0.5, ageDays/float64(halfLifeDays))
}

func clipSnippet(text string, max int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if max <= 0 || len(text) <= max {
		return text
	}
	if max < 4 {
		return text[:max]
	}
	return text[:max-3] + "..."
}

func normalizeSimilarity(sim float64) float64 {
	if math.IsNaN(sim) || math.IsInf(sim, 0) {
		return 0
	}
	if sim < -1 {
		sim = -1
	}
	if sim > 1 {
		sim = 1
	}
	return (sim + 1) / 2
}

func cosine(a []float32, b []float32) (float64, error) {
	return memory.CosineSimilarity(a, b)
}

func normalizeSources(in []string) map[string]struct{} {
	out := map[string]struct{}{"memory": {}}
	if len(in) == 0 {
		return out
	}
	out = map[string]struct{}{}
	for _, source := range in {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "" {
			continue
		}
		out[source] = struct{}{}
	}
	if len(out) == 0 {
		out["memory"] = struct{}{}
	}
	return out
}

func hashBytes(blob []byte) string {
	h := sha256.Sum256(blob)
	return hex.EncodeToString(h[:])
}

func valueInt(v int, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

func valueFloat(v float64, fallback float64) float64 {
	if v <= 0 {
		return fallback
	}
	return v
}
