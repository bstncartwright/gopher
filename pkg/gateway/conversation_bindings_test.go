package gateway

import (
	"path/filepath"
	"testing"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestFileConversationBindingStorePersistsBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	store, err := NewFileConversationBindingStore(path)
	if err != nil {
		t.Fatalf("NewFileConversationBindingStore() error: %v", err)
	}
	err = store.Set(ConversationBinding{
		ConversationID:   "!dm:one",
		ConversationName: "Writer Room",
		SessionID:        "sess-1",
		AgentID:          "agent:a",
		RecipientID:      "@agent:local",
		LastInboundEvent: "$evt-99",
		Mode:             ConversationModeDM,
	})
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	reloaded, err := NewFileConversationBindingStore(path)
	if err != nil {
		t.Fatalf("NewFileConversationBindingStore(reload) error: %v", err)
	}
	got, ok := reloaded.GetByConversation("!dm:one")
	if !ok {
		t.Fatalf("expected binding by conversation")
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("session id = %q, want sess-1", got.SessionID)
	}
	if got.Mode != ConversationModeDM {
		t.Fatalf("mode = %q, want dm", got.Mode)
	}
	if got.ConversationName != "Writer Room" {
		t.Fatalf("conversation name = %q, want Writer Room", got.ConversationName)
	}
	if got.LastInboundEvent != "$evt-99" {
		t.Fatalf("last inbound event = %q, want $evt-99", got.LastInboundEvent)
	}
}

func TestInMemoryConversationBindingStoreMaintainsOneToOnePairs(t *testing.T) {
	store := NewInMemoryConversationBindingStore()
	if err := store.Set(ConversationBinding{
		ConversationID: "!room:a",
		SessionID:      "sess-1",
		Mode:           ConversationModeDM,
	}); err != nil {
		t.Fatalf("first Set() error: %v", err)
	}
	if err := store.Set(ConversationBinding{
		ConversationID: "!room:b",
		SessionID:      "sess-1",
		Mode:           ConversationModeDelegation,
	}); err != nil {
		t.Fatalf("second Set() error: %v", err)
	}
	if _, ok := store.GetByConversation("!room:a"); ok {
		t.Fatalf("expected old conversation mapping to be removed for shared session id")
	}
	got, ok := store.GetBySession(sessionrt.SessionID("sess-1"))
	if !ok {
		t.Fatalf("expected session binding")
	}
	if got.ConversationID != "!room:b" {
		t.Fatalf("conversation id = %q, want !room:b", got.ConversationID)
	}
	if got.Mode != ConversationModeDelegation {
		t.Fatalf("mode = %q, want delegation", got.Mode)
	}
}
