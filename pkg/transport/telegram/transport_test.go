package telegram

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bstncartwright/gopher/pkg/transport"
)

func TestParseConversationChatID(t *testing.T) {
	got, err := parseConversationChatID("telegram:12345")
	if err != nil {
		t.Fatalf("parseConversationChatID() error: %v", err)
	}
	if got != "12345" {
		t.Fatalf("chat id = %q, want 12345", got)
	}
	if _, err := parseConversationChatID("!trace:one"); err == nil {
		t.Fatalf("expected error for non-telegram conversation id")
	}
}

func TestOffsetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telegram", "offset.json")
	tr, err := New(Options{
		BotToken:   "token",
		OffsetPath: path,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.persistOffset(42); err != nil {
		t.Fatalf("persistOffset() error: %v", err)
	}
	got, err := tr.loadOffset()
	if err != nil {
		t.Fatalf("loadOffset() error: %v", err)
	}
	if got != 42 {
		t.Fatalf("offset = %d, want 42", got)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read offset file: %v", err)
	}
	if len(blob) == 0 {
		t.Fatalf("expected offset file to be written")
	}
}

func TestDispatchEventAppliesAuthFilters(t *testing.T) {
	tr, err := New(Options{
		BotToken:      "token",
		AllowedUserID: "1001",
		AllowedChatID: "2002",
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	called := 0
	tr.SetInboundHandler(func(context.Context, transport.InboundMessage) error {
		called++
		return nil
	})

	ignored := telegramEvent{
		UpdateID: 1,
		Message: &telegramMessage{
			From: &telegramUser{ID: 9999},
			Chat: &telegramChat{ID: 2002},
			Text: "hello",
		},
	}
	if err := tr.dispatchEvent(context.Background(), ignored); err != nil {
		t.Fatalf("dispatchEvent(ignored) error: %v", err)
	}
	if called != 0 {
		t.Fatalf("handler called for unauthorized event")
	}

	allowed := telegramEvent{
		UpdateID: 2,
		Message: &telegramMessage{
			From: &telegramUser{ID: 1001},
			Chat: &telegramChat{ID: 2002, Title: "CEO"},
			Text: "status",
		},
	}
	if err := tr.dispatchEvent(context.Background(), allowed); err != nil {
		t.Fatalf("dispatchEvent(allowed) error: %v", err)
	}
	if called != 1 {
		t.Fatalf("handler calls = %d, want 1", called)
	}
}

func TestDispatchEventMapsInboundFields(t *testing.T) {
	tr, err := New(Options{BotToken: "token"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	var got transport.InboundMessage
	tr.SetInboundHandler(func(_ context.Context, inbound transport.InboundMessage) error {
		got = inbound
		return nil
	})
	event := telegramEvent{
		UpdateID: 33,
		Message: &telegramMessage{
			From: &telegramUser{ID: 501, Username: "boss"},
			Chat: &telegramChat{ID: 777},
			Text: "what's going on?",
		},
	}
	if err := tr.dispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatchEvent() error: %v", err)
	}
	if got.ConversationID != "telegram:777" {
		t.Fatalf("conversation id = %q, want telegram:777", got.ConversationID)
	}
	if got.SenderID != "telegram-user:501" {
		t.Fatalf("sender id = %q, want telegram-user:501", got.SenderID)
	}
	if got.EventID != "33" {
		t.Fatalf("event id = %q, want 33", got.EventID)
	}
	if got.Text != "what's going on?" {
		t.Fatalf("text = %q", got.Text)
	}
}
