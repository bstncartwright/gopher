//lint:file-ignore SA1019 nhooyr websocket is currently required for codex websocket compatibility in tests.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func TestStreamOpenAICodexResponsesSessionHeadersAndPayload(t *testing.T) {
	sessionID := "test-session-123"
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_test"},
	})
	token := "aaa." + base64.RawURLEncoding.EncodeToString(payload) + ".bbb"

	var sawConversationID string
	var sawSessionID string
	var sawPromptCacheKey string
	var sawStore any

	oldClient := defaultHTTPClient
	defer func() { defaultHTTPClient = oldClient }()
	defaultHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/codex/responses" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			sawConversationID = r.Header.Get("conversation_id")
			sawSessionID = r.Header.Get("session_id")

			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			sawPromptCacheKey = fmt.Sprint(body["prompt_cache_key"])
			sawStore = body["store"]

			events := []map[string]any{
				{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "in_progress", "content": []any{}}},
				{"type": "response.output_text.delta", "delta": "Hello"},
				{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Hello"}}}},
				{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8, "input_tokens_details": map[string]any{"cached_tokens": 0}}}},
			}
			var sse strings.Builder
			for _, event := range events {
				blob, _ := json.Marshal(event)
				fmt.Fprintf(&sse, "data: %s\n\n", blob)
			}
			sse.WriteString("data: [DONE]\n\n")

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(sse.String())),
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			}, nil
		}),
	}

	model := Model{
		ID:            "gpt-5.1-codex",
		Name:          "GPT-5.1 Codex",
		API:           APIOpenAICodexResponse,
		Provider:      ProviderOpenAICodex,
		BaseURL:       "https://example.test",
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 400000,
		MaxTokens:     128000,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamOpenAICodexResponses(
		model,
		Context{SystemPrompt: "You are helpful", Messages: []Message{{Role: RoleUser, Content: "hi", Timestamp: time.Now().UnixMilli()}}},
		&StreamOptions{APIKey: token, SessionID: sessionID, RequestContext: ctx},
	)

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected stop reason stop, got %q (%s)", result.StopReason, result.ErrorMessage)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "Hello" {
		t.Fatalf("expected text Hello, got %#v", result.Content)
	}
	if sawConversationID != sessionID {
		t.Fatalf("expected conversation_id %q, got %q", sessionID, sawConversationID)
	}
	if sawSessionID != sessionID {
		t.Fatalf("expected session_id %q, got %q", sessionID, sawSessionID)
	}
	if sawPromptCacheKey != sessionID {
		t.Fatalf("expected prompt_cache_key %q, got %q", sessionID, sawPromptCacheKey)
	}
	storeFlag, ok := sawStore.(bool)
	if !ok {
		t.Fatalf("expected store to be a boolean, got %#v", sawStore)
	}
	if storeFlag {
		t.Fatalf("expected store=false, got true")
	}
}

func TestStreamOpenAICodexResponsesAutoFallsBackAfterNormalWebSocketClosure(t *testing.T) {
	sessionID := "test-session-ws-fallback"
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_test"},
	})
	token := "aaa." + base64.RawURLEncoding.EncodeToString(payload) + ".bbb"
	var wsAttempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" {
			http.NotFound(w, r)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			wsAttempts.Add(1)
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close(websocket.StatusNormalClosure, "done")

			_, _, _ = conn.Read(r.Context())
			_ = conn.Close(websocket.StatusNormalClosure, "")
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		events := []map[string]any{
			{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "in_progress", "content": []any{}}},
			{"type": "response.output_text.delta", "delta": "Fallback Codex"},
			{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Fallback Codex"}}}},
			{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8, "input_tokens_details": map[string]any{"cached_tokens": 0}}}},
		}
		var sse strings.Builder
		for _, event := range events {
			blob, _ := json.Marshal(event)
			fmt.Fprintf(&sse, "data: %s\n\n", blob)
		}
		sse.WriteString("data: [DONE]\n\n")

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse.String()))
	}))
	defer server.Close()

	model := Model{
		ID:            "gpt-5.3-codex-spark",
		Name:          "GPT-5.3 Codex Spark",
		API:           APIOpenAICodexResponse,
		Provider:      ProviderOpenAICodex,
		BaseURL:       server.URL,
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 400000,
		MaxTokens:     128000,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamOpenAICodexResponses(
		model,
		Context{SystemPrompt: "You are helpful", Messages: []Message{{Role: RoleUser, Content: "hi", Timestamp: time.Now().UnixMilli()}}},
		&StreamOptions{APIKey: token, SessionID: sessionID, Transport: TransportAuto, RequestContext: ctx},
	)

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected stop reason stop, got %q (%s)", result.StopReason, result.ErrorMessage)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "Fallback Codex" {
		t.Fatalf("expected text Fallback Codex, got %#v", result.Content)
	}
	if wsAttempts.Load() == 0 {
		t.Fatalf("expected at least one websocket attempt before SSE fallback")
	}
}

func TestResolveOpenAICodexResponsesTransportDefaultsToAuto(t *testing.T) {
	model := Model{
		Provider: ProviderOpenAICodex,
		BaseURL:  "https://chatgpt.com/backend-api",
	}
	if got := resolveOpenAICodexResponsesTransport(model, &OpenAICodexResponsesOptions{}); got != TransportAuto {
		t.Fatalf("transport = %q, want %q", got, TransportAuto)
	}
}

func TestStreamOpenAICodexResponsesWebSocketAcceptsLargeEventFrames(t *testing.T) {
	sessionID := "test-session-ws-large-frame"
	payload, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_test"},
	})
	token := "aaa." + base64.RawURLEncoding.EncodeToString(payload) + ".bbb"
	largeDelta := strings.Repeat("x", 40*1024)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" {
			http.NotFound(w, r)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}

		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")

		if _, _, err := conn.Read(r.Context()); err != nil {
			return
		}

		events := []map[string]any{
			{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "in_progress", "content": []any{}}},
			{"type": "response.output_text.delta", "delta": largeDelta},
			{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Large frame ok"}}}},
			{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 5, "output_tokens": 3, "total_tokens": 8, "input_tokens_details": map[string]any{"cached_tokens": 0}}}},
		}
		for _, event := range events {
			blob, _ := json.Marshal(event)
			if err := conn.Write(r.Context(), websocket.MessageText, blob); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	model := Model{
		ID:            "gpt-5.3-codex-spark",
		Name:          "GPT-5.3 Codex Spark",
		API:           APIOpenAICodexResponse,
		Provider:      ProviderOpenAICodex,
		BaseURL:       server.URL,
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 400000,
		MaxTokens:     128000,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamOpenAICodexResponses(
		model,
		Context{SystemPrompt: "You are helpful", Messages: []Message{{Role: RoleUser, Content: "hi", Timestamp: time.Now().UnixMilli()}}},
		&StreamOptions{APIKey: token, SessionID: sessionID, Transport: TransportWebSocket, RequestContext: ctx},
	)

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected stop reason stop, got %q (%s)", result.StopReason, result.ErrorMessage)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "Large frame ok" {
		t.Fatalf("expected text Large frame ok, got %#v", result.Content)
	}
}
