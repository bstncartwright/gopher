package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type EventStore interface {
	Append(ctx context.Context, e Event) error
	List(ctx context.Context, sessionID SessionID) ([]Event, error)
	Stream(ctx context.Context, sessionID SessionID) (<-chan Event, error)
}

type SessionRecord struct {
	SessionID   SessionID     `json:"session_id"`
	DisplayName string        `json:"display_name,omitempty"`
	Status      SessionStatus `json:"status"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	LastSeq     uint64        `json:"last_seq"`
	InFlight    bool          `json:"in_flight"`
}

type SessionRegistryStore interface {
	UpsertSession(ctx context.Context, record SessionRecord) error
	GetSessionRecord(ctx context.Context, sessionID SessionID) (SessionRecord, error)
	ListSessions(ctx context.Context) ([]SessionRecord, error)
}

type InMemoryEventStoreOptions struct {
	JSONLDir     string
	StreamBuffer int
}

type InMemoryEventStore struct {
	mu          sync.RWMutex
	events      map[SessionID][]Event
	sessions    map[SessionID]SessionRecord
	subscribers map[SessionID]map[uint64]chan Event
	nextSubID   uint64
	jsonlDir    string
	streamBuf   int
}

func NewInMemoryEventStore(opts InMemoryEventStoreOptions) *InMemoryEventStore {
	streamBuf := opts.StreamBuffer
	if streamBuf <= 0 {
		streamBuf = 32
	}
	return &InMemoryEventStore{
		events:      make(map[SessionID][]Event),
		sessions:    make(map[SessionID]SessionRecord),
		subscribers: make(map[SessionID]map[uint64]chan Event),
		jsonlDir:    strings.TrimSpace(opts.JSONLDir),
		streamBuf:   streamBuf,
	}
}

func (s *InMemoryEventStore) Append(ctx context.Context, e Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	previousRecord, hadRecord := s.sessions[e.SessionID]
	s.events[e.SessionID] = append(s.events[e.SessionID], e)
	s.updateSessionRecordLocked(e.SessionID, func(record SessionRecord) SessionRecord {
		if record.SessionID == "" {
			record.SessionID = e.SessionID
		}
		if displayName, ok := displayNameFromSessionCreatedEvent(e); ok {
			record.DisplayName = displayName
		}
		if e.Seq > record.LastSeq {
			record.LastSeq = e.Seq
		}
		if record.CreatedAt.IsZero() {
			record.CreatedAt = e.Timestamp
		}
		if !e.Timestamp.IsZero() {
			record.UpdatedAt = e.Timestamp
		}
		record.Status = applyStatusTransition(record.Status, e)
		return record
	})
	if s.jsonlDir != "" {
		if err := appendJSONLEvent(s.jsonlDir, e); err != nil {
			events := s.events[e.SessionID]
			s.events[e.SessionID] = events[:len(events)-1]
			if hadRecord {
				s.sessions[e.SessionID] = previousRecord
			} else {
				delete(s.sessions, e.SessionID)
			}
			return err
		}
	}

	subs := s.subscribers[e.SessionID]
	for subID, ch := range subs {
		select {
		case ch <- e:
		default:
			close(ch)
			delete(subs, subID)
		}
	}

	return nil
}

func (s *InMemoryEventStore) UpsertSession(ctx context.Context, record SessionRecord) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if strings.TrimSpace(string(record.SessionID)) == "" {
		return fmt.Errorf("%w: session ID is required", ErrInvalidSession)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.sessions[record.SessionID]
	if ok {
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
	s.sessions[record.SessionID] = record
	return nil
}

func (s *InMemoryEventStore) GetSessionRecord(ctx context.Context, sessionID SessionID) (SessionRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return SessionRecord{}, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.sessions[sessionID]
	if !ok {
		return SessionRecord{}, ErrSessionNotFound
	}
	return record, nil
}

func (s *InMemoryEventStore) ListSessions(ctx context.Context) ([]SessionRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]SessionRecord, 0, len(s.sessions))
	for _, record := range s.sessions {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SessionID < records[j].SessionID
	})
	return records, nil
}

func (s *InMemoryEventStore) List(ctx context.Context, sessionID SessionID) ([]Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	events, ok := s.events[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}
	out := make([]Event, len(events))
	copy(out, events)
	return out, nil
}

func (s *InMemoryEventStore) Stream(ctx context.Context, sessionID SessionID) (<-chan Event, error) {
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
	if _, ok := s.events[sessionID]; !ok {
		return nil, ErrSessionNotFound
	}

	s.nextSubID++
	subID := s.nextSubID
	ch := make(chan Event, s.streamBuf)
	if s.subscribers[sessionID] == nil {
		s.subscribers[sessionID] = make(map[uint64]chan Event)
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

func SessionJSONLPath(dir string, sessionID SessionID) string {
	return filepath.Join(dir, fmt.Sprintf("%s.jsonl", sanitizeSessionFilePart(string(sessionID))))
}

func appendJSONLEvent(dir string, e Event) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create jsonl directory: %w", err)
	}
	path := SessionJSONLPath(dir, e.SessionID)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open jsonl file: %w", err)
	}
	defer f.Close()

	blob, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := f.Write(append(blob, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

func sanitizeSessionFilePart(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "..", "_")
	cleaned := replacer.Replace(strings.TrimSpace(value))
	if cleaned == "" {
		return "session"
	}
	return cleaned
}

func (s *InMemoryEventStore) updateSessionRecordLocked(sessionID SessionID, fn func(SessionRecord) SessionRecord) {
	record := s.sessions[sessionID]
	record.SessionID = sessionID
	record = fn(record)
	s.sessions[sessionID] = record
}
