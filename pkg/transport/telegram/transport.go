package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/transport"
	"log/slog"
)

const (
	defaultPollInterval = 2 * time.Second
	defaultPollTimeout  = 30 * time.Second
	defaultHTTPTimeout  = 45 * time.Second
)

type Options struct {
	BotToken      string
	PollInterval  time.Duration
	PollTimeout   time.Duration
	AllowedUserID string
	AllowedChatID string
	OffsetPath    string
	APIBaseURL    string
}

type Transport struct {
	botToken      string
	pollInterval  time.Duration
	pollTimeout   time.Duration
	allowedUserID string
	allowedChatID string
	offsetPath    string
	apiBaseURL    string

	client *http.Client

	handlerMu sync.RWMutex
	handler   transport.InboundHandler

	runMu     sync.Mutex
	runCancel context.CancelFunc
}

type pollResponse struct {
	OK     bool            `json:"ok"`
	Result []telegramEvent `json:"result"`
}

type telegramEvent struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramMessage struct {
	MessageID int64         `json:"message_id"`
	From      *telegramUser `json:"from"`
	Chat      *telegramChat `json:"chat"`
	Date      int64         `json:"date"`
	Text      string        `json:"text"`
}

type telegramUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type telegramChat struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

type sendResponse struct {
	OK bool `json:"ok"`
}

func New(opts Options) (*Transport, error) {
	token := strings.TrimSpace(opts.BotToken)
	if token == "" {
		return nil, fmt.Errorf("telegram bot token is required")
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}
	pollTimeout := opts.PollTimeout
	if pollTimeout <= 0 {
		pollTimeout = defaultPollTimeout
	}
	apiBaseURL := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
	if apiBaseURL == "" {
		apiBaseURL = "https://api.telegram.org"
	}
	slog.Info(
		"telegram transport: initializing",
		"bot_token_tail", tokenSuffix(token),
		"poll_interval", pollInterval,
		"poll_timeout", pollTimeout,
		"api_base_url", apiBaseURL,
		"allowed_user_id_set", opts.AllowedUserID != "",
		"allowed_chat_id_set", opts.AllowedChatID != "",
		"offset_path", strings.TrimSpace(opts.OffsetPath),
	)
	return &Transport{
		botToken:      token,
		pollInterval:  pollInterval,
		pollTimeout:   pollTimeout,
		allowedUserID: strings.TrimSpace(opts.AllowedUserID),
		allowedChatID: strings.TrimSpace(opts.AllowedChatID),
		offsetPath:    strings.TrimSpace(opts.OffsetPath),
		apiBaseURL:    apiBaseURL,
		client:        &http.Client{Timeout: defaultHTTPTimeout},
	}, nil
}

func (t *Transport) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	t.runMu.Lock()
	if t.runCancel != nil {
		t.runMu.Unlock()
		cancel()
		slog.Info("telegram transport: start ignored; already running")
		return nil
	}
	t.runCancel = cancel
	t.runMu.Unlock()
	defer t.clearRunCancel()

	slog.Info("telegram transport: starting poll loop", "api_base_url", t.apiBaseURL)
	offset, err := t.loadOffset()
	if err != nil {
		slog.Error("telegram transport: failed to load offset", "error", err)
		return err
	}
	slog.Info("telegram transport: loaded offset", "offset", offset)
	for {
		if runCtx.Err() != nil {
			return nil
		}
		nextOffset, err := t.pollAndDispatch(runCtx, offset)
		if err != nil {
			slog.Error("telegram transport: poll failed", "error", err, "offset", offset, "next_offset", nextOffset)
			return err
		}
		if nextOffset > offset {
			offset = nextOffset
			slog.Debug("telegram transport: advancing offset", "next_offset", offset)
			if err := t.persistOffset(offset); err != nil {
				slog.Error("telegram transport: failed to persist offset", "error", err, "offset", offset)
				return err
			}
			slog.Debug("telegram transport: persisted offset", "offset", offset)
		}
		timer := time.NewTimer(t.pollInterval)
		select {
		case <-runCtx.Done():
			timer.Stop()
			slog.Info("telegram transport: poll loop stopped")
			return nil
		case <-timer.C:
			slog.Debug("telegram transport: poll interval elapsed", "poll_interval", t.pollInterval)
		}
	}
}

func (t *Transport) Stop() error {
	t.runMu.Lock()
	cancel := t.runCancel
	t.runCancel = nil
	t.runMu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (t *Transport) SetInboundHandler(handler transport.InboundHandler) {
	t.handlerMu.Lock()
	t.handler = handler
	t.handlerMu.Unlock()
}

func (t *Transport) SendMessage(ctx context.Context, message transport.OutboundMessage) error {
	chatID, err := parseConversationChatID(message.ConversationID)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(message.Text)
	if text == "" && len(message.Attachments) > 0 {
		text = formatAttachmentNotice(message.Attachments)
	}
	if text == "" {
		slog.Debug("telegram transport: send message skipped due empty text", "conversation_id", message.ConversationID, "attachments", len(message.Attachments))
		return nil
	}
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	slog.Info("telegram transport: sending message", "conversation_id", message.ConversationID, "chat_id", chatID, "attachment_count", len(message.Attachments))
	return t.sendAPI(ctx, "sendMessage", payload)
}

func (t *Transport) SendTyping(ctx context.Context, conversationID string, typing bool) error {
	if !typing {
		return nil
	}
	chatID, err := parseConversationChatID(conversationID)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id": chatID,
		"action":  "typing",
	}
	slog.Debug("telegram transport: sending typing indicator", "conversation_id", conversationID, "chat_id", chatID)
	return t.sendAPI(ctx, "sendChatAction", payload)
}

func (t *Transport) pollAndDispatch(ctx context.Context, offset int64) (int64, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getUpdates", t.apiBaseURL, url.PathEscape(t.botToken))
	logEndpoint := fmt.Sprintf("%s/getUpdates", t.apiBaseURL)
	params := url.Values{}
	params.Set("timeout", strconv.Itoa(int(t.pollTimeout.Seconds())))
	if offset > 0 {
		params.Set("offset", strconv.FormatInt(offset, 10))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return offset, fmt.Errorf("build telegram getUpdates request: %w", err)
	}
	slog.Debug(
		"telegram transport: poll request",
		"api_url", logEndpoint,
		"offset", offset,
		"timeout", t.pollTimeout.String(),
		"poll_interval", t.pollInterval,
	)
	response, err := t.client.Do(request)
	if err != nil {
		return offset, fmt.Errorf("send telegram getUpdates request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return offset, fmt.Errorf("telegram getUpdates status: %s", response.Status)
	}
	var payload pollResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return offset, fmt.Errorf("decode telegram getUpdates response: %w", err)
	}
	if !payload.OK {
		return offset, fmt.Errorf("telegram getUpdates returned ok=false")
	}
	nextOffset := offset
	for _, event := range payload.Result {
		if event.UpdateID >= nextOffset {
			nextOffset = event.UpdateID + 1
		}
		if err := t.dispatchEvent(ctx, event); err != nil {
			return offset, err
		}
	}
	slog.Info(
		"telegram transport: poll completed",
		"result_count", len(payload.Result),
		"offset", offset,
		"next_offset", nextOffset,
	)
	return nextOffset, nil
}

func (t *Transport) dispatchEvent(ctx context.Context, event telegramEvent) error {
	if event.Message == nil || event.Message.From == nil || event.Message.Chat == nil {
		slog.Debug("telegram transport: dropping event: missing message metadata", "update_id", event.UpdateID)
		return nil
	}
	messageText := strings.TrimSpace(event.Message.Text)
	if messageText == "" {
		slog.Debug("telegram transport: dropping event: empty message text", "update_id", event.UpdateID, "chat_id", event.Message.Chat.ID, "user_id", event.Message.From.ID)
		return nil
	}
	userID := strconv.FormatInt(event.Message.From.ID, 10)
	chatID := strconv.FormatInt(event.Message.Chat.ID, 10)
	if t.allowedUserID != "" && userID != t.allowedUserID {
		slog.Info("telegram transport: ignoring message from unauthorized user", "update_id", event.UpdateID, "user_id", userID, "chat_id", chatID)
		return nil
	}
	if t.allowedChatID != "" && chatID != t.allowedChatID {
		slog.Info("telegram transport: ignoring message from unauthorized chat", "update_id", event.UpdateID, "chat_id", chatID)
		return nil
	}
	handler := t.getHandler()
	if handler == nil {
		slog.Warn("telegram transport: no inbound handler configured", "update_id", event.UpdateID, "chat_id", chatID, "user_id", userID)
		return nil
	}
	conversationName := strings.TrimSpace(event.Message.Chat.Title)
	if conversationName == "" {
		conversationName = strings.TrimSpace(event.Message.Chat.Username)
	}
	if conversationName == "" {
		conversationName = "telegram-chat-" + chatID
	}
	slog.Info(
		"telegram transport: dispatching inbound message",
		"update_id", event.UpdateID,
		"conversation_id", "telegram:"+chatID,
		"sender_id", "telegram-user:"+userID,
		"conversation_name", conversationName,
		"text_length", len(messageText),
	)
	return handler(ctx, transport.InboundMessage{
		ConversationID:   "telegram:" + chatID,
		ConversationName: conversationName,
		SenderID:         "telegram-user:" + userID,
		RecipientID:      "telegram-bot",
		EventID:          strconv.FormatInt(event.UpdateID, 10),
		Text:             messageText,
	})
}

func (t *Transport) sendAPI(ctx context.Context, method string, payload map[string]any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		return fmt.Errorf("telegram method is required")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode telegram %s payload: %w", method, err)
	}
	endpoint := fmt.Sprintf("%s/bot%s/%s", t.apiBaseURL, url.PathEscape(t.botToken), method)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build telegram %s request: %w", method, err)
	}
	request.Header.Set("Content-Type", "application/json")
	slog.Debug("telegram transport: sending API request", "method", method, "payload_keys", mapKeys(payload), "chat_id", payload["chat_id"])
	response, err := t.client.Do(request)
	if err != nil {
		return fmt.Errorf("send telegram %s request: %w", method, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("telegram %s status: %s", method, response.Status)
	}
	var parsed sendResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return fmt.Errorf("decode telegram %s response: %w", method, err)
	}
	if !parsed.OK {
		return fmt.Errorf("telegram %s returned ok=false", method)
	}
	slog.Debug("telegram transport: api request successful", "method", method, "payload_keys", mapKeys(payload))
	return nil
}

func (t *Transport) getHandler() transport.InboundHandler {
	t.handlerMu.RLock()
	defer t.handlerMu.RUnlock()
	return t.handler
}

func (t *Transport) clearRunCancel() {
	t.runMu.Lock()
	t.runCancel = nil
	t.runMu.Unlock()
}

func (t *Transport) loadOffset() (int64, error) {
	if strings.TrimSpace(t.offsetPath) == "" {
		slog.Debug("telegram transport: offset persistence disabled", "offset_path", "")
		return 0, nil
	}
	blob, err := os.ReadFile(t.offsetPath)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("telegram transport: no persisted offset found", "offset_path", t.offsetPath)
			return 0, nil
		}
		return 0, fmt.Errorf("read telegram offset: %w", err)
	}
	if strings.TrimSpace(string(blob)) == "" {
		slog.Info("telegram transport: persisted offset file is empty", "offset_path", t.offsetPath)
		return 0, nil
	}
	var payload struct {
		Offset int64 `json:"offset"`
	}
	if err := json.Unmarshal(blob, &payload); err != nil {
		return 0, fmt.Errorf("decode telegram offset: %w", err)
	}
	slog.Info("telegram transport: loaded persisted offset", "offset", payload.Offset, "offset_path", t.offsetPath)
	if payload.Offset < 0 {
		return 0, nil
	}
	return payload.Offset, nil
}

func (t *Transport) persistOffset(offset int64) error {
	if strings.TrimSpace(t.offsetPath) == "" {
		return nil
	}
	slog.Debug("telegram transport: persisting offset", "offset", offset, "offset_path", t.offsetPath)
	blob, err := json.Marshal(map[string]any{
		"offset": offset,
	})
	if err != nil {
		return fmt.Errorf("encode telegram offset: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(t.offsetPath), 0o755); err != nil {
		return fmt.Errorf("create telegram offset dir: %w", err)
	}
	tmpPath := t.offsetPath + ".tmp"
	if err := os.WriteFile(tmpPath, blob, 0o644); err != nil {
		return fmt.Errorf("write telegram offset temp file: %w", err)
	}
	if err := os.Rename(tmpPath, t.offsetPath); err != nil {
		return fmt.Errorf("replace telegram offset file: %w", err)
	}
	slog.Debug("telegram transport: offset persisted", "offset", offset, "offset_path", t.offsetPath)
	return nil
}

func tokenSuffix(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) <= 8 {
		return strings.Repeat("*", len(token))
	}
	return strings.Repeat("*", len(token)-8) + token[len(token)-4:]
}

func mapKeys(payload map[string]any) []string {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func parseConversationChatID(conversationID string) (string, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", fmt.Errorf("conversation id is required")
	}
	if !strings.HasPrefix(conversationID, "telegram:") {
		return "", fmt.Errorf("unsupported telegram conversation id %q", conversationID)
	}
	chatID := strings.TrimSpace(strings.TrimPrefix(conversationID, "telegram:"))
	if chatID == "" {
		return "", fmt.Errorf("telegram chat id is required")
	}
	return chatID, nil
}

func formatAttachmentNotice(attachments []transport.OutboundAttachment) string {
	names := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		name := strings.TrimSpace(attachment.Name)
		if name == "" {
			name = filepath.Base(strings.TrimSpace(attachment.Path))
		}
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	return "Generated files: " + strings.Join(names, ", ")
}
