package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestCodeExecToolRunsBashAndCapturesArtifact(t *testing.T) {
	tool := &codeExecTool{}
	workdir := t.TempDir()
	artifactPath := filepath.Join(workdir, "report.txt")

	output, err := tool.Run(context.Background(), ToolInput{
		Agent:   &Agent{Workspace: workdir},
		Session: &Session{ID: "session-code-exec-bash"},
		Args: map[string]any{
			"runtime": "bash",
			"source": strings.Join([]string{
				"echo bash-ok",
				"printf 'artifact-ready\\n' > report.txt",
			}, "\n"),
			"workdir": workdir,
			"capture_paths": []any{
				artifactPath,
			},
		},
	})
	if err != nil {
		t.Fatalf("expected code_exec success, got %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("expected ok status, got %q", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected structured result map")
	}
	stdout, _ := result["stdout"].(string)
	if !strings.Contains(stdout, "bash-ok") {
		t.Fatalf("expected stdout to contain bash-ok, got %q", stdout)
	}
	artifacts, ok := result["artifacts"].([]map[string]any)
	if ok {
		if len(artifacts) != 1 {
			t.Fatalf("artifacts len = %d, want 1", len(artifacts))
		}
		content, _ := artifacts[0]["content"].(string)
		if !strings.Contains(content, "artifact-ready") {
			t.Fatalf("expected artifact content, got %#v", artifacts[0])
		}
		return
	}

	artifactEntries, ok := result["artifacts"].([]any)
	if !ok || len(artifactEntries) != 1 {
		t.Fatalf("expected one artifact entry, got %#v", result["artifacts"])
	}
	artifact, ok := artifactEntries[0].(map[string]any)
	if !ok {
		t.Fatalf("expected artifact map, got %#v", artifactEntries[0])
	}
	content, _ := artifact["content"].(string)
	if !strings.Contains(content, "artifact-ready") {
		t.Fatalf("expected artifact content, got %#v", artifact)
	}
}

func TestCodeExecToolCleansTemporaryRunDir(t *testing.T) {
	tool := &codeExecTool{}
	workspace := t.TempDir()
	baseDir := filepath.Join(workspace, ".gopher", "code_exec")

	_, err := tool.Run(context.Background(), ToolInput{
		Agent:   &Agent{Workspace: workspace},
		Session: &Session{ID: "session-code-exec-cleanup"},
		Args: map[string]any{
			"runtime": "bash",
			"source":  "echo cleanup",
			"workdir": workspace,
		},
	})
	if err != nil {
		t.Fatalf("expected code_exec success, got %v", err)
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		t.Fatalf("expected code_exec base dir to exist, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected temporary run dirs to be cleaned up, found %d entries", len(entries))
	}
}

func TestCodeExecPolicyEnforcement(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"code_exec"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	t.Run("denied_when_can_shell_false", func(t *testing.T) {
		agent.Policies.CanShell = false
		_, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "code_exec",
			Arguments: map[string]any{
				"runtime": "bash",
				"source":  "echo denied",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected code_exec policy error when can_shell=false, got %v", err)
		}
		agent.Policies.CanShell = true
	})

	t.Run("denied_when_runtime_not_in_allowlist", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"bash"}
		_, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "code_exec",
			Arguments: map[string]any{
				"runtime": "python",
				"source":  "print('denied')",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected code_exec allowlist error, got %v", err)
		}
		if !strings.Contains(err.Error(), "shell_allowlist") {
			t.Fatalf("expected shell_allowlist in error, got %v", err)
		}
	})

	t.Run("resolves_capture_paths_in_allowed_roots", func(t *testing.T) {
		agent.Policies.ShellAllowlist = nil
		artifactPath := filepath.Join(workspace, "artifact.txt")
		output, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "workspace_runner",
			Arguments: map[string]any{
				"runtime": "bash",
				"source":  "printf 'artifact' > artifact.txt",
				"capture_paths": []any{
					"./artifact.txt",
				},
			},
		})
		if err != nil {
			t.Fatalf("expected code_exec success, got %v", err)
		}
		if output.Status != ToolStatusOK {
			t.Fatalf("expected ok status, got %q", output.Status)
		}
		if _, err := os.Stat(artifactPath); err != nil {
			t.Fatalf("expected artifact file to exist, got %v", err)
		}
	})
}
