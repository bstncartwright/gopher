package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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
	var sawPromptCacheRetention string

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
			sawPromptCacheRetention = fmt.Sprint(body["prompt_cache_retention"])

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
	if !strings.EqualFold(sawPromptCacheRetention, "in-memory") {
		t.Fatalf("expected prompt_cache_retention in-memory, got %q", sawPromptCacheRetention)
	}
}
