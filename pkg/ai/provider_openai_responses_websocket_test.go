package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestStreamOpenAIResponsesWebSocketMode(t *testing.T) {
	createPayload := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
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

		_, raw, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var request map[string]any
		if err := json.Unmarshal(raw, &request); err == nil {
			createPayload <- request
		}

		events := []map[string]any{
			{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "in_progress", "content": []any{}}},
			{"type": "response.output_text.delta", "delta": "Hello"},
			{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Hello"}}}},
			{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6, "input_tokens_details": map[string]any{"cached_tokens": 0}}}},
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
		ID:            "gpt-4.1-mini",
		Name:          "GPT-4.1 mini",
		API:           APIOpenAIResponses,
		Provider:      ProviderOpenAI,
		BaseURL:       server.URL + "/v1",
		Reasoning:     false,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 4096,
		MaxTokens:     512,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamOpenAIResponses(model, Context{
		Messages: []Message{{Role: RoleUser, Content: "hi", Timestamp: time.Now().UnixMilli()}},
	}, &StreamOptions{
		APIKey:         "test",
		Transport:      TransportWebSocket,
		RequestContext: ctx,
	})

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

	var request map[string]any
	select {
	case request = <-createPayload:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for websocket request payload")
	}
	if got := fmt.Sprint(request["type"]); got != "response.create" {
		t.Fatalf("expected websocket event type response.create, got %q", got)
	}
	if _, ok := request["stream"]; ok {
		t.Fatalf("expected websocket payload without stream field, got %#v", request["stream"])
	}
}

func TestStreamOpenAIResponsesAutoFallsBackToSSE(t *testing.T) {
	var wsAttempts atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			wsAttempts.Add(1)
			http.Error(w, "websocket unsupported", http.StatusUpgradeRequired)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		events := []map[string]any{
			{"type": "response.output_item.added", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "in_progress", "content": []any{}}},
			{"type": "response.output_text.delta", "delta": "Fallback"},
			{"type": "response.output_item.done", "item": map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Fallback"}}}},
			{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6, "input_tokens_details": map[string]any{"cached_tokens": 0}}}},
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
		ID:            "gpt-4.1-mini",
		Name:          "GPT-4.1 mini",
		API:           APIOpenAIResponses,
		Provider:      ProviderOpenAI,
		BaseURL:       server.URL + "/v1",
		Reasoning:     false,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 4096,
		MaxTokens:     512,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamOpenAIResponses(model, Context{
		Messages: []Message{{Role: RoleUser, Content: "hi", Timestamp: time.Now().UnixMilli()}},
	}, &StreamOptions{
		APIKey:         "test",
		Transport:      TransportAuto,
		RequestContext: ctx,
	})

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected stop reason stop, got %q (%s)", result.StopReason, result.ErrorMessage)
	}
	if len(result.Content) == 0 || result.Content[0].Text != "Fallback" {
		t.Fatalf("expected text Fallback, got %#v", result.Content)
	}
	if wsAttempts.Load() == 0 {
		t.Fatalf("expected at least one websocket attempt before SSE fallback")
	}
}
