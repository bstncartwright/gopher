package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestMatrixAttachmentResolverFromWriteToolResult(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "report.md")
	if err := os.WriteFile(target, []byte("# report\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	runtime := &gatewayAgentRuntime{
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"writer": {Workspace: workspace},
		},
	}
	resolver := newMatrixAttachmentResolver(runtime)
	if resolver == nil {
		t.Fatalf("resolver is nil")
	}

	attachments := resolver("!dm:local", "writer", sessionrt.Event{
		Type: sessionrt.EventToolResult,
		Payload: map[string]any{
			"name":   "write",
			"status": "ok",
			"result": map[string]any{"path": "report.md"},
		},
	})
	if len(attachments) != 1 {
		t.Fatalf("attachments length = %d, want 1", len(attachments))
	}
	if attachments[0].Path != target {
		t.Fatalf("attachment path = %q, want %q", attachments[0].Path, target)
	}
	if attachments[0].Name != "report.md" {
		t.Fatalf("attachment name = %q, want report.md", attachments[0].Name)
	}
}

func TestMatrixAttachmentResolverSkipsReadToolResults(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "SOUL.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	runtime := &gatewayAgentRuntime{
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"writer": {Workspace: workspace},
		},
	}
	resolver := newMatrixAttachmentResolver(runtime)
	if resolver == nil {
		t.Fatalf("resolver is nil")
	}

	attachments := resolver("!dm:local", "writer", sessionrt.Event{
		Type: sessionrt.EventToolResult,
		Payload: map[string]any{
			"name":   "read",
			"status": "ok",
			"result": map[string]any{"path": "SOUL.md"},
		},
	})
	if len(attachments) != 0 {
		t.Fatalf("attachments length = %d, want 0", len(attachments))
	}
}

func TestMatrixAttachmentResolverSkipsPathsOutsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	runtime := &gatewayAgentRuntime{
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"writer": {Workspace: workspace},
		},
	}
	resolver := newMatrixAttachmentResolver(runtime)
	if resolver == nil {
		t.Fatalf("resolver is nil")
	}

	attachments := resolver("!dm:local", "writer", sessionrt.Event{
		Type: sessionrt.EventToolResult,
		Payload: map[string]any{
			"name":   "write",
			"status": "ok",
			"result": map[string]any{"files": []any{outside}},
		},
	})
	if len(attachments) != 0 {
		t.Fatalf("attachments length = %d, want 0", len(attachments))
	}
}

func TestMatrixAttachmentResolverAllowsExplicitAttachmentFields(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "image.png")
	if err := os.WriteFile(target, []byte("png"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	runtime := &gatewayAgentRuntime{
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"writer": {Workspace: workspace},
		},
	}
	resolver := newMatrixAttachmentResolver(runtime)
	if resolver == nil {
		t.Fatalf("resolver is nil")
	}

	attachments := resolver("!dm:local", "writer", sessionrt.Event{
		Type: sessionrt.EventToolResult,
		Payload: map[string]any{
			"name":   "exec",
			"status": "ok",
			"result": map[string]any{
				"attachments": []any{"image.png"},
			},
		},
	})
	if len(attachments) != 1 {
		t.Fatalf("attachments length = %d, want 1", len(attachments))
	}
	if attachments[0].Path != target {
		t.Fatalf("attachment path = %q, want %q", attachments[0].Path, target)
	}
}
