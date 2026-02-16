package agentcore

import (
	"context"
	"os"
	"path/filepath"
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
		Name: "fs.read",
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
		Name: "fs.write",
		Arguments: map[string]any{
			"path":    "notes/out.txt",
			"content": "hello",
		},
	}
	allowedOutput, allowedErr := runner.Run(context.Background(), session, allowedCall)
	if allowedErr != nil {
		t.Fatalf("expected fs.write to succeed, got %v", allowedErr)
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
