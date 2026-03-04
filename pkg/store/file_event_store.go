package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const sessionsRegistryFilename = "sessions.json"

type FileEventStoreOptions struct {
	Dir          string
	StreamBuffer int
}

type FileEventStore struct {
	mu sync.RWMutex

	dir          string
	eventsDir    string
	sessionsPath string
	streamBuf    int

	eventsCache map[sessionrt.SessionID][]sessionrt.Event
	sessions    map[sessionrt.SessionID]sessionrt.SessionRecord

	subscribers map[sessionrt.SessionID]map[uint64]chan sessionrt.Event
	nextSubID   uint64
}

var _ sessionrt.EventStore = (*FileEventStore)(nil)
var _ sessionrt.SessionRegistryStore = (*FileEventStore)(nil)

func NewFileEventStore(opts FileEventStoreOptions) (*FileEventStore, error) {
	dir := strings.TrimSpace(opts.Dir)
	if dir == "" {
		return nil, fmt.Errorf("%w: store directory is required", sessionrt.ErrInvalidSession)
	}
	streamBuf := opts.StreamBuffer
	if streamBuf <= 0 {
		streamBuf = 32
	}

	eventsDir := filepath.Join(dir, "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		return nil, fmt.Errorf("create events directory: %w", err)
	}
	store := &FileEventStore{
		dir:          dir,
		eventsDir:    eventsDir,
		sessionsPath: filepath.Join(dir, sessionsRegistryFilename),
		streamBuf:    streamBuf,
		eventsCache:  make(map[sessionrt.SessionID][]sessionrt.Event),
		sessions:     make(map[sessionrt.SessionID]sessionrt.SessionRecord),
		subscribers:  make(map[sessionrt.SessionID]map[uint64]chan sessionrt.Event),
	}
	if err := store.loadSessionRegistry(); err != nil {
		return nil, err
	}
	if err := store.reconcileSessionsFromEvents(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *FileEventStore) Append(ctx context.Context, event sessionrt.Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if strings.TrimSpace(string(event.SessionID)) == "" {
		return fmt.Errorf("%w: session ID is required", sessionrt.ErrInvalidEvent)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	record := s.sessions[event.SessionID]
	if record.SessionID == "" {
		record.SessionID = event.SessionID
		record.Status = sessionrt.SessionActive
		record.CreatedAt = event.Timestamp
	}
	if displayName, ok := displayNameFromSessionCreatedEvent(event); ok {
		record.DisplayName = displayName
	}
	if record.LastSeq > 0 {
		switch {
		case event.Seq <= record.LastSeq:
			// Idempotent duplicate/out-of-order retry.
			return nil
		case event.Seq != record.LastSeq+1:
			return fmt.Errorf("%w: sequence must be monotonic for session %s (last=%d next=%d)", sessionrt.ErrInvalidEvent, event.SessionID, record.LastSeq, event.Seq)
		}
	}

	if err := appendJSONLEvent(s.eventsDir, event); err != nil {
		return err
	}
	s.eventsCache[event.SessionID] = append(s.eventsCache[event.SessionID], event)

	if record.CreatedAt.IsZero() {
		record.CreatedAt = event.Timestamp
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if event.Seq > record.LastSeq {
		record.LastSeq = event.Seq
	}
	record.Status = transitionStatus(record.Status, event)
	record.UpdatedAt = event.Timestamp
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	s.sessions[event.SessionID] = record
	if err := s.persistSessionRegistryLocked(); err != nil {
		return err
	}

	s.notifySubscribersLocked(event)
	return nil
}

func (s *FileEventStore) List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	cached, ok := s.eventsCache[sessionID]
	s.mu.RUnlock()
	if ok {
		out := make([]sessionrt.Event, len(cached))
		copy(out, cached)
		return out, nil
	}

	events, err := s.loadSessionEvents(sessionID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.eventsCache[sessionID] = append([]sessionrt.Event(nil), events...)
	s.mu.Unlock()

	out := make([]sessionrt.Event, len(events))
	copy(out, events)
	return out, nil
}

func (s *FileEventStore) Stream(ctx context.Context, sessionID sessionrt.SessionID) (<-chan sessionrt.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		if _, err := s.loadSessionEvents(sessionID); err != nil {
			return nil, err
		}
	}

	s.nextSubID++
	subID := s.nextSubID
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

func (s *FileEventStore) UpsertSession(ctx context.Context, record sessionrt.SessionRecord) error {
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

	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.sessions[record.SessionID]
	if existing.SessionID != "" {
		if strings.TrimSpace(record.DisplayName) == "" {
			record.DisplayName = existing.DisplayName
		}
		if record.CreatedAt.IsZero() {
			record.CreatedAt = existing.CreatedAt
		}
		if record.LastSeq < existing.LastSeq {
			record.LastSeq = existing.LastSeq
		}
		if record.UpdatedAt.IsZero() {
			record.UpdatedAt = existing.UpdatedAt
		}
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	s.sessions[record.SessionID] = record
	return s.persistSessionRegistryLocked()
}

func (s *FileEventStore) GetSessionRecord(ctx context.Context, sessionID sessionrt.SessionID) (sessionrt.SessionRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return sessionrt.SessionRecord{}, ctx.Err()
	default:
	}

	s.mu.RLock()
	record, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if !ok {
		return sessionrt.SessionRecord{}, sessionrt.ErrSessionNotFound
	}
	return record, nil
}

func (s *FileEventStore) ListSessions(ctx context.Context) ([]sessionrt.SessionRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.Lock()
	if err := s.reconcileSessionsFromEventsLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	records := make([]sessionrt.SessionRecord, 0, len(s.sessions))
	for _, record := range s.sessions {
		records = append(records, record)
	}
	s.mu.Unlock()

	sort.Slice(records, func(i, j int) bool {
		return records[i].SessionID < records[j].SessionID
	})
	return records, nil
}

func (s *FileEventStore) loadSessionRegistry() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	blob, err := os.ReadFile(s.sessionsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read session registry: %w", err)
	}
	if len(strings.TrimSpace(string(blob))) == 0 {
		return nil
	}

	var records []sessionrt.SessionRecord
	if err := json.Unmarshal(blob, &records); err != nil {
		return fmt.Errorf("decode session registry: %w", err)
	}
	for _, record := range records {
		s.sessions[record.SessionID] = record
	}
	return nil
}

func (s *FileEventStore) reconcileSessionsFromEvents() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileSessionsFromEventsLocked()
}

func (s *FileEventStore) reconcileSessionsFromEventsLocked() error {
	entries, err := os.ReadDir(s.eventsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read events directory: %w", err)
	}

	changed := false
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(s.eventsDir, entry.Name())
		events, err := readEventsFile(path)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			continue
		}
		sessionID := events[0].SessionID
		if sessionID == "" {
			continue
		}
		s.eventsCache[sessionID] = append([]sessionrt.Event(nil), events...)
		record := deriveSessionRecord(events)
		existing, ok := s.sessions[sessionID]
		if !ok || existing.LastSeq < record.LastSeq || existing.UpdatedAt.IsZero() {
			s.sessions[sessionID] = record
			changed = true
		}
	}

	if changed {
		if err := s.persistSessionRegistryLocked(); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileEventStore) persistSessionRegistryLocked() error {
	records := make([]sessionrt.SessionRecord, 0, len(s.sessions))
	for _, record := range s.sessions {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SessionID < records[j].SessionID
	})

	blob, err := json.Marshal(records)
	if err != nil {
		return fmt.Errorf("encode session registry: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("ensure store directory: %w", err)
	}
	tmp := s.sessionsPath + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o644); err != nil {
		return fmt.Errorf("write session registry temp file: %w", err)
	}
	if err := os.Rename(tmp, s.sessionsPath); err != nil {
		return fmt.Errorf("replace session registry: %w", err)
	}
	return nil
}

func (s *FileEventStore) loadSessionEvents(sessionID sessionrt.SessionID) ([]sessionrt.Event, error) {
	path := sessionrt.SessionJSONLPath(s.eventsDir, sessionID)
	events, err := readEventsFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, sessionrt.ErrSessionNotFound
		}
		return nil, err
	}
	if len(events) == 0 {
		return nil, sessionrt.ErrSessionNotFound
	}
	return events, nil
}

func appendJSONLEvent(eventsDir string, event sessionrt.Event) error {
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		return fmt.Errorf("create events directory: %w", err)
	}
	path := sessionrt.SessionJSONLPath(eventsDir, event.SessionID)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open events file: %w", err)
	}
	defer file.Close()

	blob, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := file.Write(append(blob, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

func readEventsFile(path string) ([]sessionrt.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	events := []sessionrt.Event{}
	decoder := json.NewDecoder(file)
	for {
		var event sessionrt.Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode event %s: %w", path, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func deriveSessionRecord(events []sessionrt.Event) sessionrt.SessionRecord {
	record := sessionrt.SessionRecord{
		SessionID:   events[0].SessionID,
		DisplayName: sessionrt.DisplayNameFromEvents(events),
		Status:      sessionrt.SessionActive,
		CreatedAt:   events[0].Timestamp,
		UpdatedAt:   events[0].Timestamp,
	}
	for _, event := range events {
		if event.Seq > record.LastSeq {
			record.LastSeq = event.Seq
		}
		if !event.Timestamp.IsZero() {
			record.UpdatedAt = event.Timestamp
		}
		record.Status = transitionStatus(record.Status, event)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = record.UpdatedAt
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = time.Now().UTC()
	}
	return record
}

func displayNameFromSessionCreatedEvent(event sessionrt.Event) (string, bool) {
	if event.Type != sessionrt.EventControl {
		return "", false
	}
	control, ok := event.Payload.(sessionrt.ControlPayload)
	if !ok {
		controlMap, ok := event.Payload.(map[string]any)
		if !ok {
			return "", false
		}
		action, _ := controlMap["action"].(string)
		if strings.TrimSpace(action) != sessionrt.ControlActionSessionCreated {
			return "", false
		}
		metadata, _ := controlMap["metadata"].(map[string]any)
		raw, _ := metadata["display_name"].(string)
		name := strings.TrimSpace(raw)
		return name, name != ""
	}
	if strings.TrimSpace(control.Action) != sessionrt.ControlActionSessionCreated {
		return "", false
	}
	raw, _ := control.Metadata["display_name"].(string)
	name := strings.TrimSpace(raw)
	return name, name != ""
}

func transitionStatus(current sessionrt.SessionStatus, event sessionrt.Event) sessionrt.SessionStatus {
	switch event.Type {
	case sessionrt.EventControl:
		ctrl, ok := event.Payload.(map[string]any)
		if !ok {
			// Some callers serialize payload structs; fall back to JSON path.
			blob, err := json.Marshal(event.Payload)
			if err != nil {
				return current
			}
			ctrl = map[string]any{}
			if err := json.Unmarshal(blob, &ctrl); err != nil {
				return current
			}
		}
		action, _ := ctrl["action"].(string)
		switch action {
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

func (s *FileEventStore) notifySubscribersLocked(event sessionrt.Event) {
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
