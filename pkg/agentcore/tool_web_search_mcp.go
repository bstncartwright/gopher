package agentcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

const (
	defaultWebSearchMCPEndpoint = "https://api.z.ai/api/mcp/web_search_prime/mcp"
	defaultMCPProtocolVersion   = "2025-06-18"
)

var preferredWebSearchMCPToolNames = []string{
	"web_search_prime",
	"web-search-prime",
	"web_search",
	"search",
}

type webSearchMCPTool struct {
	endpoint string
	client   *http.Client
}

func newWebSearchMCPTool() *webSearchMCPTool {
	return &webSearchMCPTool{
		endpoint: defaultWebSearchMCPEndpoint,
		client: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func (t *webSearchMCPTool) Name() string {
	return "web_search"
}

func (t *webSearchMCPTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Search the web through Z.ai's MCP web_search_prime endpoint.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query.",
				},
				"params": map[string]any{
					"type":        "object",
					"description": "Additional MCP tool arguments to forward.",
				},
				"tool_name": map[string]any{
					"type":        "string",
					"description": "Override the MCP tool name to call.",
				},
			},
			"required": []any{"query"},
		},
	}
}

func (t *webSearchMCPTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query, err := requiredStringArg(input.Args, "query")
	if err != nil {
		return t.fail("", err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return t.fail(query, fmt.Errorf("query is required"))
	}

	apiKey := strings.TrimSpace(ai.GetEnvAPIKey(string(ai.ProviderZAI)))
	if apiKey == "" {
		return t.fail(query, fmt.Errorf("web_search requires ZAI_API_KEY"))
	}

	extraParams, err := optionalMapArg(input.Args, "params")
	if err != nil {
		return t.fail(query, err)
	}
	toolName, _ := optionalStringArg(input.Args, "tool_name")

	sessionID := ""
	initialize := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": defaultMCPProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "gopher",
				"version": "1.0.0",
			},
		},
	}
	if _, sessionID, err = t.callMCP(ctx, apiKey, sessionID, initialize); err != nil {
		return t.fail(query, err)
	}

	initialized := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	}
	if _, sessionID, err = t.callMCP(ctx, apiKey, sessionID, initialized); err != nil {
		return t.fail(query, err)
	}

	listToolsReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	listPayload, nextSessionID, err := t.callMCP(ctx, apiKey, sessionID, listToolsReq)
	if err != nil {
		return t.fail(query, err)
	}
	sessionID = nextSessionID

	availableTools := extractMCPToolNames(listPayload)
	if len(availableTools) == 0 {
		return t.fail(query, fmt.Errorf("mcp tools/list returned no tools"))
	}

	selectedTool := selectMCPToolName(strings.TrimSpace(toolName), availableTools)
	args := map[string]any{"query": query}
	for key, value := range extraParams {
		args[key] = value
	}
	args["query"] = query

	callToolReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      selectedTool,
			"arguments": args,
		},
	}
	callPayload, _, err := t.callMCP(ctx, apiKey, sessionID, callToolReq)
	if err != nil {
		return t.fail(query, err)
	}

	summary := extractMCPCallSummary(callPayload)
	result := map[string]any{
		"endpoint":        t.endpoint,
		"query":           query,
		"selected_tool":   selectedTool,
		"available_tools": availableTools,
		"mcp_result":      callPayload,
	}
	if summary != "" {
		result["summary"] = summary
	}

	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}

func (t *webSearchMCPTool) fail(query string, err error) (ToolOutput, error) {
	out := ToolOutput{
		Status: ToolStatusError,
		Result: map[string]any{
			"error":    err.Error(),
			"endpoint": t.endpoint,
			"query":    strings.TrimSpace(query),
		},
	}
	return out, err
}

func (t *webSearchMCPTool) callMCP(ctx context.Context, apiKey string, sessionID string, payload map[string]any) (map[string]any, string, error) {
	if t.client == nil {
		t.client = &http.Client{Timeout: 45 * time.Second}
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return nil, sessionID, fmt.Errorf("marshal mcp payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(blob))
	if err != nil {
		return nil, sessionID, fmt.Errorf("build mcp request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, sessionID, fmt.Errorf("mcp request failed: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, sessionID, fmt.Errorf("read mcp response: %w", err)
	}

	nextSession := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	if nextSession == "" {
		nextSession = sessionID
	}

	parsed, parseErr := parseMCPResponsePayload(responseBody, resp.Header.Get("Content-Type"))
	if parseErr != nil {
		if resp.StatusCode >= 400 {
			return nil, nextSession, fmt.Errorf("mcp %d: %s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
		}
		if len(bytes.TrimSpace(responseBody)) == 0 {
			return map[string]any{}, nextSession, nil
		}
		return nil, nextSession, fmt.Errorf("parse mcp response: %w", parseErr)
	}

	if resp.StatusCode >= 400 {
		return nil, nextSession, fmt.Errorf("mcp %d: %s", resp.StatusCode, summarizeMCPError(parsed))
	}

	if successRaw, exists := parsed["success"]; exists {
		if success, ok := successRaw.(bool); ok && !success {
			code := formatMCPCode(parsed["code"])
			msg := strings.TrimSpace(fmt.Sprintf("%v", parsed["msg"]))
			if code != "" && msg != "" {
				return nil, nextSession, fmt.Errorf("mcp wrapper error %s: %s", code, msg)
			}
			if msg != "" {
				return nil, nextSession, fmt.Errorf("mcp wrapper error: %s", msg)
			}
			return nil, nextSession, fmt.Errorf("mcp wrapper returned success=false")
		}
	}

	if errObj, ok := parsed["error"].(map[string]any); ok {
		msg := strings.TrimSpace(fmt.Sprintf("%v", errObj["message"]))
		code := formatMCPCode(errObj["code"])
		if code != "" && msg != "" {
			return nil, nextSession, fmt.Errorf("json-rpc error %s: %s", code, msg)
		}
		if msg != "" {
			return nil, nextSession, fmt.Errorf("json-rpc error: %s", msg)
		}
		return nil, nextSession, fmt.Errorf("json-rpc error: %v", errObj)
	}

	return parsed, nextSession, nil
}

func parseMCPResponsePayload(body []byte, contentType string) (map[string]any, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}

	var out map[string]any
	if err := json.Unmarshal(trimmed, &out); err == nil {
		return out, nil
	}

	if strings.Contains(strings.ToLower(contentType), "text/event-stream") || bytes.Contains(trimmed, []byte("data:")) {
		var last map[string]any
		scanner := bufio.NewScanner(bytes.NewReader(trimmed))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var item map[string]any
			if err := json.Unmarshal([]byte(payload), &item); err == nil {
				last = item
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		if last != nil {
			return last, nil
		}
	}

	return nil, fmt.Errorf("unsupported mcp response format")
}

func extractMCPToolNames(payload map[string]any) []string {
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return nil
	}
	rawTools, ok := result["tools"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawTools))
	for _, rawTool := range rawTools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(fmt.Sprintf("%v", toolMap["name"]))
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func selectMCPToolName(override string, available []string) string {
	if override != "" {
		return override
	}
	availableSet := make(map[string]struct{}, len(available))
	for _, item := range available {
		availableSet[item] = struct{}{}
	}
	for _, candidate := range preferredWebSearchMCPToolNames {
		if _, ok := availableSet[candidate]; ok {
			return candidate
		}
	}
	return available[0]
}

func extractMCPCallSummary(payload map[string]any) string {
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return ""
	}
	if text := strings.TrimSpace(fmt.Sprintf("%v", result["output_text"])); text != "" && text != "<nil>" {
		return text
	}
	if text := strings.TrimSpace(fmt.Sprintf("%v", result["text"])); text != "" && text != "<nil>" {
		return text
	}
	blocks, ok := result["content"].([]any)
	if !ok {
		return ""
	}
	lines := make([]string, 0, len(blocks))
	for _, block := range blocks {
		entry, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(fmt.Sprintf("%v", entry["type"])) != "text" {
			continue
		}
		text := strings.TrimSpace(fmt.Sprintf("%v", entry["text"]))
		if text != "" && text != "<nil>" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

func summarizeMCPError(payload map[string]any) string {
	if payload == nil {
		return "unknown error"
	}
	if msg := strings.TrimSpace(fmt.Sprintf("%v", payload["msg"])); msg != "" && msg != "<nil>" {
		return msg
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg := strings.TrimSpace(fmt.Sprintf("%v", errObj["message"])); msg != "" && msg != "<nil>" {
			return msg
		}
	}
	return strings.TrimSpace(fmt.Sprintf("%v", payload))
}

func formatMCPCode(raw any) string {
	switch typed := raw.(type) {
	case int:
		return fmt.Sprintf("%d", typed)
	case int32:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case float32:
		return fmt.Sprintf("%.0f", typed)
	case float64:
		return fmt.Sprintf("%.0f", typed)
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func optionalMapArg(args map[string]any, key string) (map[string]any, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return map[string]any{}, nil
	}
	out, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return ai.CloneMap(out), nil
}
