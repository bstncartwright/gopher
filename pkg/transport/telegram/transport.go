package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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

	draftStreamingMu       sync.Mutex
	draftStreamingDisabled bool

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
	MessageID       int64         `json:"message_id"`
	MessageThreadID int64         `json:"message_thread_id"`
	From            *telegramUser `json:"from"`
	Chat            *telegramChat `json:"chat"`
	Date            int64         `json:"date"`
	Text            string        `json:"text"`
	Caption         string        `json:"caption"`
	Photo           []telegramPhotoSize
	Document        *telegramFile `json:"document"`
	Video           *telegramFile `json:"video"`
	Audio           *telegramFile `json:"audio"`
	Voice           *telegramFile `json:"voice"`
	Animation       *telegramFile `json:"animation"`
}

type telegramPhotoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type telegramFile struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size"`
	FileName     string `json:"file_name"`
	MimeType     string `json:"mime_type"`
}

type telegramGetFileResponse struct {
	OK          bool             `json:"ok"`
	Result      telegramFilePath `json:"result"`
	Description string           `json:"description"`
	ErrorCode   int              `json:"error_code"`
}

type telegramFilePath struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
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
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	ErrorCode   int    `json:"error_code"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type telegramAPIError struct {
	Method      string
	Description string
	ErrorCode   int
	HTTPStatus  string
	RawBody     string
}

func (e *telegramAPIError) Error() string {
	if e == nil {
		return "telegram api error"
	}
	method := strings.TrimSpace(e.Method)
	if method == "" {
		method = "request"
	}
	detail := strings.TrimSpace(e.Description)
	status := strings.TrimSpace(e.HTTPStatus)
	if detail == "" {
		detail = strings.TrimSpace(e.RawBody)
	}
	switch {
	case detail == "" && status != "" && e.ErrorCode > 0:
		return fmt.Sprintf("telegram %s status=%s returned ok=false (error_code=%d)", method, status, e.ErrorCode)
	case detail == "" && status != "":
		return fmt.Sprintf("telegram %s status=%s returned ok=false", method, status)
	case detail == "" && e.ErrorCode > 0:
		return fmt.Sprintf("telegram %s returned ok=false (error_code=%d)", method, e.ErrorCode)
	case detail == "":
		return fmt.Sprintf("telegram %s returned ok=false", method)
	case status != "" && e.ErrorCode > 0:
		return fmt.Sprintf("telegram %s status=%s returned ok=false (error_code=%d): %s", method, status, e.ErrorCode, detail)
	case status != "":
		return fmt.Sprintf("telegram %s status=%s returned ok=false: %s", method, status, detail)
	case e.ErrorCode > 0:
		return fmt.Sprintf("telegram %s returned ok=false (error_code=%d): %s", method, e.ErrorCode, detail)
	default:
		return fmt.Sprintf("telegram %s returned ok=false: %s", method, detail)
	}
}

var (
	telegramFencedCodeBlockPattern = regexp.MustCompile("(?s)```(?:[^\\n`]*)\\n(.*?)```")
	telegramInlineCodePattern      = regexp.MustCompile("`([^`\\n]+)`")
	telegramMarkdownLinkPattern    = regexp.MustCompile(`\[(.+?)\]\((https?://[^\s)]+|tg://[^\s)]+)\)`)
	telegramBoldPattern            = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	telegramMarkdownHeaderPattern  = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.+?)\s*$`)
	telegramHeaderSuffixPattern    = regexp.MustCompile(`\s+#+\s*$`)
	// Telegram Bot API only accepts this fixed set of built-in emoji reactions.
	telegramSupportedReactionEmojis = map[string]struct{}{
		"❤": {}, "👍": {}, "👎": {}, "🔥": {}, "🥰": {}, "👏": {}, "😁": {}, "🤔": {}, "🤯": {}, "😱": {},
		"🤬": {}, "😢": {}, "🎉": {}, "🤩": {}, "🤮": {}, "💩": {}, "🙏": {}, "👌": {}, "🕊": {}, "🤡": {},
		"🥱": {}, "🥴": {}, "😍": {}, "🐳": {}, "❤‍🔥": {}, "🌚": {}, "🌭": {}, "💯": {}, "🤣": {}, "⚡": {},
		"🍌": {}, "🏆": {}, "💔": {}, "🤨": {}, "😐": {}, "🍓": {}, "🍾": {}, "💋": {}, "🖕": {}, "😈": {},
		"😴": {}, "😭": {}, "🤓": {}, "👻": {}, "👨‍💻": {}, "👀": {}, "🎃": {}, "🙈": {}, "😇": {}, "😨": {},
		"🤝": {}, "✍": {}, "🫡": {}, "🎅": {}, "🎄": {}, "☃": {}, "💅": {}, "🤪": {}, "🗿": {},
		"🆒": {}, "💘": {}, "🙉": {}, "🦄": {}, "😘": {}, "💊": {}, "🙊": {}, "😎": {}, "👾": {}, "🤷‍♂": {},
		"🤷": {}, "🤷‍♀": {}, "😡": {},
	}
)

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
	target, err := parseConversationTarget(message.ConversationID)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(message.Text)
	if text == "" && len(message.Attachments) == 0 {
		slog.Debug("telegram transport: send message skipped due empty payload", "conversation_id", message.ConversationID)
		return nil
	}

	if text != "" {
		renderedText, parseMode := renderTelegramMessageText(text)
		payload := buildSendMessagePayload(target.ChatID, target.MessageThreadID, renderedText, parseMode)
		slog.Info("telegram transport: sending message", "conversation_id", message.ConversationID, "chat_id", target.ChatID, "attachment_count", len(message.Attachments))
		if err := t.sendAPI(ctx, "sendMessage", payload); err != nil {
			if parseMode == "" || !isTelegramEntityParseError(err) {
				return err
			}
			fallbackText := strings.TrimSpace(stripCommonMarkdownFormatting(text))
			if fallbackText == "" {
				fallbackText = text
			}
			slog.Warn(
				"telegram transport: parse-mode render rejected by telegram, retrying plain text",
				"conversation_id", message.ConversationID,
				"chat_id", target.ChatID,
				"error", err,
			)
			if err := t.sendAPI(ctx, "sendMessage", buildSendMessagePayload(target.ChatID, target.MessageThreadID, fallbackText, "")); err != nil {
				return err
			}
		}
	}

	for _, attachment := range message.Attachments {
		if err := t.sendAttachment(ctx, message.ConversationID, target, attachment); err != nil {
			return err
		}
	}
	return nil
}

func (t *Transport) SendTyping(ctx context.Context, conversationID string, typing bool) error {
	if !typing {
		return nil
	}
	target, err := parseConversationTarget(conversationID)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"chat_id": target.ChatID,
		"action":  "typing",
	}
	if target.MessageThreadID > 0 {
		payload["message_thread_id"] = target.MessageThreadID
	}
	slog.Debug("telegram transport: sending typing indicator", "conversation_id", conversationID, "chat_id", target.ChatID)
	return t.sendAPI(ctx, "sendChatAction", payload)
}

func (t *Transport) SendMessageDraft(ctx context.Context, conversationID string, draftID int64, text string) error {
	if draftID <= 0 {
		return fmt.Errorf("draft id must be > 0")
	}
	if t.isDraftStreamingDisabled() {
		return nil
	}
	target, err := parseConversationTarget(conversationID)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) > 4096 {
		text = strings.TrimSpace(string(runes[:4095])) + "…"
	}
	payload := map[string]any{
		"chat_id":  target.ChatID,
		"draft_id": draftID,
		"text":     text,
	}
	if target.MessageThreadID > 0 {
		payload["message_thread_id"] = target.MessageThreadID
	}
	if err := t.sendAPI(ctx, "sendMessageDraft", payload); err != nil {
		if isTelegramUnknownMethodError(err) {
			t.setDraftStreamingDisabled(true)
			slog.Warn("telegram transport: sendMessageDraft unsupported; disabling draft streaming", "error", err)
			return nil
		}
		return err
	}
	return nil
}

func (t *Transport) SendReaction(ctx context.Context, reaction transport.OutboundReaction) error {
	chatID, err := parseConversationChatID(reaction.ConversationID)
	if err != nil {
		return err
	}
	messageID, err := parseTelegramMessageID(reaction.TargetEventID)
	if err != nil {
		return err
	}
	emoji := normalizeTelegramReactionEmoji(strings.TrimSpace(reaction.Emoji))
	if emoji == "" {
		return fmt.Errorf("emoji is required")
	}
	if _, ok := telegramSupportedReactionEmojis[emoji]; !ok {
		return fmt.Errorf("unsupported telegram reaction emoji %q", strings.TrimSpace(reaction.Emoji))
	}
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"reaction": []map[string]any{
			{
				"type":  "emoji",
				"emoji": emoji,
			},
		},
	}
	slog.Debug(
		"telegram transport: sending reaction",
		"conversation_id", reaction.ConversationID,
		"chat_id", chatID,
		"message_id", messageID,
		"target_event_id", reaction.TargetEventID,
		"emoji", emoji,
	)
	if err := t.sendAPI(ctx, "setMessageReaction", payload); err != nil {
		slog.Warn(
			"telegram transport: reaction send failed",
			"conversation_id", reaction.ConversationID,
			"chat_id", chatID,
			"message_id", messageID,
			"target_event_id", reaction.TargetEventID,
			"emoji", emoji,
			"error", err,
		)
		return err
	}
	return nil
}

func normalizeTelegramReactionEmoji(emoji string) string {
	return strings.ReplaceAll(strings.TrimSpace(emoji), "\uFE0F", "")
}

func (t *Transport) SetCommands(ctx context.Context, commands []BotCommand) error {
	if len(commands) == 0 {
		return nil
	}
	normalized := make([]map[string]string, 0, len(commands))
	for _, command := range commands {
		name := strings.TrimSpace(command.Command)
		name = strings.TrimPrefix(name, "/")
		name = strings.ToLower(strings.TrimSpace(name))
		description := strings.TrimSpace(command.Description)
		if name == "" || description == "" {
			continue
		}
		normalized = append(normalized, map[string]string{
			"command":     name,
			"description": description,
		})
	}
	if len(normalized) == 0 {
		return nil
	}
	return t.sendAPI(ctx, "setMyCommands", map[string]any{
		"commands": normalized,
	})
}

func (t *Transport) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return fmt.Errorf("telegram webhook url is required")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return fmt.Errorf("telegram webhook secret is required")
	}
	payload := map[string]any{
		"url":          webhookURL,
		"secret_token": secret,
	}
	return t.sendAPI(ctx, "setWebhook", payload)
}

func (t *Transport) DeleteWebhook(ctx context.Context, dropPending bool) error {
	payload := map[string]any{
		"drop_pending_updates": dropPending,
	}
	return t.sendAPI(ctx, "deleteWebhook", payload)
}

func (t *Transport) HandleWebhookUpdate(ctx context.Context, payload []byte) error {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return fmt.Errorf("telegram webhook payload is required")
	}
	var event telegramEvent
	if err := json.Unmarshal(trimmed, &event); err != nil {
		return fmt.Errorf("decode telegram webhook update: %w", err)
	}
	return t.dispatchEvent(ctx, event)
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
	userID := strconv.FormatInt(event.Message.From.ID, 10)
	chatID := strconv.FormatInt(event.Message.Chat.ID, 10)
	if t.allowedUserID != "" && userID != t.allowedUserID {
		slog.Info("telegram transport: ignoring message from unauthorized user", "update_id", event.UpdateID, "user_id", userID, "chat_id", chatID)
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
	messageText := strings.TrimSpace(event.Message.Text)
	if messageText == "" {
		messageText = strings.TrimSpace(event.Message.Caption)
	}
	attachments, err := t.resolveInboundAttachments(ctx, event.Message)
	if err != nil {
		return err
	}
	if messageText == "" && len(attachments) == 0 {
		slog.Debug("telegram transport: dropping event: empty message payload", "update_id", event.UpdateID, "chat_id", event.Message.Chat.ID, "user_id", event.Message.From.ID)
		return nil
	}
	conversationID := formatConversationID(chatID, event.Message.MessageThreadID)
	slog.Info(
		"telegram transport: dispatching inbound message",
		"update_id", event.UpdateID,
		"conversation_id", conversationID,
		"sender_id", "telegram-user:"+userID,
		"conversation_name", conversationName,
		"text_length", len(messageText),
		"attachment_count", len(attachments),
	)
	return handler(ctx, transport.InboundMessage{
		ConversationID:   conversationID,
		ConversationName: conversationName,
		SenderID:         "telegram-user:" + userID,
		RecipientID:      "telegram-bot",
		EventID:          strconv.FormatInt(event.Message.MessageID, 10),
		Text:             messageText,
		Attachments:      attachments,
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
	body, err = io.ReadAll(io.LimitReader(response.Body, 32<<10))
	if err != nil {
		return fmt.Errorf("read telegram %s response: %w", method, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if apiErr := parseTelegramAPIError(method, response.Status, body); apiErr != nil {
			return apiErr
		}
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			return fmt.Errorf("telegram %s status: %s", method, response.Status)
		}
		return fmt.Errorf("telegram %s status: %s body: %s", method, response.Status, detail)
	}
	var parsed sendResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decode telegram %s response: %w", method, err)
	}
	if !parsed.OK {
		return &telegramAPIError{
			Method:      method,
			Description: strings.TrimSpace(parsed.Description),
			ErrorCode:   parsed.ErrorCode,
			HTTPStatus:  response.Status,
			RawBody:     strings.TrimSpace(string(body)),
		}
	}
	slog.Debug("telegram transport: api request successful", "method", method, "payload_keys", mapKeys(payload))
	return nil
}

func parseTelegramAPIError(method, httpStatus string, body []byte) error {
	trimmedBody := strings.TrimSpace(string(body))
	if trimmedBody == "" {
		return nil
	}
	var parsed sendResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	if parsed.OK && strings.TrimSpace(parsed.Description) == "" && parsed.ErrorCode == 0 {
		return nil
	}
	return &telegramAPIError{
		Method:      method,
		Description: strings.TrimSpace(parsed.Description),
		ErrorCode:   parsed.ErrorCode,
		HTTPStatus:  strings.TrimSpace(httpStatus),
		RawBody:     trimmedBody,
	}
}

func (t *Transport) sendMultipartAPI(ctx context.Context, method string, body *bytes.Buffer, contentType string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		return fmt.Errorf("telegram method is required")
	}
	endpoint := fmt.Sprintf("%s/bot%s/%s", t.apiBaseURL, url.PathEscape(t.botToken), method)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return fmt.Errorf("build telegram %s request: %w", method, err)
	}
	request.Header.Set("Content-Type", contentType)
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
		return &telegramAPIError{
			Method:      method,
			Description: strings.TrimSpace(parsed.Description),
			ErrorCode:   parsed.ErrorCode,
		}
	}
	return nil
}

func (t *Transport) resolveInboundAttachments(ctx context.Context, message *telegramMessage) ([]transport.InboundAttachment, error) {
	if message == nil {
		return nil, nil
	}
	attachments := make([]transport.InboundAttachment, 0, 4)
	if photo := largestTelegramPhoto(message.Photo); photo != nil {
		attachment, err := t.fetchInboundAttachment(ctx, photo.FileID, "", "image/jpeg")
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	fileRefs := []struct {
		file        *telegramFile
		defaultMIME string
	}{
		{file: message.Document},
		{file: message.Video, defaultMIME: "video/mp4"},
		{file: message.Audio, defaultMIME: "audio/mpeg"},
		{file: message.Voice, defaultMIME: "audio/ogg"},
		{file: message.Animation, defaultMIME: "image/gif"},
	}
	for _, ref := range fileRefs {
		if ref.file == nil || strings.TrimSpace(ref.file.FileID) == "" {
			continue
		}
		attachment, err := t.fetchInboundAttachment(ctx, ref.file.FileID, ref.file.FileName, firstNonEmpty(ref.file.MimeType, ref.defaultMIME))
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func largestTelegramPhoto(photos []telegramPhotoSize) *telegramPhotoSize {
	if len(photos) == 0 {
		return nil
	}
	largest := &photos[0]
	for i := 1; i < len(photos); i++ {
		candidate := &photos[i]
		if candidate.FileSize > largest.FileSize {
			largest = candidate
			continue
		}
		if candidate.FileSize == largest.FileSize && candidate.Width*candidate.Height > largest.Width*largest.Height {
			largest = candidate
		}
	}
	return largest
}

func (t *Transport) fetchInboundAttachment(ctx context.Context, fileID, name, mimeType string) (transport.InboundAttachment, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return transport.InboundAttachment{}, fmt.Errorf("telegram attachment file id is required")
	}
	fileInfo, err := t.getTelegramFile(ctx, fileID)
	if err != nil {
		return transport.InboundAttachment{}, err
	}
	data, err := t.downloadTelegramFile(ctx, fileInfo.FilePath)
	if err != nil {
		return transport.InboundAttachment{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(strings.TrimSpace(fileInfo.FilePath))
	}
	mimeType = normalizeInboundMIMEType(mimeType, name)
	return transport.InboundAttachment{
		Name:     strings.TrimSpace(name),
		MIMEType: mimeType,
		Text:     inlineTextFromAttachment(mimeType, data),
		Data:     data,
	}, nil
}

func (t *Transport) getTelegramFile(ctx context.Context, fileID string) (telegramFilePath, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	payload := map[string]any{"file_id": strings.TrimSpace(fileID)}
	body, err := json.Marshal(payload)
	if err != nil {
		return telegramFilePath{}, fmt.Errorf("encode telegram getFile payload: %w", err)
	}
	endpoint := fmt.Sprintf("%s/bot%s/getFile", t.apiBaseURL, url.PathEscape(t.botToken))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return telegramFilePath{}, fmt.Errorf("build telegram getFile request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := t.client.Do(request)
	if err != nil {
		return telegramFilePath{}, fmt.Errorf("send telegram getFile request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return telegramFilePath{}, fmt.Errorf("telegram getFile status: %s", response.Status)
	}
	var parsed telegramGetFileResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return telegramFilePath{}, fmt.Errorf("decode telegram getFile response: %w", err)
	}
	if !parsed.OK {
		return telegramFilePath{}, &telegramAPIError{
			Method:      "getFile",
			Description: strings.TrimSpace(parsed.Description),
			ErrorCode:   parsed.ErrorCode,
		}
	}
	if strings.TrimSpace(parsed.Result.FilePath) == "" {
		return telegramFilePath{}, fmt.Errorf("telegram getFile returned empty file path")
	}
	return parsed.Result, nil
}

func (t *Transport) downloadTelegramFile(ctx context.Context, filePath string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	filePath = strings.TrimLeft(strings.TrimSpace(filePath), "/")
	if filePath == "" {
		return nil, fmt.Errorf("telegram file path is required")
	}
	endpoint := fmt.Sprintf("%s/file/bot%s/%s", t.apiBaseURL, url.PathEscape(t.botToken), filePath)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build telegram file download request: %w", err)
	}
	response, err := t.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download telegram file: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("telegram file download status: %s", response.Status)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read telegram file download: %w", err)
	}
	return data, nil
}

func normalizeInboundMIMEType(mimeType, name string) string {
	mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(strings.TrimSpace(mimeType), ";", 2)[0]))
	if mimeType != "" {
		return mimeType
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == "" {
		return ""
	}
	guessed := strings.TrimSpace(mime.TypeByExtension(ext))
	if guessed == "" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(strings.SplitN(guessed, ";", 2)[0]))
}

func inlineTextFromAttachment(mimeType string, data []byte) string {
	if len(data) == 0 || len(data) > 256*1024 {
		return ""
	}
	switch normalizeInboundMIMEType(mimeType, "") {
	case "text/plain", "text/markdown", "text/csv", "application/json", "application/xml", "application/yaml", "application/x-yaml":
		if !utf8.Valid(data) {
			return ""
		}
		text := strings.TrimSpace(string(data))
		if len(text) > 24000 {
			text = strings.TrimSpace(text[:24000]) + "\n\n[truncated]"
		}
		return text
	}
	if strings.HasPrefix(normalizeInboundMIMEType(mimeType, ""), "text/") && utf8.Valid(data) {
		text := strings.TrimSpace(string(data))
		if len(text) > 24000 {
			text = strings.TrimSpace(text[:24000]) + "\n\n[truncated]"
		}
		return text
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (t *Transport) sendAttachment(ctx context.Context, conversationID string, target telegramConversationTarget, attachment transport.OutboundAttachment) error {
	pathValue := strings.TrimSpace(attachment.Path)
	if pathValue == "" {
		return fmt.Errorf("attachment path is required")
	}
	file, err := os.Open(pathValue)
	if err != nil {
		return fmt.Errorf("open attachment %s: %w", pathValue, err)
	}
	defer file.Close()

	method := selectTelegramAttachmentMethod(attachment)
	field := telegramAttachmentField(method)
	if field == "" {
		field = "document"
		method = "sendDocument"
	}
	filename := strings.TrimSpace(attachment.Name)
	if filename == "" {
		filename = filepath.Base(pathValue)
	}
	if filename == "" {
		filename = "attachment"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strings.TrimSpace(target.ChatID)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("build telegram %s payload: %w", method, err)
	}
	if target.MessageThreadID > 0 {
		if err := writer.WriteField("message_thread_id", strconv.FormatInt(target.MessageThreadID, 10)); err != nil {
			_ = writer.Close()
			return fmt.Errorf("build telegram %s payload: %w", method, err)
		}
	}
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		_ = writer.Close()
		return fmt.Errorf("build telegram %s attachment payload: %w", method, err)
	}
	if _, err := io.Copy(part, file); err != nil {
		_ = writer.Close()
		return fmt.Errorf("copy telegram %s attachment payload: %w", method, err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finalize telegram %s payload: %w", method, err)
	}

	slog.Info(
		"telegram transport: sending attachment",
		"conversation_id", conversationID,
		"chat_id", target.ChatID,
		"path", pathValue,
		"filename", filename,
		"method", method,
		"mime_type", strings.TrimSpace(attachment.MIMEType),
	)
	return t.sendMultipartAPI(ctx, method, &body, writer.FormDataContentType())
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

type telegramConversationTarget struct {
	ChatID          string
	MessageThreadID int64
}

func parseConversationChatID(conversationID string) (string, error) {
	target, err := parseConversationTarget(conversationID)
	if err != nil {
		return "", err
	}
	return target.ChatID, nil
}

func parseConversationTarget(conversationID string) (telegramConversationTarget, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return telegramConversationTarget{}, fmt.Errorf("conversation id is required")
	}
	if !strings.HasPrefix(conversationID, "telegram:") {
		return telegramConversationTarget{}, fmt.Errorf("unsupported telegram conversation id %q", conversationID)
	}
	raw := strings.TrimSpace(strings.TrimPrefix(conversationID, "telegram:"))
	parts := strings.SplitN(raw, ":", 2)
	chatID := strings.TrimSpace(parts[0])
	if chatID == "" {
		return telegramConversationTarget{}, fmt.Errorf("telegram chat id is required")
	}
	target := telegramConversationTarget{ChatID: chatID}
	if len(parts) == 2 {
		threadRaw := strings.TrimSpace(parts[1])
		if threadRaw == "" {
			return telegramConversationTarget{}, fmt.Errorf("telegram message thread id is required")
		}
		threadID, err := strconv.ParseInt(threadRaw, 10, 64)
		if err != nil || threadID <= 0 {
			return telegramConversationTarget{}, fmt.Errorf("invalid telegram message thread id %q", threadRaw)
		}
		target.MessageThreadID = threadID
	}
	return target, nil
}

func formatConversationID(chatID string, messageThreadID int64) string {
	chatID = strings.TrimSpace(chatID)
	if chatID == "" {
		return "telegram:"
	}
	if messageThreadID > 0 {
		return fmt.Sprintf("telegram:%s:%d", chatID, messageThreadID)
	}
	return "telegram:" + chatID
}

func parseTelegramMessageID(eventID string) (int64, error) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return 0, fmt.Errorf("target event id is required")
	}
	value, err := strconv.ParseInt(eventID, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid target event id %q", eventID)
	}
	return value, nil
}

func selectTelegramAttachmentMethod(attachment transport.OutboundAttachment) string {
	mimeType := strings.ToLower(strings.TrimSpace(attachment.MIMEType))
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(attachment.Path)))

	switch {
	case strings.HasPrefix(mimeType, "image/"):
		if mimeType == "image/gif" {
			return "sendAnimation"
		}
		return "sendPhoto"
	case strings.HasPrefix(mimeType, "video/"):
		return "sendVideo"
	case strings.HasPrefix(mimeType, "audio/"):
		if mimeType == "audio/ogg" || mimeType == "audio/opus" {
			return "sendVoice"
		}
		return "sendAudio"
	case mimeType == "application/ogg":
		return "sendVoice"
	}

	switch ext {
	case ".gif":
		return "sendAnimation"
	case ".jpg", ".jpeg", ".png", ".webp", ".bmp":
		return "sendPhoto"
	case ".mp4", ".mov", ".mkv", ".webm":
		return "sendVideo"
	case ".ogg", ".opus":
		return "sendVoice"
	case ".mp3", ".m4a", ".wav", ".flac", ".aac":
		return "sendAudio"
	default:
		return "sendDocument"
	}
}

func telegramAttachmentField(method string) string {
	switch strings.TrimSpace(method) {
	case "sendPhoto":
		return "photo"
	case "sendVideo":
		return "video"
	case "sendAudio":
		return "audio"
	case "sendVoice":
		return "voice"
	case "sendAnimation":
		return "animation"
	case "sendDocument":
		return "document"
	default:
		return ""
	}
}

func buildSendMessagePayload(chatID string, messageThreadID int64, text, parseMode string) map[string]any {
	payload := map[string]any{
		"chat_id": strings.TrimSpace(chatID),
		"text":    strings.TrimSpace(text),
	}
	if messageThreadID > 0 {
		payload["message_thread_id"] = messageThreadID
	}
	if mode := strings.TrimSpace(parseMode); mode != "" {
		payload["parse_mode"] = mode
	}
	return payload
}

func (t *Transport) setDraftStreamingDisabled(disabled bool) {
	t.draftStreamingMu.Lock()
	t.draftStreamingDisabled = disabled
	t.draftStreamingMu.Unlock()
}

func (t *Transport) isDraftStreamingDisabled() bool {
	t.draftStreamingMu.Lock()
	defer t.draftStreamingMu.Unlock()
	return t.draftStreamingDisabled
}

func isTelegramEntityParseError(err error) bool {
	var apiErr *telegramAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	detail := strings.ToLower(strings.TrimSpace(apiErr.Description))
	if detail == "" {
		return false
	}
	return strings.Contains(detail, "can't parse entities") || strings.Contains(detail, "parse entities")
}

func isTelegramUnknownMethodError(err error) bool {
	var apiErr *telegramAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	detail := strings.ToLower(strings.TrimSpace(apiErr.Description))
	if detail == "" {
		return false
	}
	return strings.Contains(detail, "method not found") || strings.Contains(detail, "unknown method")
}

func renderTelegramMessageText(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	return markdownishToTelegramHTML(text), "HTML"
}

func markdownishToTelegramHTML(input string) string {
	text := normalizeTelegramMarkdown(input)
	if text == "" {
		return ""
	}

	type htmlToken struct {
		key   string
		value string
	}
	tokens := make([]htmlToken, 0, 16)
	stash := func(pattern *regexp.Regexp, builder func([]string) string) {
		text = pattern.ReplaceAllStringFunc(text, func(match string) string {
			submatch := pattern.FindStringSubmatch(match)
			if len(submatch) == 0 {
				return match
			}
			key := fmt.Sprintf("@@TGPH_%d@@", len(tokens))
			tokens = append(tokens, htmlToken{
				key:   key,
				value: builder(submatch),
			})
			return key
		})
	}

	stash(telegramFencedCodeBlockPattern, func(parts []string) string {
		code := strings.TrimSuffix(parts[1], "\n")
		return "<pre><code>" + html.EscapeString(code) + "</code></pre>"
	})
	stash(telegramInlineCodePattern, func(parts []string) string {
		return "<code>" + html.EscapeString(parts[1]) + "</code>"
	})
	stash(telegramMarkdownLinkPattern, func(parts []string) string {
		label := strings.TrimSpace(parts[1])
		href := strings.TrimSpace(parts[2])
		if !isSupportedTelegramLink(href) {
			return html.EscapeString(parts[0])
		}
		if label == "" {
			label = href
		}
		return `<a href="` + html.EscapeString(href) + `">` + html.EscapeString(label) + "</a>"
	})
	stash(telegramBoldPattern, func(parts []string) string {
		content := parts[1]
		if strings.TrimSpace(content) == "" && len(parts) > 2 {
			content = parts[2]
		}
		if content == "" {
			return html.EscapeString(parts[0])
		}
		return "<b>" + html.EscapeString(content) + "</b>"
	})

	rendered := html.EscapeString(text)
	for i := len(tokens) - 1; i >= 0; i-- {
		rendered = strings.ReplaceAll(rendered, tokens[i].key, tokens[i].value)
	}
	return rendered
}

func normalizeTelegramMarkdown(input string) string {
	text := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(input), "\r\n", "\n"), "\r", "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	inFencedCode := false
	for i := range lines {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "```") {
			inFencedCode = !inFencedCode
			continue
		}
		if inFencedCode {
			continue
		}
		matches := telegramMarkdownHeaderPattern.FindStringSubmatch(lines[i])
		if len(matches) == 0 {
			continue
		}
		header := strings.TrimSpace(telegramHeaderSuffixPattern.ReplaceAllString(matches[1], ""))
		if header == "" {
			continue
		}
		lines[i] = "**" + header + "**"
	}
	return strings.Join(lines, "\n")
}

func isSupportedTelegramLink(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return strings.TrimSpace(parsed.Host) != ""
	case "tg":
		return true
	default:
		return false
	}
}

func stripCommonMarkdownFormatting(text string) string {
	text = normalizeTelegramMarkdown(text)
	if text == "" {
		return ""
	}
	text = telegramMarkdownLinkPattern.ReplaceAllString(text, "$1 ($2)")
	text = strings.ReplaceAll(text, "**", "")
	text = strings.ReplaceAll(text, "__", "")
	text = strings.ReplaceAll(text, "`", "")
	return text
}
