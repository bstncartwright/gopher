package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type EventStore interface {
	Append(ctx context.Context, e Event) error
	List(ctx context.Context, sessionID SessionID) ([]Event, error)
	Stream(ctx context.Context, sessionID SessionID) (<-chan Event, error)
}

type InMemoryEventStoreOptions struct {
	JSONLDir     string
	StreamBuffer int
}

type InMemoryEventStore struct {
	mu          sync.RWMutex
	events      map[SessionID][]Event
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

	s.events[e.SessionID] = append(s.events[e.SessionID], e)
	if s.jsonlDir != "" {
		if err := appendJSONLEvent(s.jsonlDir, e); err != nil {
			events := s.events[e.SessionID]
			s.events[e.SessionID] = events[:len(events)-1]
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
