package gateway

import (
	"strings"
	"sync"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type ConversationSessionMap struct {
	mu    sync.RWMutex
	items map[string]sessionrt.SessionID
}

func NewConversationSessionMap() *ConversationSessionMap {
	return &ConversationSessionMap{
		items: map[string]sessionrt.SessionID{},
	}
}

func (m *ConversationSessionMap) Get(conversationID string) (sessionrt.SessionID, bool) {
	key := strings.TrimSpace(conversationID)
	if key == "" {
		return "", false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessionID, ok := m.items[key]
	return sessionID, ok
}

func (m *ConversationSessionMap) Set(conversationID string, sessionID sessionrt.SessionID) {
	key := strings.TrimSpace(conversationID)
	if key == "" || strings.TrimSpace(string(sessionID)) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = sessionID
}
