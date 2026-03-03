package agentcore

import (
	"context"
	"testing"
)

type aliasCaptureTool struct {
	name    string
	ran     bool
	lastArg map[string]any
}

func (t *aliasCaptureTool) Name() string {
	return t.name
}

func (t *aliasCaptureTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.name,
		Description: "capture tool calls for alias normalization tests",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"probe": map[string]any{"type": "string"},
			},
		},
	}
}

func (t *aliasCaptureTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	t.ran = true
	t.lastArg = input.Args
	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{"executed_tool": t.name},
	}, nil
}

func TestToolRunnerNormalizesWebAliases(t *testing.T) {
	tests := []struct {
		name      string
		toolAlias string
		canonical string
	}{
		{name: "fetch_content alias", toolAlias: "fetch_content", canonical: "web_fetch"},
		{name: "fetch_mcp alias", toolAlias: "fetch_mcp", canonical: "web_fetch"},
		{name: "search_mcp alias", toolAlias: "search_mcp", canonical: "web_search"},
		{name: "search alias", toolAlias: "search", canonical: "web_search"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
			agent, err := LoadAgent(workspace)
			if err != nil {
				t.Fatalf("LoadAgent() error: %v", err)
			}
			capture := &aliasCaptureTool{name: tc.canonical}
			agent.Tools = NewToolRegistry([]Tool{capture})

			runner := NewToolRunner(agent)
			_, err = runner.Run(context.Background(), agent.NewSession(), toolCall(tc.toolAlias, map[string]any{"probe": "ok"}))
			if err != nil {
				t.Fatalf("Run() error: %v", err)
			}
			if !capture.ran {
				t.Fatalf("expected canonical tool %q to execute for alias %q", tc.canonical, tc.toolAlias)
			}
			if got, _ := capture.lastArg["probe"].(string); got != "ok" {
				t.Fatalf("probe argument = %q, want ok", got)
			}
		})
	}
}

