package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStreamOpenAICompletionsBasicText(t *testing.T) {
	oldClient := defaultHTTPClient
	defer func() { defaultHTTPClient = oldClient }()

	defaultHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/chat/completions" {
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found")), Header: make(http.Header)}, nil
			}
			chunks := []map[string]any{
				{
					"choices": []any{map[string]any{"delta": map[string]any{"content": "Hello"}, "finish_reason": nil}},
					"usage": map[string]any{
						"prompt_tokens":             1,
						"completion_tokens":         1,
						"prompt_tokens_details":     map[string]any{"cached_tokens": 0},
						"completion_tokens_details": map[string]any{"reasoning_tokens": 0},
					},
				},
				{
					"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
				},
			}
			var sse strings.Builder
			for _, chunk := range chunks {
				blob, _ := json.Marshal(chunk)
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
		ID:            "test-model",
		Name:          "Test",
		API:           APIOpenAICompletions,
		Provider:      ProviderOpenAI,
		BaseURL:       "https://example.test",
		Reasoning:     false,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 4096,
		MaxTokens:     512,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamOpenAICompletions(model, Context{Messages: []Message{{Role: RoleUser, Content: "hi", Timestamp: time.Now().UnixMilli()}}}, &StreamOptions{APIKey: "test", RequestContext: ctx})

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected stop, got %q", result.StopReason)
	}
	if len(result.Content) == 0 || result.Content[0].Type != ContentTypeText {
		t.Fatalf("expected text block, got %#v", result.Content)
	}
	if got := result.Content[0].Text; got != "Hello" {
		t.Fatalf("expected text Hello, got %q", got)
	}
}

func TestStreamOpenAICompletionsGitHubCopilotRequestShape(t *testing.T) {
	oldClient := defaultHTTPClient
	defer func() { defaultHTTPClient = oldClient }()

	var gotPath string
	var gotHeaders http.Header
	var gotPayload map[string]any

	defaultHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotPath = r.URL.String()
			gotHeaders = r.Header.Clone()
			if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}

			chunks := []map[string]any{
				{
					"choices": []any{map[string]any{"delta": map[string]any{"content": "Hello"}, "finish_reason": nil}},
					"usage": map[string]any{
						"prompt_tokens":             1,
						"completion_tokens":         1,
						"prompt_tokens_details":     map[string]any{"cached_tokens": 0},
						"completion_tokens_details": map[string]any{"reasoning_tokens": 0},
					},
				},
				{
					"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
				},
			}
			var sse strings.Builder
			for _, chunk := range chunks {
				blob, _ := json.Marshal(chunk)
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
		ID:            "gpt-5.4",
		Name:          "GPT-5.4",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Headers:       defaultGitHubCopilotHeaders(),
		Reasoning:     true,
		Input:         []string{"text", "image"},
		Cost:          ModelCost{},
		ContextWindow: 400000,
		MaxTokens:     128000,
		Compat:        githubCopilotCompat(true),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream := StreamOpenAICompletions(model, Context{
		SystemPrompt: "You are helpful.",
		Messages: []Message{
			NewUserBlocksMessage([]ContentBlock{
				{Type: ContentTypeText, Text: "See this image"},
				{Type: ContentTypeImage, Data: "ZmFrZQ==", MimeType: "image/png"},
			}),
			{Role: RoleAssistant, Content: []ContentBlock{{Type: ContentTypeText, Text: "Prior answer"}}, Timestamp: time.Now().UnixMilli()},
		},
	}, &StreamOptions{APIKey: "tid=1;proxy-ep=proxy.individual.githubcopilot.com", RequestContext: ctx})

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if gotPath != "https://api.individual.githubcopilot.com/chat/completions" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotHeaders.Get("Authorization") == "" {
		t.Fatalf("expected authorization header")
	}
	if gotHeaders.Get("X-Initiator") != "agent" {
		t.Fatalf("X-Initiator = %q, want agent", gotHeaders.Get("X-Initiator"))
	}
	if gotHeaders.Get("Openai-Intent") != "conversation-edits" {
		t.Fatalf("Openai-Intent = %q", gotHeaders.Get("Openai-Intent"))
	}
	if gotHeaders.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("Copilot-Vision-Request = %q", gotHeaders.Get("Copilot-Vision-Request"))
	}
	if gotHeaders.Get("Editor-Version") == "" {
		t.Fatalf("expected static Copilot headers")
	}
	if _, ok := gotPayload["store"]; ok {
		t.Fatalf("did not expect store param for Copilot payload")
	}
	messages, ok := gotPayload["messages"].([]any)
	if !ok || len(messages) < 3 {
		t.Fatalf("messages payload = %#v", gotPayload["messages"])
	}
	assistant, ok := messages[2].(map[string]any)
	if !ok {
		t.Fatalf("assistant payload = %#v", messages[2])
	}
	if got := assistant["content"]; got != "Prior answer" {
		t.Fatalf("assistant content = %#v, want string Prior answer", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
