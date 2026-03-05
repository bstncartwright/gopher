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
	threaded, err := parseConversationChatID("telegram:12345:9")
	if err != nil {
		t.Fatalf("parseConversationChatID(threaded) error: %v", err)
	}
	if threaded != "12345" {
		t.Fatalf("threaded chat id = %q, want 12345", threaded)
	}
	target, err := parseConversationTarget("telegram:12345:9")
	if err != nil {
		t.Fatalf("parseConversationTarget() error: %v", err)
	}
	if target.ChatID != "12345" || target.MessageThreadID != 9 {
		t.Fatalf("parsed target = %#v, want chat 12345 thread 9", target)
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
			MessageID: 733,
			From:      &telegramUser{ID: 501, Username: "boss"},
			Chat:      &telegramChat{ID: 777},
			Text:      "what's going on?",
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
	if got.EventID != "733" {
		t.Fatalf("event id = %q, want 733", got.EventID)
	}
	if got.Text != "what's going on?" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestDispatchEventMapsInboundFieldsWithMessageThread(t *testing.T) {
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
		UpdateID: 34,
		Message: &telegramMessage{
			MessageID:       734,
			MessageThreadID: 11,
			From:            &telegramUser{ID: 501, Username: "boss"},
			Chat:            &telegramChat{ID: 777},
			Text:            "status?",
		},
	}
	if err := tr.dispatchEvent(context.Background(), event); err != nil {
		t.Fatalf("dispatchEvent() error: %v", err)
	}
	if got.ConversationID != "telegram:777:11" {
		t.Fatalf("conversation id = %q, want telegram:777:11", got.ConversationID)
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

func TestRenderTelegramMessageTextConvertsMarkdownHeaders(t *testing.T) {
	text := "### Gmail (last 24h)\n- **Total unread:** 29\n\n```text\n### keep literal\n```"
	rendered, parseMode := renderTelegramMessageText(text)
	if parseMode != "HTML" {
		t.Fatalf("parse mode = %q, want HTML", parseMode)
	}
	if !strings.Contains(rendered, "<b>Gmail (last 24h)</b>") {
		t.Fatalf("rendered text missing markdown heading conversion: %q", rendered)
	}
	if !strings.Contains(rendered, "- <b>Total unread:</b> 29") {
		t.Fatalf("rendered text missing list/bold conversion: %q", rendered)
	}
	if !strings.Contains(rendered, "<pre><code>### keep literal</code></pre>") {
		t.Fatalf("rendered text should preserve heading markers inside code blocks: %q", rendered)
	}
}

func TestStripCommonMarkdownFormattingRemovesHeadingMarkers(t *testing.T) {
	text := "### Bottom line\n- **Total unread:** 29\n[docs](https://example.com)"
	stripped := stripCommonMarkdownFormatting(text)
	if strings.Contains(stripped, "###") {
		t.Fatalf("stripped text should not include markdown heading markers: %q", stripped)
	}
	if strings.Contains(stripped, "**") {
		t.Fatalf("stripped text should not include markdown bold delimiters: %q", stripped)
	}
	if !strings.Contains(stripped, "Bottom line") {
		t.Fatalf("stripped text missing heading text: %q", stripped)
	}
	if !strings.Contains(stripped, "docs (https://example.com)") {
		t.Fatalf("stripped text missing markdown link fallback: %q", stripped)
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

func TestSendMessageIncludesMessageThreadID(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessage" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode sendMessage payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
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
		ConversationID: "telegram:777:8",
		Text:           "threaded reply",
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if gotThread, _ := payload["message_thread_id"].(float64); gotThread != 8 {
		t.Fatalf("message_thread_id = %v, want 8", payload["message_thread_id"])
	}
}

func TestSendMessageDraftCallsTelegramAPI(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendMessageDraft" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode sendMessageDraft payload: %v", err)
		}
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
	if err := tr.SendMessageDraft(context.Background(), "telegram:777:8", 42, "building response"); err != nil {
		t.Fatalf("SendMessageDraft() error: %v", err)
	}
	if gotDraftID, _ := payload["draft_id"].(float64); gotDraftID != 42 {
		t.Fatalf("draft_id = %v, want 42", payload["draft_id"])
	}
	if gotThread, _ := payload["message_thread_id"].(float64); gotThread != 8 {
		t.Fatalf("message_thread_id = %v, want 8", payload["message_thread_id"])
	}
}

func TestSendMessageDraftDisablesOnUnknownMethod(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":404,"description":"Bad Request: method not found"}`))
	}))
	defer server.Close()

	tr, err := New(Options{
		BotToken:   "token",
		APIBaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.SendMessageDraft(context.Background(), "telegram:777", 1, "one"); err != nil {
		t.Fatalf("SendMessageDraft(first) error: %v", err)
	}
	if err := tr.SendMessageDraft(context.Background(), "telegram:777", 2, "two"); err != nil {
		t.Fatalf("SendMessageDraft(second) error: %v", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

func TestSendReactionCallsTelegramAPI(t *testing.T) {
	var requestPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/setMessageReaction" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
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
	if err := tr.SendReaction(context.Background(), transport.OutboundReaction{
		ConversationID: "telegram:777",
		TargetEventID:  "42",
		Emoji:          "👍",
	}); err != nil {
		t.Fatalf("SendReaction() error: %v", err)
	}
	if gotChatID, _ := requestPayload["chat_id"].(string); gotChatID != "777" {
		t.Fatalf("chat_id = %q, want 777", gotChatID)
	}
	if gotMessageID, _ := requestPayload["message_id"].(float64); gotMessageID != 42 {
		t.Fatalf("message_id = %v, want 42", requestPayload["message_id"])
	}
	reactions, ok := requestPayload["reaction"].([]any)
	if !ok || len(reactions) != 1 {
		t.Fatalf("reaction payload malformed: %#v", requestPayload["reaction"])
	}
	first, ok := reactions[0].(map[string]any)
	if !ok {
		t.Fatalf("reaction entry malformed: %#v", reactions[0])
	}
	if gotEmoji, _ := first["emoji"].(string); gotEmoji != "👍" {
		t.Fatalf("emoji = %q, want 👍", gotEmoji)
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

func TestSendMessageAttachmentRoutesImageToSendPhoto(t *testing.T) {
	attachmentPath := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(attachmentPath, []byte("png-bytes"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendPhoto" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		if chatID := r.FormValue("chat_id"); chatID != "777" {
			t.Fatalf("chat_id = %q, want 777", chatID)
		}
		file, _, err := r.FormFile("photo")
		if err != nil {
			t.Fatalf("photo form file missing: %v", err)
		}
		_ = file.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tr, err := New(Options{BotToken: "token", APIBaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "telegram:777",
		Attachments: []transport.OutboundAttachment{{
			Path:     attachmentPath,
			MIMEType: "image/png",
		}},
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
}

func TestSendMessageAttachmentRoutesUnknownTypeToSendDocument(t *testing.T) {
	attachmentPath := filepath.Join(t.TempDir(), "archive.bin")
	if err := os.WriteFile(attachmentPath, []byte("binary"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/sendDocument" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		file, _, err := r.FormFile("document")
		if err != nil {
			t.Fatalf("document form file missing: %v", err)
		}
		_ = file.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tr, err := New(Options{BotToken: "token", APIBaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "telegram:777",
		Attachments: []transport.OutboundAttachment{{
			Path: attachmentPath,
		}},
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
}

func TestSendMessageWithTextAndAttachmentSendsTextThenMedia(t *testing.T) {
	attachmentPath := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(attachmentPath, []byte("jpg"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/bottoken/sendMessage":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode sendMessage payload: %v", err)
			}
		case "/bottoken/sendPhoto":
			if err := r.ParseMultipartForm(4 << 20); err != nil {
				t.Fatalf("parse multipart form: %v", err)
			}
			file, _, err := r.FormFile("photo")
			if err != nil {
				t.Fatalf("photo form file missing: %v", err)
			}
			_ = file.Close()
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tr, err := New(Options{BotToken: "token", APIBaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "telegram:777",
		Text:           "done",
		Attachments: []transport.OutboundAttachment{{
			Path:     attachmentPath,
			MIMEType: "image/jpeg",
		}},
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("request count = %d, want 2", len(paths))
	}
	if paths[0] != "/bottoken/sendMessage" || paths[1] != "/bottoken/sendPhoto" {
		t.Fatalf("request order = %#v, want sendMessage then sendPhoto", paths)
	}
}

func TestSendMessageAttachmentOnlySkipsTextRequest(t *testing.T) {
	attachmentPath := filepath.Join(t.TempDir(), "voice.ogg")
	if err := os.WriteFile(attachmentPath, []byte("ogg"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path != "/bottoken/sendVoice" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(4 << 20); err != nil {
			t.Fatalf("parse multipart form: %v", err)
		}
		file, _, err := r.FormFile("voice")
		if err != nil {
			t.Fatalf("voice form file missing: %v", err)
		}
		_ = file.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	tr, err := New(Options{BotToken: "token", APIBaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "telegram:777",
		Attachments: []transport.OutboundAttachment{{
			Path:     attachmentPath,
			MIMEType: "audio/ogg",
		}},
	}); err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/bottoken/sendVoice" {
		t.Fatalf("request paths = %#v, want only sendVoice", paths)
	}
}

func TestSendMessageAttachmentMissingFileReturnsError(t *testing.T) {
	tr, err := New(Options{BotToken: "token", APIBaseURL: "https://example.invalid"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	err = tr.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: "telegram:777",
		Attachments: []transport.OutboundAttachment{{
			Path: "/tmp/does-not-exist.bin",
		}},
	})
	if err == nil {
		t.Fatalf("expected attachment read error")
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

func TestSetWebhookCallsTelegramAPI(t *testing.T) {
	var requestPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/setWebhook" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
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
	if err := tr.SetWebhook(context.Background(), "https://example.ts.net/_gopher/telegram/webhook", "shared-secret"); err != nil {
		t.Fatalf("SetWebhook() error: %v", err)
	}
	if gotURL, _ := requestPayload["url"].(string); gotURL != "https://example.ts.net/_gopher/telegram/webhook" {
		t.Fatalf("setWebhook url = %q", gotURL)
	}
	if gotSecret, _ := requestPayload["secret_token"].(string); gotSecret != "shared-secret" {
		t.Fatalf("setWebhook secret_token = %q", gotSecret)
	}
}

func TestDeleteWebhookCallsTelegramAPI(t *testing.T) {
	var requestPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/deleteWebhook" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
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
	if err := tr.DeleteWebhook(context.Background(), true); err != nil {
		t.Fatalf("DeleteWebhook() error: %v", err)
	}
	if gotDropPending, ok := requestPayload["drop_pending_updates"].(bool); !ok || !gotDropPending {
		t.Fatalf("deleteWebhook drop_pending_updates = %#v", requestPayload["drop_pending_updates"])
	}
}

func TestHandleWebhookUpdateDispatchesEvent(t *testing.T) {
	tr, err := New(Options{BotToken: "token"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	var got transport.InboundMessage
	tr.SetInboundHandler(func(_ context.Context, inbound transport.InboundMessage) error {
		got = inbound
		return nil
	})
	payload := []byte(`{
		"update_id": 99,
		"message": {
			"message_id": 199,
			"from": {"id": 501},
			"chat": {"id": 777, "title": "Ops"},
			"text": "hi from webhook"
		}
	}`)
	if err := tr.HandleWebhookUpdate(context.Background(), payload); err != nil {
		t.Fatalf("HandleWebhookUpdate() error: %v", err)
	}
	if got.EventID != "199" {
		t.Fatalf("event id = %q, want 199", got.EventID)
	}
	if got.ConversationID != "telegram:777" {
		t.Fatalf("conversation id = %q, want telegram:777", got.ConversationID)
	}
}

func TestHandleWebhookUpdateRejectsInvalidJSON(t *testing.T) {
	tr, err := New(Options{BotToken: "token"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if err := tr.HandleWebhookUpdate(context.Background(), []byte(`{`)); err == nil {
		t.Fatalf("expected HandleWebhookUpdate() error")
	}
}
