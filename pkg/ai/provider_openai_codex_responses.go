//lint:file-ignore SA1019 nhooyr websocket is required for codex compatibility.
package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

type OpenAICodexResponsesOptions struct {
	StreamOptions
	ReasoningEffort  ThinkingLevel `json:"reasoningEffort,omitempty"`
	ReasoningSummary string        `json:"reasoningSummary,omitempty"`
	TextVerbosity    string        `json:"textVerbosity,omitempty"`
}

const (
	defaultCodexBaseURL = "https://chatgpt.com/backend-api"
	codexJWTClaimPath   = "https://api.openai.com/auth"
	codexMaxRetries     = 3
	codexBaseDelay      = time.Second
	websocketSessionTTL = 5 * time.Minute
)

var codexToolCallProviders = map[Provider]struct{}{
	ProviderOpenAI:       {},
	ProviderOpenAICodex:  {},
	Provider("opencode"): {},
}

type codexRequestBody struct {
	Model             string           `json:"model"`
	Store             bool             `json:"store"`
	Stream            bool             `json:"stream,omitempty"`
	Instructions      string           `json:"instructions,omitempty"`
	Input             []any            `json:"input,omitempty"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolChoice        string           `json:"tool_choice,omitempty"`
	ParallelToolCalls bool             `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64         `json:"temperature,omitempty"`
	Reasoning         map[string]any   `json:"reasoning,omitempty"`
	Text              map[string]any   `json:"text,omitempty"`
	Include           []string         `json:"include,omitempty"`
	PromptCacheKey    string           `json:"prompt_cache_key,omitempty"`
}

func StreamOpenAICodexResponses(model Model, conversation Context, options *StreamOptions) *AssistantMessageEventStream {
	opts := &OpenAICodexResponsesOptions{}
	if options != nil {
		opts.StreamOptions = *options
	}
	return streamOpenAICodexResponses(model, conversation, opts)
}

func StreamSimpleOpenAICodexResponses(model Model, conversation Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == "" {
		apiKey = GetEnvAPIKey(string(model.Provider))
	}
	if apiKey == "" {
		// OAuth-first provider, but still allow explicit key injection only.
		apiKey = ""
	}
	base := BuildBaseOptions(model, options, apiKey)
	reasoningEffort := ThinkingLevel("")
	if options != nil {
		reasoningEffort = options.Reasoning
	}
	if !SupportsXHigh(model) {
		reasoningEffort = ClampReasoning(reasoningEffort)
	}
	return streamOpenAICodexResponses(model, conversation, &OpenAICodexResponsesOptions{
		StreamOptions:   *base,
		ReasoningEffort: reasoningEffort,
	})
}

func streamOpenAICodexResponses(model Model, conversation Context, options *OpenAICodexResponsesOptions) *AssistantMessageEventStream {
	stream := CreateAssistantMessageEventStream()
	if options == nil {
		options = &OpenAICodexResponsesOptions{}
	}
	ctx := resolveRequestContext(&options.StreamOptions)

	go func() {
		output := NewAssistantMessage(model)
		output.API = APIOpenAICodexResponse
		defer stream.End(&output)

		apiKey := options.APIKey
		if apiKey == "" {
			apiKey = GetEnvAPIKey(string(model.Provider))
		}
		if apiKey == "" {
			output.StopReason = StopReasonError
			output.ErrorMessage = fmt.Sprintf("no API key for provider %s", model.Provider)
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		accountID, err := extractCodexAccountID(apiKey)
		if err != nil {
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		body := buildCodexRequestBody(model, conversation, options)
		if options.OnPayload != nil {
			options.OnPayload(body)
		}
		jsonBody, err := json.Marshal(body)
		if err != nil {
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		headers := buildCodexHeaders(model.Headers, options.Headers, accountID, apiKey, options.SessionID)
		transport := options.Transport
		if transport == "" {
			if strings.TrimSpace(options.SessionID) != "" && supportsCodexWebSocketByDefault(model.BaseURL) {
				transport = TransportAuto
			} else {
				transport = TransportSSE
			}
		}

		if transport != TransportSSE {
			wsStarted := false
			if err := processCodexWebSocket(ctx, resolveCodexWebSocketURL(model.BaseURL), body, headers, &output, stream, model, options, &wsStarted); err == nil {
				if output.StopReason == StopReasonStop || output.StopReason == StopReasonLength || output.StopReason == StopReasonToolUse {
					stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: &output})
					return
				}
				stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
				return
			} else if transport == TransportWebSocket || (wsStarted && !isNormalWebSocketClosureError(err)) {
				output.StopReason = stopReasonForError(ctx, err)
				output.ErrorMessage = err.Error()
				stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
				return
			}
		}

		response, err := doCodexSSERequestWithRetry(ctx, resolveCodexURL(model.BaseURL), headers, jsonBody, options)
		if err != nil {
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		defer response.Body.Close()

		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: &output})
		events := make(chan sseEvent, 32)
		errCh := make(chan error, 1)
		go func() {
			errCh <- readSSE(ctx, response.Body, events)
		}()
		state := &responsesStreamState{}
		for ev := range events {
			if ev.Data == "[DONE]" || ev.Data == "" {
				continue
			}
			payload := mapCodexResponsesEvent(decodeJSON(ev.Data))
			if err := processResponsesStreamEvent(payload, &output, stream, model, state, nil); err != nil {
				output.StopReason = StopReasonError
				output.ErrorMessage = err.Error()
				stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
				return
			}
		}
		if err := <-errCh; err != nil && ctx.Err() == nil {
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		if ctx.Err() != nil {
			output.StopReason = StopReasonAborted
			output.ErrorMessage = "request was aborted"
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonAborted, Error: &output})
			return
		}
		if output.StopReason == StopReasonError || output.StopReason == StopReasonAborted {
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: &output})
	}()

	return stream
}

func buildCodexRequestBody(model Model, conversation Context, options *OpenAICodexResponsesOptions) codexRequestBody {
	messages := convertResponsesMessages(model, conversation, codexToolCallProviders, false)
	textVerbosity := options.TextVerbosity
	if textVerbosity == "" {
		textVerbosity = "medium"
	}
	body := codexRequestBody{
		Model:             model.ID,
		Store:             false,
		Stream:            true,
		Instructions:      conversation.SystemPrompt,
		Input:             messages,
		Text:              map[string]any{"verbosity": textVerbosity},
		Include:           []string{"reasoning.encrypted_content"},
		PromptCacheKey:    options.SessionID,
		ToolChoice:        "auto",
		ParallelToolCalls: true,
	}
	if options.Temperature != nil {
		body.Temperature = options.Temperature
	}
	if len(conversation.Tools) > 0 {
		strict := false
		body.Tools = convertResponsesTools(conversation.Tools, &strict)
	}
	if options.ReasoningEffort != "" {
		summary := options.ReasoningSummary
		if summary == "" {
			summary = "auto"
		}
		body.Reasoning = map[string]any{
			"effort":  clampCodexReasoningEffort(model.ID, options.ReasoningEffort),
			"summary": summary,
		}
	}
	return body
}

func clampCodexReasoningEffort(modelID string, effort ThinkingLevel) string {
	id := modelID
	if strings.Contains(id, "/") {
		parts := strings.Split(id, "/")
		id = parts[len(parts)-1]
	}
	if (strings.HasPrefix(id, "gpt-5.2") || strings.HasPrefix(id, "gpt-5.3")) && effort == ThinkingMinimal {
		return "low"
	}
	if id == "gpt-5.1" && effort == ThinkingXHigh {
		return "high"
	}
	if id == "gpt-5.1-codex-mini" && (effort == ThinkingHigh || effort == ThinkingXHigh) {
		return "high"
	}
	if id == "gpt-5.1-codex-mini" {
		return "medium"
	}
	return string(effort)
}

func resolveCodexURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		raw = defaultCodexBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	if strings.HasSuffix(normalized, "/codex/responses") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/codex") {
		return normalized + "/responses"
	}
	return normalized + "/codex/responses"
}

func resolveCodexWebSocketURL(baseURL string) string {
	resolved := resolveCodexURL(baseURL)
	u, err := url.Parse(resolved)
	if err != nil {
		return resolved
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	return u.String()
}

func supportsCodexWebSocketByDefault(baseURL string) bool {
	resolved := resolveCodexURL(baseURL)
	u, err := url.Parse(resolved)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "chatgpt.com")
}

func buildCodexHeaders(base, extra map[string]string, accountID, token, sessionID string) http.Header {
	headers := http.Header{}
	for k, v := range mergeHeaders(base, extra) {
		headers.Set(k, v)
	}
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("chatgpt-account-id", accountID)
	headers.Set("OpenAI-Beta", "responses=experimental")
	headers.Set("originator", "pi")
	headers.Set("User-Agent", fmt.Sprintf("pi (%s; %s)", runtime.GOOS, runtime.GOARCH))
	headers.Set("accept", "text/event-stream")
	headers.Set("content-type", "application/json")
	if sessionID != "" {
		headers.Set("conversation_id", sessionID)
		headers.Set("session_id", sessionID)
	}
	return headers
}

func extractCodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("failed to extract accountId from token")
	}
	payloadPart := parts[1]
	payloadPart = strings.ReplaceAll(payloadPart, "-", "+")
	payloadPart = strings.ReplaceAll(payloadPart, "_", "/")
	for len(payloadPart)%4 != 0 {
		payloadPart += "="
	}
	decoded, err := base64.StdEncoding.DecodeString(payloadPart)
	if err != nil {
		return "", errors.New("failed to extract accountId from token")
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return "", errors.New("failed to extract accountId from token")
	}
	auth, _ := payload[codexJWTClaimPath].(map[string]any)
	accountID, _ := auth["chatgpt_account_id"].(string)
	if accountID == "" {
		return "", errors.New("failed to extract accountId from token")
	}
	return accountID, nil
}

func isCodexRetryableError(status int, errorText string) bool {
	if status == 429 || status == 500 || status == 502 || status == 503 || status == 504 {
		return true
	}
	text := strings.ToLower(errorText)
	patterns := []string{"rate limit", "overloaded", "service unavailable", "upstream connect", "connection refused"}
	for _, p := range patterns {
		if strings.Contains(text, p) {
			return true
		}
	}
	return false
}

func doCodexSSERequestWithRetry(ctx context.Context, endpoint string, headers http.Header, body []byte, options *OpenAICodexResponsesOptions) (*http.Response, error) {
	maxDelay := 60 * time.Second
	if options != nil && options.MaxRetryDelayMS > 0 {
		maxDelay = time.Duration(options.MaxRetryDelayMS) * time.Millisecond
	}
	var lastErr error
	for attempt := 0; attempt <= codexMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		for k, values := range headers {
			for _, v := range values {
				req.Header.Add(k, v)
			}
		}

		resp, err := defaultHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			if attempt < codexMaxRetries {
				delay := codexBaseDelay * time.Duration(1<<attempt)
				if delay > maxDelay && maxDelay > 0 {
					delay = maxDelay
				}
				if err := sleepWithContext(ctx, delay); err != nil {
					return nil, err
				}
				continue
			}
			return nil, err
		}

		if resp.StatusCode < 400 {
			return resp, nil
		}

		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		message := strings.TrimSpace(string(raw))
		if attempt < codexMaxRetries && isCodexRetryableError(resp.StatusCode, message) {
			delay := codexBaseDelay * time.Duration(1<<attempt)
			if retry := parseRetryAfter(resp.Header); retry > 0 {
				delay = retry
			}
			if maxDelay > 0 && delay > maxDelay {
				return nil, fmt.Errorf("server requested retry delay %s exceeds maxRetryDelayMs cap", delay)
			}
			if err := sleepWithContext(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		if message == "" {
			message = "no body"
		}
		return nil, fmt.Errorf("%d status code (%s)", resp.StatusCode, message)
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("request failed after retries")
}

func mapCodexResponsesEvent(event map[string]any) map[string]any {
	typeValue := stringFrom(event, "type")
	if typeValue == "response.done" || typeValue == "response.completed" {
		response := mapFrom(event, "response")
		status := stringFrom(response, "status")
		if status != "" {
			response["status"] = normalizeCodexStatus(status)
		}
		event["type"] = "response.completed"
		event["response"] = response
		return event
	}
	return event
}

func normalizeCodexStatus(status string) string {
	switch status {
	case "completed", "incomplete", "failed", "cancelled", "queued", "in_progress":
		return status
	default:
		return ""
	}
}

// --- WebSocket transport support ---

type cachedWebSocketConnection struct {
	conn      *websocket.Conn
	busy      bool
	idleTimer *time.Timer
}

var (
	codexWSMu       sync.Mutex
	codexWSSessions = map[string]*cachedWebSocketConnection{}
)

const codexWebSocketReadLimitBytes int64 = 10 << 20 // 10 MiB

func processCodexWebSocket(ctx context.Context, wsURL string, body codexRequestBody, headers http.Header, output *AssistantMessage, stream *AssistantMessageEventStream, model Model, options *OpenAICodexResponsesOptions, started *bool) error {
	conn, release, err := acquireCodexWebSocket(ctx, wsURL, headers, options.SessionID)
	if err != nil {
		return err
	}
	keep := true
	defer release(&keep)

	payload := map[string]any{"type": "response.create"}
	blob, _ := json.Marshal(body)
	_ = json.Unmarshal(blob, &payload)
	if err := writeCodexWebSocketJSON(ctx, conn, payload); err != nil {
		keep = false
		return err
	}
	*started = true
	stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})

	state := &responsesStreamState{}
	sawCompletion := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			keep = false
			if sawCompletion {
				return nil
			}
			return err
		}
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		event = mapCodexResponsesEvent(event)
		t := stringFrom(event, "type")
		if t == "response.completed" {
			sawCompletion = true
		}
		if err := processResponsesStreamEvent(event, output, stream, model, state, nil); err != nil {
			keep = false
			return err
		}
		if sawCompletion {
			return nil
		}
	}
}

func writeCodexWebSocketJSON(ctx context.Context, conn *websocket.Conn, payload any) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, blob)
}

func acquireCodexWebSocket(ctx context.Context, wsURL string, headers http.Header, sessionID string) (*websocket.Conn, func(*bool), error) {
	if sessionID == "" {
		conn, err := dialCodexWebSocket(ctx, wsURL, headers)
		if err != nil {
			return nil, nil, err
		}
		release := func(keep *bool) {
			_ = conn.Close(websocket.StatusNormalClosure, "done")
		}
		return conn, release, nil
	}

	codexWSMu.Lock()
	if cached, ok := codexWSSessions[sessionID]; ok {
		if cached.idleTimer != nil {
			cached.idleTimer.Stop()
			cached.idleTimer = nil
		}
		if !cached.busy {
			cached.busy = true
			conn := cached.conn
			codexWSMu.Unlock()
			return conn, makeCodexWSRelease(sessionID, cached), nil
		}
	}
	codexWSMu.Unlock()

	conn, err := dialCodexWebSocket(ctx, wsURL, headers)
	if err != nil {
		return nil, nil, err
	}

	codexWSMu.Lock()
	entry := &cachedWebSocketConnection{conn: conn, busy: true}
	codexWSSessions[sessionID] = entry
	codexWSMu.Unlock()

	return conn, makeCodexWSRelease(sessionID, entry), nil
}

func makeCodexWSRelease(sessionID string, entry *cachedWebSocketConnection) func(*bool) {
	return func(keep *bool) {
		keepConn := keep != nil && *keep
		codexWSMu.Lock()
		defer codexWSMu.Unlock()
		if !keepConn {
			_ = entry.conn.Close(websocket.StatusNormalClosure, "done")
			if entry.idleTimer != nil {
				entry.idleTimer.Stop()
			}
			delete(codexWSSessions, sessionID)
			return
		}
		entry.busy = false
		if entry.idleTimer != nil {
			entry.idleTimer.Stop()
		}
		entry.idleTimer = time.AfterFunc(websocketSessionTTL, func() {
			codexWSMu.Lock()
			defer codexWSMu.Unlock()
			if entry.busy {
				return
			}
			_ = entry.conn.Close(websocket.StatusNormalClosure, "idle_timeout")
			if current, ok := codexWSSessions[sessionID]; ok && current == entry {
				delete(codexWSSessions, sessionID)
			}
		})
	}
}

func dialCodexWebSocket(ctx context.Context, wsURL string, headers http.Header) (*websocket.Conn, error) {
	headers.Set("OpenAI-Beta", "responses_websockets=2026-02-06")
	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(codexWebSocketReadLimitBytes)
	return conn, nil
}
