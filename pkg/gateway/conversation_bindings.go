package gateway

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type ConversationMode string

const (
	ConversationModeDM         ConversationMode = "dm"
	ConversationModeDelegation ConversationMode = "delegation"
)

type ConversationBinding struct {
	ConversationID   string              `json:"conversation_id"`
	ConversationName string              `json:"conversation_name,omitempty"`
	SessionID        sessionrt.SessionID `json:"session_id"`
	AgentID          sessionrt.ActorID   `json:"agent_id,omitempty"`
	RecipientID      string              `json:"recipient_id,omitempty"`
	Mode             ConversationMode    `json:"mode"`
	CreatedAt        time.Time           `json:"created_at"`
	UpdatedAt        time.Time           `json:"updated_at"`
}

type ConversationBindingStore interface {
	GetByConversation(conversationID string) (ConversationBinding, bool)
	GetBySession(sessionID sessionrt.SessionID) (ConversationBinding, bool)
	Set(binding ConversationBinding) error
	List() []ConversationBinding
}

type conversationBindingDisk struct {
	Bindings []ConversationBinding `json:"bindings"`
}

type FileConversationBindingStore struct {
	mu       sync.RWMutex
	filePath string
	items    map[string]ConversationBinding
}

func NewFileConversationBindingStore(filePath string) (*FileConversationBindingStore, error) {
	path := strings.TrimSpace(filePath)
	if path == "" {
		return nil, fmt.Errorf("conversation binding store file path is required")
	}
	store := &FileConversationBindingStore{
		filePath: path,
		items:    map[string]ConversationBinding{},
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *FileConversationBindingStore) GetByConversation(conversationID string) (ConversationBinding, bool) {
	key := strings.TrimSpace(conversationID)
	if key == "" {
		return ConversationBinding{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[key]
	if !ok {
		return ConversationBinding{}, false
	}
	return cloneConversationBinding(item), true
}

func (s *FileConversationBindingStore) GetBySession(sessionID sessionrt.SessionID) (ConversationBinding, bool) {
	key := strings.TrimSpace(string(sessionID))
	if key == "" {
		return ConversationBinding{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if strings.TrimSpace(string(item.SessionID)) == key {
			return cloneConversationBinding(item), true
		}
	}
	return ConversationBinding{}, false
}

func (s *FileConversationBindingStore) Set(binding ConversationBinding) error {
	normalized, err := normalizeConversationBinding(binding)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.items[normalized.ConversationID]; ok {
		normalized.CreatedAt = existing.CreatedAt
		if normalized.ConversationName == "" {
			normalized.ConversationName = existing.ConversationName
		}
	}
	for conversationID, existing := range s.items {
		if conversationID == normalized.ConversationID {
			continue
		}
		if existing.SessionID == normalized.SessionID {
			delete(s.items, conversationID)
		}
	}
	s.items[normalized.ConversationID] = normalized
	return s.persistLocked()
}

func (s *FileConversationBindingStore) List() []ConversationBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConversationBinding, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, cloneConversationBinding(item))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ConversationID < out[j].ConversationID
	})
	return out
}

func (s *FileConversationBindingStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read conversation binding store: %w", err)
	}
	if len(strings.TrimSpace(string(blob))) == 0 {
		return nil
	}
	var disk conversationBindingDisk
	if err := json.Unmarshal(blob, &disk); err != nil {
		return fmt.Errorf("decode conversation binding store: %w", err)
	}
	for _, item := range disk.Bindings {
		normalized, err := normalizeConversationBinding(item)
		if err != nil {
			continue
		}
		s.items[normalized.ConversationID] = normalized
	}
	return nil
}

func (s *FileConversationBindingStore) persistLocked() error {
	items := make([]ConversationBinding, 0, len(s.items))
	for _, item := range s.items {
		items = append(items, cloneConversationBinding(item))
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ConversationID < items[j].ConversationID
	})
	blob, err := json.Marshal(conversationBindingDisk{Bindings: items})
	if err != nil {
		return fmt.Errorf("encode conversation binding store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.filePath), 0o755); err != nil {
		return fmt.Errorf("create conversation binding directory: %w", err)
	}
	tmpPath := s.filePath + ".tmp"
	if err := os.WriteFile(tmpPath, blob, 0o644); err != nil {
		return fmt.Errorf("write conversation binding temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.filePath); err != nil {
		return fmt.Errorf("replace conversation binding file: %w", err)
	}
	return nil
}

type InMemoryConversationBindingStore struct {
	mu    sync.RWMutex
	items map[string]ConversationBinding
}

func NewInMemoryConversationBindingStore() *InMemoryConversationBindingStore {
	return &InMemoryConversationBindingStore{
		items: map[string]ConversationBinding{},
	}
}

func (s *InMemoryConversationBindingStore) GetByConversation(conversationID string) (ConversationBinding, bool) {
	key := strings.TrimSpace(conversationID)
	if key == "" {
		return ConversationBinding{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[key]
	if !ok {
		return ConversationBinding{}, false
	}
	return cloneConversationBinding(item), true
}

func (s *InMemoryConversationBindingStore) GetBySession(sessionID sessionrt.SessionID) (ConversationBinding, bool) {
	key := strings.TrimSpace(string(sessionID))
	if key == "" {
		return ConversationBinding{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if strings.TrimSpace(string(item.SessionID)) == key {
			return cloneConversationBinding(item), true
		}
	}
	return ConversationBinding{}, false
}

func (s *InMemoryConversationBindingStore) Set(binding ConversationBinding) error {
	normalized, err := normalizeConversationBinding(binding)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.items[normalized.ConversationID]; ok {
		normalized.CreatedAt = existing.CreatedAt
		if normalized.ConversationName == "" {
			normalized.ConversationName = existing.ConversationName
		}
	}
	for conversationID, existing := range s.items {
		if conversationID == normalized.ConversationID {
			continue
		}
		if existing.SessionID == normalized.SessionID {
			delete(s.items, conversationID)
		}
	}
	s.items[normalized.ConversationID] = normalized
	return nil
}

func (s *InMemoryConversationBindingStore) List() []ConversationBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ConversationBinding, 0, len(s.items))
	for _, item := range s.items {
		out = append(out, cloneConversationBinding(item))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ConversationID < out[j].ConversationID
	})
	return out
}

func normalizeConversationBinding(binding ConversationBinding) (ConversationBinding, error) {
	conversationID := strings.TrimSpace(binding.ConversationID)
	if conversationID == "" {
		return ConversationBinding{}, fmt.Errorf("conversation id is required")
	}
	conversationName := strings.TrimSpace(binding.ConversationName)
	sessionID := sessionrt.SessionID(strings.TrimSpace(string(binding.SessionID)))
	if strings.TrimSpace(string(sessionID)) == "" {
		return ConversationBinding{}, fmt.Errorf("session id is required")
	}
	agentID := sessionrt.ActorID(strings.TrimSpace(string(binding.AgentID)))
	recipientID := strings.TrimSpace(binding.RecipientID)
	mode := normalizeConversationMode(binding.Mode)

	now := time.Now().UTC()
	createdAt := binding.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := binding.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = now
	}
	return ConversationBinding{
		ConversationID:   conversationID,
		ConversationName: conversationName,
		SessionID:        sessionID,
		AgentID:          agentID,
		RecipientID:      recipientID,
		Mode:             mode,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
	}, nil
}

func normalizeConversationMode(mode ConversationMode) ConversationMode {
	switch mode {
	case ConversationModeDelegation:
		return ConversationModeDelegation
	default:
		return ConversationModeDM
	}
}

func cloneConversationBinding(in ConversationBinding) ConversationBinding {
	out := in
	out.ConversationID = strings.TrimSpace(out.ConversationID)
	out.ConversationName = strings.TrimSpace(out.ConversationName)
	out.SessionID = sessionrt.SessionID(strings.TrimSpace(string(out.SessionID)))
	out.AgentID = sessionrt.ActorID(strings.TrimSpace(string(out.AgentID)))
	out.RecipientID = strings.TrimSpace(out.RecipientID)
	out.Mode = normalizeConversationMode(out.Mode)
	out.CreatedAt = out.CreatedAt.UTC()
	out.UpdatedAt = out.UpdatedAt.UTC()
	return out
}
