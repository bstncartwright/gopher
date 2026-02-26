package agentcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchMCPToolRunSuccessWithExaPrimary(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	var sawSessionHeader bool
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
			if got := r.Header.Get("Mcp-Session-Id"); got != "sess-123" {
				t.Fatalf("notifications request missing session id: %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": map[string]any{}})
		case "tools/list":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sess-123" {
				t.Fatalf("tools/list request missing session id: %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{map[string]any{"name": "web_search_exa"}}}})
		case "tools/call":
			if got := r.Header.Get("Mcp-Session-Id"); got == "sess-123" {
				sawSessionHeader = true
			}
			params := req["params"].(map[string]any)
			if gotName := strings.TrimSpace(params["name"].(string)); gotName != "web_search_exa" {
				t.Fatalf("tools/call name = %q, want web_search_exa", gotName)
			}
			args := params["arguments"].(map[string]any)
			if gotQuery := strings.TrimSpace(args["query"].(string)); gotQuery != "what is mcp" {
				t.Fatalf("query = %q, want what is mcp", gotQuery)
			}
			if gotSearchQuery := strings.TrimSpace(args["search_query"].(string)); gotSearchQuery != "what is mcp" {
				t.Fatalf("search_query = %q, want what is mcp", gotSearchQuery)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "exa summary"}}}})
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

	tool := newWebSearchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider(tavilyServer.URL, "TAVILY_API_KEY", tavilyServer.Client()),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{"query": "what is mcp"}})
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
	if got := strings.TrimSpace(result["selected_tool"].(string)); got != "web_search_exa" {
		t.Fatalf("selected_tool = %q, want web_search_exa", got)
	}
	if got := strings.TrimSpace(result["summary"].(string)); got != "exa summary" {
		t.Fatalf("summary = %q, want exa summary", got)
	}
	if tavilyCalled {
		t.Fatalf("expected no tavily fallback call")
	}
	if !sawSessionHeader {
		t.Fatalf("expected tools/call to include MCP session header")
	}
}

func TestWebSearchMCPToolRunFallsBackToTavilyOnQuota(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 402, "message": "out of credits"}})
	}))
	defer exaServer.Close()

	tavilyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer tavily-key" {
			t.Fatalf("unexpected authorization header: %q", auth)
		}
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
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{map[string]any{"name": "tavily_search"}}}})
		case "tools/call":
			params := req["params"].(map[string]any)
			if gotName := strings.TrimSpace(params["name"].(string)); gotName != "tavily_search" {
				t.Fatalf("tools/call name = %q, want tavily_search", gotName)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "tavily summary"}}}})
		default:
			t.Fatalf("unexpected method: %v", req["method"])
		}
	}))
	defer tavilyServer.Close()

	tool := newWebSearchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider(tavilyServer.URL, "TAVILY_API_KEY", tavilyServer.Client()),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{"query": "latest ai news"}})
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
	if !strings.Contains(strings.ToLower(strings.TrimSpace(result["fallback_reason"].(string))), "quota") {
		t.Fatalf("fallback_reason = %v, want quota", result["fallback_reason"])
	}
}

func TestWebSearchMCPToolRunFallsBackToTavilyOnRateLimit(t *testing.T) {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{map[string]any{"name": "tavily_search"}}}})
		case "tools/call":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "tavily summary"}}}})
		default:
			t.Fatalf("unexpected method: %v", req["method"])
		}
	}))
	defer tavilyServer.Close()

	tool := newWebSearchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider(tavilyServer.URL, "TAVILY_API_KEY", tavilyServer.Client()),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{"query": "latest ai news"}})
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

func TestWebSearchMCPToolRunDoesNotFallbackOnUnauthorized(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 401, "message": "unauthorized"}})
	}))
	defer exaServer.Close()

	tavilyCalled := false
	tavilyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		tavilyCalled = true
	}))
	defer tavilyServer.Close()

	tool := newWebSearchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider(tavilyServer.URL, "TAVILY_API_KEY", tavilyServer.Client()),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{"query": "latest ai news"}})
	if err == nil {
		t.Fatalf("expected unauthorized error")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want error", output.Status)
	}
	if tavilyCalled {
		t.Fatalf("did not expect tavily fallback for unauthorized exa error")
	}
}

func TestWebSearchMCPToolRunMissingEXAAPIKey(t *testing.T) {
	t.Setenv("EXA_API_KEY", "")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	tool := newWebSearchMCPTool()
	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{"query": "hello"}})
	if err == nil {
		t.Fatalf("expected error for missing EXA_API_KEY")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want error", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if !strings.Contains(strings.TrimSpace(result["error"].(string)), "EXA_API_KEY") {
		t.Fatalf("expected EXA_API_KEY in error message, got: %v", result["error"])
	}
}

func TestWebSearchMCPToolRunFallbackMissingTavilyKey(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "")

	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": 429, "message": "rate limit exceeded"}})
	}))
	defer exaServer.Close()

	tool := newWebSearchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider("http://127.0.0.1:1", "TAVILY_API_KEY", http.DefaultClient),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{"query": "latest ai news"}})
	if err == nil {
		t.Fatalf("expected fallback error when TAVILY_API_KEY is missing")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want error", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if !strings.Contains(strings.TrimSpace(result["error"].(string)), "TAVILY_API_KEY") {
		t.Fatalf("expected TAVILY_API_KEY in error message, got: %v", result["error"])
	}
}

func TestWebSearchMCPToolRunRespectsToolNameOverride(t *testing.T) {
	t.Setenv("EXA_API_KEY", "exa-key")
	t.Setenv("TAVILY_API_KEY", "tavily-key")

	exaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req["method"] {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-123")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{}})
		case "notifications/initialized":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "result": map[string]any{}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"tools": []any{map[string]any{"name": "web_search_exa"}}}})
		case "tools/call":
			params := req["params"].(map[string]any)
			if gotName := strings.TrimSpace(params["name"].(string)); gotName != "custom_search_tool" {
				t.Fatalf("tools/call name = %q, want custom_search_tool", gotName)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "override summary"}}}})
		default:
			t.Fatalf("unexpected method: %v", req["method"])
		}
	}))
	defer exaServer.Close()

	tool := newWebSearchMCPToolWithProviders(
		newExaMCPProvider(exaServer.URL, "EXA_API_KEY", exaServer.Client()),
		newTavilyMCPProvider("http://127.0.0.1:1", "TAVILY_API_KEY", http.DefaultClient),
	)

	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{"query": "latest ai news", "tool_name": "custom_search_tool"}})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if got := strings.TrimSpace(result["selected_tool"].(string)); got != "custom_search_tool" {
		t.Fatalf("selected_tool = %q, want custom_search_tool", got)
	}
}
