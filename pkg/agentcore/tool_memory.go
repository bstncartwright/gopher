package agentcore

import (
	"context"
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/memory"
)

type memorySearchTool struct{}

func (t *memorySearchTool) Name() string { return "memory_search" }

func (t *memorySearchTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Search canonical memory markdown files using hybrid retrieval with FTS fallback.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":       map[string]any{"type": "string"},
				"max_results": map[string]any{"type": "integer"},
				"min_score":   map[string]any{"type": "number"},
			},
			"required": []any{"query"},
		},
	}
}

func (t *memorySearchTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil || input.Agent.MemorySearch == nil {
		result := map[string]any{
			"disabled": true,
		}
		return ToolOutput{Status: ToolStatusOK, Result: result}, nil
	}
	query, err := requiredStringArg(input.Args, "query")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	maxResults := 0
	if raw, ok := input.Args["max_results"]; ok {
		if v, ok := toInt(raw); ok {
			maxResults = v
		}
	}
	minScore := 0.0
	if raw, ok := input.Args["min_score"]; ok {
		switch typed := raw.(type) {
		case float64:
			minScore = typed
		case int:
			minScore = float64(typed)
		}
	}

	resp, err := input.Agent.MemorySearch.Search(ctx, memory.MemorySearchRequest{
		Query:      query,
		MaxResults: maxResults,
		MinScore:   minScore,
		SessionKey: input.Session.ID,
	})
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	result := map[string]any{
		"mode":               strings.TrimSpace(resp.Mode),
		"provider":           strings.TrimSpace(resp.Provider),
		"model":              strings.TrimSpace(resp.Model),
		"fallback_reason":    strings.TrimSpace(resp.FallbackReason),
		"unavailable_reason": strings.TrimSpace(resp.UnavailableReason),
		"warning":            strings.TrimSpace(resp.Warning),
		"action":             strings.TrimSpace(resp.Action),
		"error":              strings.TrimSpace(resp.Error),
		"disabled":           resp.Disabled,
		"unavailable":        resp.Unavailable,
	}
	items := make([]map[string]any, 0, len(resp.Results))
	for _, item := range resp.Results {
		entry := map[string]any{
			"path":       item.Path,
			"start_line": item.StartLine,
			"end_line":   item.EndLine,
			"score":      item.Score,
			"snippet":    item.Snippet,
			"source":     item.Source,
		}
		if strings.TrimSpace(item.Citation) != "" {
			entry["citation"] = item.Citation
		}
		items = append(items, entry)
	}
	result["results"] = items
	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}

type memoryGetTool struct{}

func (t *memoryGetTool) Name() string { return "memory_get" }

func (t *memoryGetTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Read a small line window from MEMORY.md or memory/YYYY-MM-DD.md.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"from":  map[string]any{"type": "integer"},
				"lines": map[string]any{"type": "integer"},
			},
			"required": []any{"path"},
		},
	}
}

func (t *memoryGetTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil || input.Agent.MemorySearch == nil {
		return ToolOutput{Status: ToolStatusOK, Result: map[string]any{"path": "", "text": "", "start_line": 0, "end_line": 0}}, nil
	}
	path, err := requiredStringArg(input.Args, "path")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	from := 1
	if raw, ok := input.Args["from"]; ok {
		if v, ok := toInt(raw); ok {
			from = v
		}
	}
	lines := 40
	if raw, ok := input.Args["lines"]; ok {
		if v, ok := toInt(raw); ok {
			lines = v
		}
	}
	resp, err := input.Agent.MemorySearch.Read(ctx, memory.MemoryReadRequest{Path: path, From: from, Lines: lines})
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	result := map[string]any{
		"path":       resp.Path,
		"start_line": resp.StartLine,
		"end_line":   resp.EndLine,
		"text":       resp.Text,
	}
	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}

func memorySearchDiagnosticsStatus(agent *Agent, ctx context.Context) (memory.MemorySearchStatus, error) {
	if agent == nil || agent.MemorySearch == nil {
		return memory.MemorySearchStatus{Enabled: false, Mode: "disabled"}, nil
	}
	status, err := agent.MemorySearch.Status(ctx)
	if err != nil {
		return memory.MemorySearchStatus{}, fmt.Errorf("memory status: %w", err)
	}
	return status, nil
}
