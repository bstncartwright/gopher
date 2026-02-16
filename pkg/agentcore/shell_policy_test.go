package agentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestShellAllowlistEnforcement(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"shell"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	agent.Policies.CanShell = false
	_, err = runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "shell.exec",
		Arguments: map[string]any{
			"command": "echo",
		},
	})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected shell policy error when can_shell=false, got %v", err)
	}

	agent.Policies.CanShell = true
	_, err = runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "shell.exec",
		Arguments: map[string]any{
			"command": "uname",
		},
	})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected allowlist policy error, got %v", err)
	}

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "shell.exec",
		Arguments: map[string]any{
			"command": "echo",
			"args":    []any{"hello"},
		},
	})
	if err != nil {
		t.Fatalf("expected shell.exec success, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected structured result map")
	}
	stdout, _ := result["stdout"].(string)
	if !strings.Contains(stdout, "hello") {
		t.Fatalf("expected stdout to contain hello, got %q", stdout)
	}
}
