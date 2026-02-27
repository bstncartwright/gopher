package gateway

import (
	"path/filepath"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestFileConversationBindingStorePersistsBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bindings.json")
	store, err := NewFileConversationBindingStore(path)
	if err != nil {
		t.Fatalf("NewFileConversationBindingStore() error: %v", err)
	}
	lastHeartbeatSentAt := time.Date(2026, 2, 18, 12, 34, 0, 0, time.UTC)
	err = store.Set(ConversationBinding{
		ConversationID:        "!dm:one",
		ConversationName:      "Writer Room",
		SessionID:             "sess-1",
		AgentID:               "agent:a",
		RecipientID:           "@agent:local",
		TraceConversationID:   "!trace:sess-1",
		TraceConversationName: "trace-sess-1",
		TraceMode:             TraceModeReadOnly,
		TraceRender:           TraceRenderCards,
		LastInboundEvent:      "$evt-99",
		LastHeartbeatText:     "disk is full",
		LastHeartbeatSentAt:   lastHeartbeatSentAt,
		Mode:                  ConversationModeDM,
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
	if got.TraceConversationID != "!trace:sess-1" {
		t.Fatalf("trace conversation id = %q, want !trace:sess-1", got.TraceConversationID)
	}
	if got.TraceMode != TraceModeReadOnly {
		t.Fatalf("trace mode = %q, want %q", got.TraceMode, TraceModeReadOnly)
	}
	if got.TraceRender != TraceRenderCards {
		t.Fatalf("trace render = %q, want %q", got.TraceRender, TraceRenderCards)
	}
	if got.LastHeartbeatText != "disk is full" {
		t.Fatalf("last heartbeat text = %q, want disk is full", got.LastHeartbeatText)
	}
	if !got.LastHeartbeatSentAt.Equal(lastHeartbeatSentAt) {
		t.Fatalf("last heartbeat sent at = %s, want %s", got.LastHeartbeatSentAt, lastHeartbeatSentAt)
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

func TestInMemoryConversationBindingStoreKeepsTraceFieldsForSameSession(t *testing.T) {
	store := NewInMemoryConversationBindingStore()
	if err := store.Set(ConversationBinding{
		ConversationID:      "!room:a",
		SessionID:           "sess-1",
		TraceConversationID: "!trace:one",
		TraceMode:           TraceModeReadOnly,
		TraceRender:         TraceRenderCards,
		Mode:                ConversationModeDM,
	}); err != nil {
		t.Fatalf("first Set() error: %v", err)
	}
	if err := store.Set(ConversationBinding{
		ConversationID: "!room:a",
		SessionID:      "sess-1",
		Mode:           ConversationModeDM,
	}); err != nil {
		t.Fatalf("second Set() error: %v", err)
	}
	got, ok := store.GetByConversation("!room:a")
	if !ok {
		t.Fatalf("expected conversation binding")
	}
	if got.TraceConversationID != "!trace:one" {
		t.Fatalf("trace conversation id = %q, want !trace:one", got.TraceConversationID)
	}
}

func TestInMemoryConversationBindingStorePreservesTraceFieldsWhenSessionChanges(t *testing.T) {
	store := NewInMemoryConversationBindingStore()
	if err := store.Set(ConversationBinding{
		ConversationID:      "!room:a",
		SessionID:           "sess-1",
		TraceConversationID: "!trace:one",
		TraceMode:           TraceModeReadOnly,
		TraceRender:         TraceRenderCards,
		Mode:                ConversationModeDM,
	}); err != nil {
		t.Fatalf("first Set() error: %v", err)
	}
	if err := store.Set(ConversationBinding{
		ConversationID: "!room:a",
		SessionID:      "sess-2",
		Mode:           ConversationModeDM,
	}); err != nil {
		t.Fatalf("second Set() error: %v", err)
	}
	got, ok := store.GetByConversation("!room:a")
	if !ok {
		t.Fatalf("expected conversation binding")
	}
	if got.TraceConversationID != "!trace:one" {
		t.Fatalf("trace conversation id = %q, want !trace:one", got.TraceConversationID)
	}
	if got.TraceMode != TraceModeReadOnly {
		t.Fatalf("trace mode = %q, want %q", got.TraceMode, TraceModeReadOnly)
	}
	if got.TraceRender != TraceRenderCards {
		t.Fatalf("trace render = %q, want %q", got.TraceRender, TraceRenderCards)
	}
}

func TestInMemoryConversationBindingStorePersistsTraceModeOff(t *testing.T) {
	store := NewInMemoryConversationBindingStore()
	if err := store.Set(ConversationBinding{
		ConversationID: "!room:a",
		SessionID:      "sess-1",
		TraceMode:      TraceModeOff,
		Mode:           ConversationModeDM,
	}); err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	got, ok := store.GetByConversation("!room:a")
	if !ok {
		t.Fatalf("expected conversation binding")
	}
	if got.TraceMode != TraceModeOff {
		t.Fatalf("trace mode = %q, want %q", got.TraceMode, TraceModeOff)
	}
}

func TestInMemoryConversationBindingStoreKeepsHeartbeatFieldsForSameSession(t *testing.T) {
	store := NewInMemoryConversationBindingStore()
	lastHeartbeatSentAt := time.Date(2026, 2, 20, 9, 10, 0, 0, time.UTC)
	if err := store.Set(ConversationBinding{
		ConversationID:      "!room:a",
		SessionID:           "sess-1",
		LastHeartbeatText:   "disk is full",
		LastHeartbeatSentAt: lastHeartbeatSentAt,
		Mode:                ConversationModeDM,
	}); err != nil {
		t.Fatalf("first Set() error: %v", err)
	}
	if err := store.Set(ConversationBinding{
		ConversationID: "!room:a",
		SessionID:      "sess-1",
		Mode:           ConversationModeDM,
	}); err != nil {
		t.Fatalf("second Set() error: %v", err)
	}
	got, ok := store.GetByConversation("!room:a")
	if !ok {
		t.Fatalf("expected conversation binding")
	}
	if got.LastHeartbeatText != "disk is full" {
		t.Fatalf("last heartbeat text = %q, want disk is full", got.LastHeartbeatText)
	}
	if !got.LastHeartbeatSentAt.Equal(lastHeartbeatSentAt) {
		t.Fatalf("last heartbeat sent at = %s, want %s", got.LastHeartbeatSentAt, lastHeartbeatSentAt)
	}
}
