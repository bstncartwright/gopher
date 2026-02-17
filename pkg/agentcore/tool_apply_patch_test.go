package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestApplyPatchAddFile(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	policies := defaultPolicies()
	policies.ApplyPatchEnabled = true
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "apply_patch",
		Arguments: map[string]any{
			"input": "*** Begin Patch\n*** Add File: hello.txt\n+hello world\n+second line\n*** End Patch",
		},
	})
	if err != nil {
		t.Fatalf("expected apply_patch to succeed, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	blob, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatalf("expected created file: %v", err)
	}
	content := string(blob)
	if !strings.Contains(content, "hello world") {
		t.Fatalf("expected file to contain 'hello world', got %q", content)
	}
	if !strings.Contains(content, "second line") {
		t.Fatalf("expected file to contain 'second line', got %q", content)
	}
}

func TestApplyPatchUpdateFile(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	policies := defaultPolicies()
	policies.ApplyPatchEnabled = true
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	mustWriteFile(t, filepath.Join(workspace, "existing.txt"), "line one\nline two\nline three\n")

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "apply_patch",
		Arguments: map[string]any{
			"input": "*** Begin Patch\n*** Update File: existing.txt\n@@\n-line two\n+line TWO UPDATED\n*** End Patch",
		},
	})
	if err != nil {
		t.Fatalf("expected apply_patch update to succeed, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	blob, err := os.ReadFile(filepath.Join(workspace, "existing.txt"))
	if err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
	content := string(blob)
	if !strings.Contains(content, "line TWO UPDATED") {
		t.Fatalf("expected updated content, got %q", content)
	}
	if strings.Contains(content, "line two") {
		t.Fatalf("expected old content to be replaced, got %q", content)
	}
}

func TestApplyPatchDeleteFile(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	policies := defaultPolicies()
	policies.ApplyPatchEnabled = true
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	mustWriteFile(t, filepath.Join(workspace, "doomed.txt"), "this will be deleted")

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "apply_patch",
		Arguments: map[string]any{
			"input": "*** Begin Patch\n*** Delete File: doomed.txt\n*** End Patch",
		},
	})
	if err != nil {
		t.Fatalf("expected apply_patch delete to succeed, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	if _, err := os.Stat(filepath.Join(workspace, "doomed.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, but it still exists")
	}
}

func TestApplyPatchBlocksPathTraversal(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	policies := defaultPolicies()
	policies.ApplyPatchEnabled = true
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	traversalPaths := []string{
		"../../../../tmp/evil.txt",
		"../../../etc/passwd",
		"/tmp/absolute_escape.txt",
	}

	for _, path := range traversalPaths {
		t.Run(path, func(t *testing.T) {
			output, err := runner.Run(context.Background(), session, ai.ContentBlock{
				Type: ai.ContentTypeToolCall,
				Name: "apply_patch",
				Arguments: map[string]any{
					"input": "*** Begin Patch\n*** Add File: " + path + "\n+pwned\n*** End Patch",
				},
			})
			if err == nil {
				t.Fatalf("expected error for path traversal with %q", path)
			}
			if output.Status != ToolStatusError {
				t.Fatalf("expected error status, got %q", output.Status)
			}
			resultMap, ok := output.Result.(map[string]any)
			if !ok {
				t.Fatalf("expected result map")
			}
			errMsg, _ := resultMap["error"].(string)
			if !strings.Contains(errMsg, "denied") {
				t.Fatalf("expected denial message, got %q", errMsg)
			}
		})
	}
}

func TestApplyPatchDeniedWhenDisabled(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	policies := defaultPolicies()
	policies.ApplyPatchEnabled = true
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	agent.Policies.ApplyPatchEnabled = false

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "apply_patch",
		Arguments: map[string]any{
			"input": "*** Begin Patch\n*** Add File: nope.txt\n+denied\n*** End Patch",
		},
	})
	if err == nil {
		t.Fatalf("expected policy error when apply_patch disabled")
	}
	if !IsPolicyError(err) {
		t.Fatalf("expected PolicyError type, got: %v", err)
	}
	if output.Status != ToolStatusDenied {
		t.Fatalf("expected denied status, got %q", output.Status)
	}
}
