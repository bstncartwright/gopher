package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestFSPolicyBlocksTraversalAndAllowsInRoot(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	blockedCall := ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "read",
		Arguments: map[string]any{
			"path": "../secret.txt",
		},
	}
	blockedOutput, blockedErr := runner.Run(context.Background(), session, blockedCall)
	if blockedErr == nil {
		t.Fatalf("expected policy error for traversal")
	}
	if !IsPolicyError(blockedErr) {
		t.Fatalf("expected policy error type, got: %v", blockedErr)
	}
	if blockedOutput.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %q", blockedOutput.Status)
	}

	allowedCall := ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "write",
		Arguments: map[string]any{
			"path":    "notes/out.txt",
			"content": "hello",
		},
	}
	allowedOutput, allowedErr := runner.Run(context.Background(), session, allowedCall)
	if allowedErr != nil {
		t.Fatalf("expected write to succeed, got %v", allowedErr)
	}
	if allowedOutput.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", allowedOutput.Status)
	}

	blob, err := os.ReadFile(filepath.Join(workspace, "notes", "out.txt"))
	if err != nil {
		t.Fatalf("expected written file: %v", err)
	}
	if string(blob) != "hello" {
		t.Fatalf("unexpected file content: %q", string(blob))
	}
}

func TestFSPolicyAllowsCrossAgentAccessWhenEnabled(t *testing.T) {
	otherWorkspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())

	policies := defaultPolicies()
	policies.FSRoots = []string{"./", otherWorkspace}
	policies.AllowCrossAgentFS = true

	workspace := createTestWorkspace(t, defaultConfig(), policies)
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	targetPath := filepath.Join(otherWorkspace, "IDENTITY.md")
	output, runErr := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "edit",
		Arguments: map[string]any{
			"path":     targetPath,
			"old_text": "Test agent.",
			"new_text": "Cross-agent updated identity.",
		},
	})
	if runErr != nil {
		t.Fatalf("expected cross-agent edit success, got %v", runErr)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	blob, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target file: %v", err)
	}
	if string(blob) == "" || !strings.Contains(string(blob), "Cross-agent updated identity.") {
		t.Fatalf("expected updated target content, got: %q", string(blob))
	}
}
