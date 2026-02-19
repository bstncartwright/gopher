package agentcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchMCPToolRunSuccess(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	var sawSessionHeader bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Fatalf("unexpected authorization header: %q", auth)
		}
		if r.URL.Path != "/" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}

		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		method := strings.TrimSpace(req["method"].(string))

		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-123")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result":  map[string]any{"protocolVersion": defaultMCPProtocolVersion},
			})
		case "notifications/initialized":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sess-123" {
				t.Fatalf("notifications request missing session id: %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"result":  map[string]any{},
			})
		case "tools/list":
			if got := r.Header.Get("Mcp-Session-Id"); got != "sess-123" {
				t.Fatalf("tools/list request missing session id: %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      2,
				"result": map[string]any{
					"tools": []any{
						map[string]any{"name": "web_search_prime"},
						map[string]any{"name": "other"},
					},
				},
			})
		case "tools/call":
			if got := r.Header.Get("Mcp-Session-Id"); got == "sess-123" {
				sawSessionHeader = true
			}
			params := req["params"].(map[string]any)
			if gotName := strings.TrimSpace(params["name"].(string)); gotName != "web_search_prime" {
				t.Fatalf("tools/call name = %q, want web_search_prime", gotName)
			}
			args := params["arguments"].(map[string]any)
			if gotQuery := strings.TrimSpace(args["query"].(string)); gotQuery != "what is mcp" {
				t.Fatalf("query = %q, want what is mcp", gotQuery)
			}
			if gotLang := strings.TrimSpace(args["lang"].(string)); gotLang != "en" {
				t.Fatalf("lang = %q, want en", gotLang)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":3,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"summary line\"}]}}\n\n"))
		default:
			t.Fatalf("unexpected method: %s", method)
		}
	}))
	defer server.Close()

	tool := newWebSearchMCPTool()
	tool.endpoint = server.URL
	tool.client = server.Client()

	output, err := tool.Run(context.Background(), ToolInput{
		Args: map[string]any{
			"query": "what is mcp",
			"params": map[string]any{
				"lang": "en",
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result")
	}
	if got := strings.TrimSpace(result["selected_tool"].(string)); got != "web_search_prime" {
		t.Fatalf("selected_tool = %q, want web_search_prime", got)
	}
	if got := strings.TrimSpace(result["summary"].(string)); got != "summary line" {
		t.Fatalf("summary = %q, want summary line", got)
	}
	if !sawSessionHeader {
		t.Fatalf("expected tools/call to include MCP session header")
	}
}

func TestWebSearchMCPToolRunMissingAPIKey(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	tool := newWebSearchMCPTool()

	output, err := tool.Run(context.Background(), ToolInput{
		Args: map[string]any{"query": "hello"},
	})
	if err == nil {
		t.Fatalf("expected error for missing ZAI_API_KEY")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want error", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if !strings.Contains(strings.TrimSpace(result["error"].(string)), "ZAI_API_KEY") {
		t.Fatalf("expected ZAI_API_KEY in error message, got: %v", result["error"])
	}
}

func TestWebSearchMCPToolRunHandlesWrapperError(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"code":    401,
			"msg":     "token expired or incorrect",
		})
	}))
	defer server.Close()

	tool := newWebSearchMCPTool()
	tool.endpoint = server.URL
	tool.client = server.Client()

	output, err := tool.Run(context.Background(), ToolInput{
		Args: map[string]any{"query": "latest ai news"},
	})
	if err == nil {
		t.Fatalf("expected wrapper error")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want error", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if !strings.Contains(strings.ToLower(strings.TrimSpace(result["error"].(string))), "wrapper") {
		t.Fatalf("expected wrapper error message, got: %v", result["error"])
	}
}

func TestWebSearchMCPToolRunErrorsWhenToolsListEmpty(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      2,
				"result":  map[string]any{"tools": []any{}},
			})
		default:
			t.Fatalf("unexpected method: %v", req["method"])
		}
	}))
	defer server.Close()

	tool := newWebSearchMCPTool()
	tool.endpoint = server.URL
	tool.client = server.Client()

	output, err := tool.Run(context.Background(), ToolInput{
		Args: map[string]any{"query": "latest ai news"},
	})
	if err == nil {
		t.Fatalf("expected empty tools list error")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want error", output.Status)
	}
	result, _ := output.Result.(map[string]any)
	if !strings.Contains(strings.ToLower(strings.TrimSpace(result["error"].(string))), "no tools") {
		t.Fatalf("expected no tools error message, got: %v", result["error"])
	}
}

func TestWebSearchMCPToolRunRespectsToolNameOverride(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "test-key")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      2,
				"result": map[string]any{
					"tools": []any{
						map[string]any{"name": "web_search_prime"},
					},
				},
			})
		case "tools/call":
			params := req["params"].(map[string]any)
			if gotName := strings.TrimSpace(params["name"].(string)); gotName != "custom_search_tool" {
				t.Fatalf("tools/call name = %q, want custom_search_tool", gotName)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      3,
				"result": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "override summary"}},
				},
			})
		default:
			t.Fatalf("unexpected method: %v", req["method"])
		}
	}))
	defer server.Close()

	tool := newWebSearchMCPTool()
	tool.endpoint = server.URL
	tool.client = server.Client()

	output, err := tool.Run(context.Background(), ToolInput{
		Args: map[string]any{
			"query":     "latest ai news",
			"tool_name": "custom_search_tool",
		},
	})
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
