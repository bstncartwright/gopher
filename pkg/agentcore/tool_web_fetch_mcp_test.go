package agentcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebFetchMCPToolRunSuccessWithExaPrimary(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer exa-key" {
			t.Fatalf("unexpected authorization header: %q", auth)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		method := strings.TrimSpace(req["method"].(string))
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-123")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": defaultMCPProtocolVersion}})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": map[string]any{}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{map[string]any{"name": "crawling_exa"}}}})
		case "tools/call":
			params := req["params"].(map[string]any)
			if gotName := strings.TrimSpace(params["name"].(string)); gotName != "crawling_exa" {
				t.Fatalf("tools/call name = %q, want crawling_exa", gotName)
			}
			args := params["arguments"].(map[string]any)
			if got := strings.TrimSpace(args["url"].(string)); got != "https://example.com" {
				t.Fatalf("url = %q, want https://example.com", got)
			}
			urls, ok := args["urls"].([]any)
			if !ok || len(urls) != 2 {
				t.Fatalf("urls = %#v, want 2 urls", args["urls"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "exa extract"}}}})
		default:
			t.Fatalf("unexpected method: %s", method)
		}
	}))
	defer exaServer.Close()

	tavilyCalled := false
	tavilyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		tavilyCalled = true
	}))
	defer tavilyServer.Close()

	tool := newWebFetchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider(tavilyServer.URL, "TAVILY_API_KEY", tavilyServer.Client()),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{
		"url":  "https://example.com",
		"urls": []any{"https://example.com", "https://example.org"},
	}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if got := strings.TrimSpace(result["provider"].(string)); got != "exa" {
		t.Fatalf("provider = %q, want exa", got)
	}
	if got, _ := result["fallback_used"].(bool); got {
		t.Fatalf("fallback_used = true, want false")
	}
	if tavilyCalled {
		t.Fatalf("expected no tavily fallback call")
	}
}

func TestWebFetchMCPToolRunFallsBackToTavilyOnQuota(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 402, "message": "out of credits"}})
	}))
	defer exaServer.Close()

	tavilyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-tv")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": map[string]any{}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{map[string]any{"name": "tavily_extract"}}}})
		case "tools/call":
			params := req["params"].(map[string]any)
			if gotName := strings.TrimSpace(params["name"].(string)); gotName != "tavily_extract" {
				t.Fatalf("tools/call name = %q, want tavily_extract", gotName)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "tavily extract"}}}})
		default:
			t.Fatalf("unexpected method: %v", req["method"])
		}
	}))
	defer tavilyServer.Close()

	tool := newWebFetchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider(tavilyServer.URL, "TAVILY_API_KEY", tavilyServer.Client()),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{
		"url": "https://example.com",
	}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if got := strings.TrimSpace(result["provider"].(string)); got != "tavily" {
		t.Fatalf("provider = %q, want tavily", got)
	}
	if got, _ := result["fallback_used"].(bool); !got {
		t.Fatalf("fallback_used = false, want true")
	}
}

func TestWebFetchMCPToolRunFallsBackToTavilyOnRateLimitWhenProviderOverrideExa(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 429, "message": "rate limit exceeded"}})
	}))
	defer exaServer.Close()

	tavilyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-tv")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": map[string]any{}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{map[string]any{"name": "tavily_extract"}}}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "tavily extract"}}}})
		default:
			t.Fatalf("unexpected method: %v", req["method"])
		}
	}))
	defer tavilyServer.Close()

	tool := newWebFetchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider(tavilyServer.URL, "TAVILY_API_KEY", tavilyServer.Client()),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{
		"url":      "https://example.com",
		"provider": "exa",
	}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if got := strings.TrimSpace(result["provider"].(string)); got != "tavily" {
		t.Fatalf("provider = %q, want tavily", got)
	}
	if got, _ := result["fallback_used"].(bool); !got {
		t.Fatalf("fallback_used = false, want true")
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(result["fallback_reason"].(string))), "rate") {
		t.Fatalf("fallback_reason = %v, want rate-limit", result["fallback_reason"])
	}
}

func TestWebFetchMCPToolRunRequiresURL(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	tool := newWebFetchMCPTool()
	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{}})
	if err == nil {
		t.Fatalf("expected error for missing url input")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want error", output.Status)
	}
}
