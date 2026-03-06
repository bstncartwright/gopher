package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type EventStoreOptions struct {
	Path         string
	StreamBuffer int
}

type EventStore struct {
	db *sql.DB

	mu          sync.RWMutex
	subscribers map[sessionrt.SessionID]map[uint64]chan sessionrt.Event
	nextSubID   uint64
	streamBuf   int
}

var _ sessionrt.EventStore = (*EventStore)(nil)
var _ sessionrt.SessionRegistryStore = (*EventStore)(nil)

func NewEventStore(opts EventStoreOptions) (*EventStore, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		slog.Error("sqlite_store: path is required")
		return nil, fmt.Errorf("%w: sqlite path is required", sessionrt.ErrInvalidSession)
	}
	streamBuf := opts.StreamBuffer
	if streamBuf <= 0 {
		streamBuf = 256
	}

	slog.Info("sqlite_store: opening database", "path", path, "stream_buffer", streamBuf)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		slog.Error("sqlite_store: failed to open database", "path", path, "error", err)
		return nil, fmt.Errorf("open sqlite database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &EventStore{
		db:          db,
		subscribers: make(map[sessionrt.SessionID]map[uint64]chan sessionrt.Event),
		streamBuf:   streamBuf,
	}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		slog.Error("sqlite_store: failed to init schema", "path", path, "error", err)
		return nil, err
	}
	slog.Info("sqlite_store: database initialized", "path", path)
	return store, nil
}

func (s *EventStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	slog.Info("sqlite_store: closing database")
	return s.db.Close()
}

func (s *EventStore) Append(ctx context.Context, event sessionrt.Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	sessionID := strings.TrimSpace(string(event.SessionID))
	if sessionID == "" {
		slog.Error("sqlite_store: session ID is required for append")
		return fmt.Errorf("%w: session ID is required", sessionrt.ErrInvalidEvent)
	}

	slog.Debug("sqlite_store: appending event",
		"session_id", sessionID,
		"event_type", event.Type,
		"event_seq", event.Seq,
	)

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		slog.Error("sqlite_store: failed to marshal payload", "session_id", sessionID, "error", err)
		return fmt.Errorf("marshal event payload: %w", err)
	}
	timestampMillis := event.Timestamp.UTC().UnixMilli()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		slog.Error("sqlite_store: failed to begin transaction", "session_id", sessionID, "error", err)
		return fmt.Errorf("begin append transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	record, err := loadSessionRecordTx(ctx, tx, event.SessionID)
	if err != nil {
		if !errors.Is(err, sessionrt.ErrSessionNotFound) {
			return err
		}
		record = sessionrt.SessionRecord{SessionID: event.SessionID}
	}
	if displayName, ok := displayNameFromSessionCreatedEvent(event); ok {
		record.DisplayName = displayName
	}

	if record.LastSeq > 0 {
		switch {
		case event.Seq <= record.LastSeq:
			slog.Debug("sqlite_store: event already exists, skipping", "session_id", sessionID, "seq", event.Seq)
			return nil
		case event.Seq != record.LastSeq+1:
			slog.Error("sqlite_store: sequence must be monotonic",
				"session_id", sessionID,
				"last_seq", record.LastSeq,
				"next_seq", event.Seq,
			)
			return fmt.Errorf("%w: sequence must be monotonic for session %s (last=%d next=%d)", sessionrt.ErrInvalidEvent, event.SessionID, record.LastSeq, event.Seq)
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO events (session_id, seq, event_id, timestamp, actor_id, type, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, sessionID, int64(event.Seq), string(event.ID), timestampMillis, string(event.From), string(event.Type), payload)
	if err != nil {
		if isSQLitePrimaryKeyViolation(err) {
			slog.Debug("sqlite_store: event already exists (constraint)", "session_id", sessionID, "seq", event.Seq)
			return nil
		}
		slog.Error("sqlite_store: failed to insert event", "session_id", sessionID, "error", err)
		return fmt.Errorf("insert event: %w", err)
	}

	record.SessionID = event.SessionID
	if record.CreatedAt.IsZero() {
		record.CreatedAt = event.Timestamp.UTC()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if event.Seq > record.LastSeq {
		record.LastSeq = event.Seq
	}
	record.Status = applyStatusTransition(record.Status, event)
	record.UpdatedAt = event.Timestamp.UTC()
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	if err := upsertSessionTx(ctx, tx, record); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		slog.Error("sqlite_store: failed to commit transaction", "session_id", sessionID, "error", err)
		return fmt.Errorf("commit append transaction: %w", err)
	}
	slog.Debug("sqlite_store: event appended", "session_id", sessionID, "event_type", event.Type, "seq", event.Seq)
	s.notifySubscribers(event)
	return nil
}

func (s *EventStore) List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	slog.Debug("sqlite_store: listing events", "session_id", sessionID)
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, timestamp, actor_id, type, payload, seq
		FROM events
		WHERE session_id = ?
		ORDER BY seq ASC
	`, string(sessionID))
	if err != nil {
		slog.Error("sqlite_store: failed to query events", "session_id", sessionID, "error", err)
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	events := make([]sessionrt.Event, 0)
	for rows.Next() {
		var eventID string
		var tsMillis int64
		var actorID string
		var eventType string
		var payloadBlob []byte
		var seq int64
		if err := rows.Scan(&eventID, &tsMillis, &actorID, &eventType, &payloadBlob, &seq); err != nil {
			slog.Error("sqlite_store: failed to scan event row", "session_id", sessionID, "error", err)
			return nil, fmt.Errorf("scan event row: %w", err)
		}
		payload, err := decodePayload(payloadBlob)
		if err != nil {
			slog.Error("sqlite_store: failed to decode payload", "session_id", sessionID, "seq", seq, "error", err)
			return nil, fmt.Errorf("decode payload for session %s seq %d: %w", sessionID, seq, err)
		}
		events = append(events, sessionrt.Event{
			ID:        sessionrt.EventID(eventID),
			SessionID: sessionID,
			From:      sessionrt.ActorID(actorID),
			Type:      sessionrt.EventType(eventType),
			Payload:   payload,
			Timestamp: fromMillis(tsMillis),
			Seq:       uint64(seq),
		})
	}
	if err := rows.Err(); err != nil {
		slog.Error("sqlite_store: error iterating rows", "session_id", sessionID, "error", err)
		return nil, fmt.Errorf("iterate event rows: %w", err)
	}
	if len(events) == 0 {
		slog.Debug("sqlite_store: no events found", "session_id", sessionID)
		return nil, sessionrt.ErrSessionNotFound
	}
	slog.Debug("sqlite_store: listed events", "session_id", sessionID, "count", len(events))
	return events, nil
}

func (s *EventStore) ListBefore(ctx context.Context, sessionID sessionrt.SessionID, beforeSeq uint64, limit int) ([]sessionrt.Event, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	default:
	}

	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT event_id, timestamp, actor_id, type, payload, seq
		FROM events
		WHERE session_id = ?
	`
	args := []any{string(sessionID)}
	if beforeSeq > 0 {
		query += ` AND seq < ?`
		args = append(args, int64(beforeSeq))
	}
	query += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("query events before: %w", err)
	}
	defer rows.Close()

	events := make([]sessionrt.Event, 0, limit+1)
	for rows.Next() {
		event, err := scanEventRow(rows, sessionID)
		if err != nil {
			return nil, false, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate event rows: %w", err)
	}
	if len(events) == 0 {
		exists, err := s.sessionExists(ctx, sessionID)
		if err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, sessionrt.ErrSessionNotFound
		}
		return nil, false, nil
	}

	hasMore := len(events) > limit
	if hasMore {
		events = events[:limit]
	}
	reverseEvents(events)
	return events, hasMore, nil
}

func (s *EventStore) ListAfter(ctx context.Context, sessionID sessionrt.SessionID, afterSeq uint64, limit int) ([]sessionrt.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	query := `
		SELECT event_id, timestamp, actor_id, type, payload, seq
		FROM events
		WHERE session_id = ? AND seq > ?
		ORDER BY seq ASC
	`
	args := []any{string(sessionID), int64(afterSeq)}
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query events after: %w", err)
	}
	defer rows.Close()

	capHint := 0
	if limit > 0 {
		capHint = limit
	}
	events := make([]sessionrt.Event, 0, capHint)
	for rows.Next() {
		event, err := scanEventRow(rows, sessionID)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event rows: %w", err)
	}
	if len(events) == 0 {
		exists, err := s.sessionExists(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, sessionrt.ErrSessionNotFound
		}
	}
	return events, nil
}

func (s *EventStore) Stream(ctx context.Context, sessionID sessionrt.SessionID) (<-chan sessionrt.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if _, err := s.GetSessionRecord(ctx, sessionID); err != nil {
		if !errors.Is(err, sessionrt.ErrSessionNotFound) {
			return nil, err
		}
		if _, err := s.List(ctx, sessionID); err != nil {
			return nil, err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	subID := atomic.AddUint64(&s.nextSubID, 1)
	ch := make(chan sessionrt.Event, s.streamBuf)
	if s.subscribers[sessionID] == nil {
		s.subscribers[sessionID] = make(map[uint64]chan sessionrt.Event)
	}
	s.subscribers[sessionID][subID] = ch

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		subs := s.subscribers[sessionID]
		stream, ok := subs[subID]
		if !ok {
			return
		}
		delete(subs, subID)
		close(stream)
	}()

	return ch, nil
}

func (s *EventStore) UpsertSession(ctx context.Context, record sessionrt.SessionRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if strings.TrimSpace(string(record.SessionID)) == "" {
		return fmt.Errorf("%w: session ID is required", sessionrt.ErrInvalidSession)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin upsert session transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	existing, err := loadSessionRecordTx(ctx, tx, record.SessionID)
	if err != nil && !errors.Is(err, sessionrt.ErrSessionNotFound) {
		return err
	}
	if err == nil {
		if strings.TrimSpace(record.DisplayName) == "" {
			record.DisplayName = existing.DisplayName
		}
		if record.CreatedAt.IsZero() {
			record.CreatedAt = existing.CreatedAt
		}
		if record.UpdatedAt.IsZero() {
			record.UpdatedAt = existing.UpdatedAt
		}
		if record.LastSeq < existing.LastSeq {
			record.LastSeq = existing.LastSeq
		}
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	if err := upsertSessionTx(ctx, tx, record); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upsert session transaction: %w", err)
	}
	return nil
}

func (s *EventStore) GetSessionRecord(ctx context.Context, sessionID sessionrt.SessionID) (sessionrt.SessionRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return sessionrt.SessionRecord{}, ctx.Err()
	default:
	}

	record, err := loadSessionRecord(ctx, s.db, sessionID)
	if err != nil {
		return sessionrt.SessionRecord{}, err
	}
	return record, nil
}

func (s *EventStore) ListSessions(ctx context.Context) ([]sessionrt.SessionRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, display_name, status, created_at, updated_at, last_seq, in_flight
		FROM sessions
		ORDER BY session_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	records := make([]sessionrt.SessionRecord, 0)
	for rows.Next() {
		record, err := scanSessionRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions rows: %w", err)
	}
	return records, nil
}

func (s *EventStore) initSchema(ctx context.Context) error {
	slog.Debug("sqlite_store: initializing schema")
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS events (
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			event_id TEXT NOT NULL,
			timestamp INTEGER NOT NULL,
			actor_id TEXT NOT NULL,
			type TEXT NOT NULL,
			payload BLOB NOT NULL,
			PRIMARY KEY (session_id, seq)
		)
	`); err != nil {
		slog.Error("sqlite_store: failed to create events table", "error", err)
		return fmt.Errorf("create events table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS sessions (
			session_id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			status INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			last_seq INTEGER NOT NULL,
			in_flight INTEGER NOT NULL
		)
	`); err != nil {
		slog.Error("sqlite_store: failed to create sessions table", "error", err)
		return fmt.Errorf("create sessions table: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		ALTER TABLE sessions ADD COLUMN display_name TEXT NOT NULL DEFAULT ''
	`); err != nil && !isSQLiteDuplicateColumnErr(err) {
		slog.Error("sqlite_store: failed to migrate sessions table", "error", err)
		return fmt.Errorf("migrate sessions table display_name: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_events_session_seq ON events(session_id, seq)
	`); err != nil {
		slog.Error("sqlite_store: failed to create events index", "error", err)
		return fmt.Errorf("create events index: %w", err)
	}
	slog.Debug("sqlite_store: schema initialized")
	return nil
}

func (s *EventStore) notifySubscribers(event sessionrt.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	subs := s.subscribers[event.SessionID]
	for subID, ch := range subs {
		select {
		case ch <- event:
		default:
			// Keep lagging subscribers connected by dropping their oldest buffered
			// event and preferring the newest event.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- event:
			default:
				close(ch)
				delete(subs, subID)
			}
		}
	}
}

func loadSessionRecord(ctx context.Context, db *sql.DB, sessionID sessionrt.SessionID) (sessionrt.SessionRecord, error) {
	row := db.QueryRowContext(ctx, `
		SELECT session_id, display_name, status, created_at, updated_at, last_seq, in_flight
		FROM sessions
		WHERE session_id = ?
	`, string(sessionID))
	return scanSessionRecordFromRow(row)
}

func loadSessionRecordTx(ctx context.Context, tx *sql.Tx, sessionID sessionrt.SessionID) (sessionrt.SessionRecord, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT session_id, display_name, status, created_at, updated_at, last_seq, in_flight
		FROM sessions
		WHERE session_id = ?
	`, string(sessionID))
	return scanSessionRecordFromRow(row)
}

func upsertSessionTx(ctx context.Context, tx *sql.Tx, record sessionrt.SessionRecord) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sessions (session_id, display_name, status, created_at, updated_at, last_seq, in_flight)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id)
		DO UPDATE SET
			display_name = excluded.display_name,
			status = excluded.status,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at,
			last_seq = excluded.last_seq,
			in_flight = excluded.in_flight
	`,
		string(record.SessionID),
		strings.TrimSpace(record.DisplayName),
		int(record.Status),
		record.CreatedAt.UTC().UnixMilli(),
		record.UpdatedAt.UTC().UnixMilli(),
		int64(record.LastSeq),
		boolToInt(record.InFlight),
	)
	if err != nil {
		return fmt.Errorf("upsert session record: %w", err)
	}
	return nil
}

func scanSessionRecord(rows *sql.Rows) (sessionrt.SessionRecord, error) {
	var sessionID string
	var displayName string
	var status int
	var createdAtMillis int64
	var updatedAtMillis int64
	var lastSeq int64
	var inFlight int
	if err := rows.Scan(&sessionID, &displayName, &status, &createdAtMillis, &updatedAtMillis, &lastSeq, &inFlight); err != nil {
		return sessionrt.SessionRecord{}, fmt.Errorf("scan session row: %w", err)
	}
	return sessionrt.SessionRecord{
		SessionID:   sessionrt.SessionID(sessionID),
		DisplayName: strings.TrimSpace(displayName),
		Status:      sessionrt.SessionStatus(status),
		CreatedAt:   fromMillis(createdAtMillis),
		UpdatedAt:   fromMillis(updatedAtMillis),
		LastSeq:     uint64(lastSeq),
		InFlight:    inFlight != 0,
	}, nil
}

func scanSessionRecordFromRow(row *sql.Row) (sessionrt.SessionRecord, error) {
	var sessionID string
	var displayName string
	var status int
	var createdAtMillis int64
	var updatedAtMillis int64
	var lastSeq int64
	var inFlight int
	if err := row.Scan(&sessionID, &displayName, &status, &createdAtMillis, &updatedAtMillis, &lastSeq, &inFlight); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sessionrt.SessionRecord{}, sessionrt.ErrSessionNotFound
		}
		return sessionrt.SessionRecord{}, fmt.Errorf("scan session row: %w", err)
	}
	return sessionrt.SessionRecord{
		SessionID:   sessionrt.SessionID(sessionID),
		DisplayName: strings.TrimSpace(displayName),
		Status:      sessionrt.SessionStatus(status),
		CreatedAt:   fromMillis(createdAtMillis),
		UpdatedAt:   fromMillis(updatedAtMillis),
		LastSeq:     uint64(lastSeq),
		InFlight:    inFlight != 0,
	}, nil
}

func scanEventRow(scanner interface{ Scan(dest ...any) error }, sessionID sessionrt.SessionID) (sessionrt.Event, error) {
	var eventID string
	var tsMillis int64
	var actorID string
	var eventType string
	var payloadBlob []byte
	var seq int64
	if err := scanner.Scan(&eventID, &tsMillis, &actorID, &eventType, &payloadBlob, &seq); err != nil {
		return sessionrt.Event{}, fmt.Errorf("scan event row: %w", err)
	}
	payload, err := decodePayload(payloadBlob)
	if err != nil {
		return sessionrt.Event{}, fmt.Errorf("decode payload for session %s seq %d: %w", sessionID, seq, err)
	}
	return sessionrt.Event{
		ID:        sessionrt.EventID(eventID),
		SessionID: sessionID,
		From:      sessionrt.ActorID(actorID),
		Type:      sessionrt.EventType(eventType),
		Payload:   payload,
		Timestamp: fromMillis(tsMillis),
		Seq:       uint64(seq),
	}, nil
}

func reverseEvents(events []sessionrt.Event) {
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
}

func (s *EventStore) sessionExists(ctx context.Context, sessionID sessionrt.SessionID) (bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT 1 FROM events WHERE session_id = ? LIMIT 1`, string(sessionID))
	var marker int
	if err := row.Scan(&marker); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("query session existence: %w", err)
	}
	return true, nil
}

func decodePayload(blob []byte) (any, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	var payload any
	if err := json.Unmarshal(blob, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func applyStatusTransition(current sessionrt.SessionStatus, event sessionrt.Event) sessionrt.SessionStatus {
	switch event.Type {
	case sessionrt.EventControl:
		ctrl, ok := payloadToControl(event.Payload)
		if !ok {
			return current
		}
		switch ctrl.Action {
		case sessionrt.ControlActionSessionCreated:
			return sessionrt.SessionActive
		case sessionrt.ControlActionSessionCancelled:
			return sessionrt.SessionPaused
		case sessionrt.ControlActionSessionCompleted:
			return sessionrt.SessionCompleted
		case sessionrt.ControlActionSessionFailed:
			return sessionrt.SessionFailed
		}
	case sessionrt.EventError:
		return sessionrt.SessionFailed
	}
	return current
}

func payloadToControl(payload any) (sessionrt.ControlPayload, bool) {
	switch value := payload.(type) {
	case sessionrt.ControlPayload:
		return value, true
	case map[string]any:
		action, _ := value["action"].(string)
		reason, _ := value["reason"].(string)
		out := sessionrt.ControlPayload{Action: action, Reason: reason}
		if metadataAny, ok := value["metadata"]; ok && metadataAny != nil {
			if metadata, ok := metadataAny.(map[string]any); ok {
				out.Metadata = metadata
			}
		}
		return out, action != ""
	default:
		blob, err := json.Marshal(payload)
		if err != nil {
			return sessionrt.ControlPayload{}, false
		}
		var out sessionrt.ControlPayload
		if err := json.Unmarshal(blob, &out); err != nil {
			return sessionrt.ControlPayload{}, false
		}
		if strings.TrimSpace(out.Action) == "" {
			return sessionrt.ControlPayload{}, false
		}
		return out, true
	}
}

func fromMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func isSQLitePrimaryKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") || strings.Contains(message, "constraint")
}

func isSQLiteDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate column")
}

func displayNameFromSessionCreatedEvent(event sessionrt.Event) (string, bool) {
	if event.Type != sessionrt.EventControl {
		return "", false
	}
	ctrl, ok := payloadToControl(event.Payload)
	if !ok || strings.TrimSpace(ctrl.Action) != sessionrt.ControlActionSessionCreated {
		return "", false
	}
	raw, _ := ctrl.Metadata["display_name"].(string)
	name := strings.TrimSpace(raw)
	return name, name != ""
}
