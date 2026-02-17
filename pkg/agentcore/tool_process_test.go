package agentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestProcessListEmpty(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:runtime"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "process",
		Arguments: map[string]any{
			"action": "list",
		},
	})
	if err != nil {
		t.Fatalf("expected process list to succeed, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected structured result map")
	}
	sessions, ok := result["sessions"].([]map[string]any)
	if !ok {
		t.Fatalf("expected sessions to be []map[string]any, got %T", result["sessions"])
	}
	if len(sessions) != 0 {
		t.Fatalf("expected empty sessions list, got %d entries", len(sessions))
	}
}

func TestProcessPollNotFound(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:runtime"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "process",
		Arguments: map[string]any{
			"action":     "poll",
			"session_id": "nonexistent-session",
		},
	})
	if err == nil {
		t.Fatalf("expected error for poll on nonexistent session")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("expected error status, got %q", output.Status)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' in error, got %q", err.Error())
	}
}

func TestProcessUnknownAction(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:runtime"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "process",
		Arguments: map[string]any{
			"action": "bogus",
		},
	})
	if err == nil {
		t.Fatalf("expected error for unknown action")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("expected error status, got %q", output.Status)
	}
	if !strings.Contains(err.Error(), "not one of the allowed enum values") {
		t.Fatalf("expected enum validation error, got %q", err.Error())
	}
}
