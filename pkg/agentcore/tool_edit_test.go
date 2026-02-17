package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestEditToolReplacesExactMatch(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "hello.txt"), "hello world")

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "edit",
		Arguments: map[string]any{
			"path":     "hello.txt",
			"old_text": "hello",
			"new_text": "goodbye",
		},
	})
	if err != nil {
		t.Fatalf("expected edit to succeed, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	blob, err := os.ReadFile(filepath.Join(workspace, "hello.txt"))
	if err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
	if string(blob) != "goodbye world" {
		t.Fatalf("expected %q, got %q", "goodbye world", string(blob))
	}
}

func TestEditToolNotFound(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "file.txt"), "alpha beta gamma")

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "edit",
		Arguments: map[string]any{
			"path":     "file.txt",
			"old_text": "nonexistent",
			"new_text": "replaced",
		},
	})
	if err == nil {
		t.Fatalf("expected error for missing old_text")
	}
	if !strings.Contains(err.Error(), "old_text not found in file") {
		t.Fatalf("expected 'old_text not found in file' error, got %v", err)
	}
	if output.Status != ToolStatusError {
		t.Fatalf("expected error status, got %q", output.Status)
	}
}

func TestEditToolAmbiguousMatch(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "dup.txt"), "aaa\naaa\naaa")

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "edit",
		Arguments: map[string]any{
			"path":     "dup.txt",
			"old_text": "aaa",
			"new_text": "bbb",
		},
	})
	if err == nil {
		t.Fatalf("expected error for ambiguous old_text")
	}
	if !strings.Contains(err.Error(), "old_text is ambiguous") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	if !strings.Contains(err.Error(), "3 matches") {
		t.Fatalf("expected 3 matches in error, got %v", err)
	}
	if output.Status != ToolStatusError {
		t.Fatalf("expected error status, got %q", output.Status)
	}
}

func TestEditToolReturnsSnippet(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	mustWriteFile(t, filepath.Join(workspace, "snip.txt"), content)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	output, err := runner.Run(context.Background(), session, ai.ContentBlock{
		Type: ai.ContentTypeToolCall,
		Name: "edit",
		Arguments: map[string]any{
			"path":     "snip.txt",
			"old_text": "line5",
			"new_text": "REPLACED",
		},
	})
	if err != nil {
		t.Fatalf("expected edit to succeed, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected result to be map[string]any, got %T", output.Result)
	}
	snippet, ok := result["snippet"].(string)
	if !ok {
		t.Fatalf("expected snippet to be a string, got %T", result["snippet"])
	}
	if !strings.Contains(snippet, "REPLACED") {
		t.Fatalf("expected snippet to contain replacement text, got %q", snippet)
	}
	if !strings.Contains(snippet, "line4") || !strings.Contains(snippet, "line6") {
		t.Fatalf("expected snippet to contain surrounding context lines, got %q", snippet)
	}
}
