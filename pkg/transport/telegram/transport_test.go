package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestRenderTelegramMessageTextConvertsMarkdown(t *testing.T) {
	text := "Hey **Boston**\nUse `gopher` and [docs](https://example.com)\n\n```bash\necho hi\n```"
	rendered, parseMode := renderTelegramMessageText(text)
	if parseMode != "HTML" {
		t.Fatalf("parse mode = %q, want HTML", parseMode)
	}
	if !strings.Contains(rendered, "<b>Boston</b>") {
		t.Fatalf("rendered text missing bold conversion: %q", rendered)
	}
	if !strings.Contains(rendered, "<code>gopher</code>") {
		t.Fatalf("rendered text missing inline code conversion: %q", rendered)
	}
	if !strings.Contains(rendered, `<a href="https://example.com">docs</a>`) {
		t.Fatalf("rendered text missing link conversion: %q", rendered)
	}
	if !strings.Contains(rendered, "<pre><code>echo hi</code></pre>") {
		t.Fatalf("rendered text missing fenced code conversion: %q", rendered)
	}
}

func TestRenderTelegramMessageTextEscapesRawHTML(t *testing.T) {
	rendered, parseMode := renderTelegramMessageText("<b>raw</b> **safe**")
	if parseMode != "HTML" {
		t.Fatalf("parse mode = %q, want HTML", parseMode)
	}
	if !strings.Contains(rendered, "&lt;b&gt;raw&lt;/b&gt;") {
		t.Fatalf("rendered text should escape raw html: %q", rendered)
	}
	if !strings.Contains(rendered, "<b>safe</b>") {
		t.Fatalf("rendered text missing markdown bold conversion: %q", rendered)
	}
}

func TestSendMessageRetriesWithoutParseModeWhenTelegramRejectsEntities(t *testing.T) {
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		requests = append(requests, payload)
		w.Header().Set("Content-Type", "application/json")
		if len(requests) == 1 {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: can't parse entities"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tr, err := New(Options{
		BotToken:   "token",
		APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "telegram:777",
		Text:           "Hey **Boston**",
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if mode, _ := requests[0]["parse_mode"].(string); mode != "HTML" {
		t.Fatalf("first request parse_mode = %q, want HTML", mode)
	}
	if _, hasParseMode := requests[1]["parse_mode"]; hasParseMode {
		t.Fatalf("second request should not set parse_mode")
	}
	fallbackText, _ := requests[1]["text"].(string)
	if strings.Contains(fallbackText, "**") {
		t.Fatalf("fallback text still includes markdown delimiters: %q", fallbackText)
	}
}

func TestSendMessageDoesNotRetryOnNonParseTelegramError(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()

	tr, err := New(Options{
		BotToken:   "token",
		APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	err = tr.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "telegram:777",
		Text:           "Hey **Boston**",
	})
	if err == nil {
		t.Fatalf("expected SendMessage() error")
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

func TestSetCommandsRegistersTelegramCommands(t *testing.T) {
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/setMyCommands" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		payloads = append(payloads, payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	tr, err := New(Options{
		BotToken:   "token",
		APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	err = tr.SetCommands(context.Background(), []BotCommand{
		{Command: "/status", Description: "Show status"},
		{Command: "trace", Description: "Trace controls"},
		{Command: "", Description: "ignored"},
	})
	if err != nil {
		t.Fatalf("SetCommands() error: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("request count = %d, want 1", len(payloads))
	}
	rawCommands, ok := payloads[0]["commands"].([]any)
	if !ok {
		t.Fatalf("commands payload type = %T, want []any", payloads[0]["commands"])
	}
	if len(rawCommands) != 2 {
		t.Fatalf("commands count = %d, want 2", len(rawCommands))
	}
	first, ok := rawCommands[0].(map[string]any)
	if !ok {
		t.Fatalf("first command payload type = %T, want map[string]any", rawCommands[0])
	}
	if first["command"] != "status" {
		t.Fatalf("first command = %v, want status", first["command"])
	}
	if first["description"] != "Show status" {
		t.Fatalf("first description = %v, want Show status", first["description"])
	}
}
