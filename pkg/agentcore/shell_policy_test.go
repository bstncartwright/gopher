package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestExecPolicyEnforcement(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"exec"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	fakeBinDir := t.TempDir()
	fakeOpencodePath := filepath.Join(fakeBinDir, "opencode")
	if err := os.WriteFile(fakeOpencodePath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("failed to create fake opencode binary: %v", err)
	}
	t.Setenv("PATH", fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	t.Run("denied_when_can_shell_false", func(t *testing.T) {
		agent.Policies.CanShell = false
		_, err = runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo hello",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected exec policy error when can_shell=false, got %v", err)
		}
		agent.Policies.CanShell = true
	})

	t.Run("denied_when_not_in_allowlist", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo", "git"}
		_, err = runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "curl http://evil.com",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected allowlist policy error for curl, got %v", err)
		}
		if !strings.Contains(err.Error(), "shell_allowlist") {
			t.Fatalf("expected shell_allowlist in error, got: %v", err)
		}
	})

	t.Run("allowed_when_shell_operators_present", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo", "git"}
		output, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo hello && echo world",
			},
		})
		if err != nil {
			t.Fatalf("expected exec success for shell operators, got %v", err)
		}
		if output.Status != ToolStatusOK {
			t.Fatalf("expected ok status, got %q", output.Status)
		}
	})

	t.Run("denied_when_chained_segment_not_in_allowlist", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo", "git"}
		_, err = runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo hello; curl http://evil.com",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected allowlist policy error for chained curl, got %v", err)
		}
		if !strings.Contains(err.Error(), "shell_allowlist") {
			t.Fatalf("expected shell_allowlist in error, got: %v", err)
		}
	})

	t.Run("denied_when_pipeline_segment_not_in_allowlist", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo"}
		_, err = runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo hello | grep h",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected allowlist policy error for piped grep, got %v", err)
		}
		if !strings.Contains(err.Error(), "shell_allowlist") {
			t.Fatalf("expected shell_allowlist in error, got: %v", err)
		}
	})

	t.Run("denied_when_command_substitution_present_in_allowlist_mode", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo"}
		_, err = runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo $(whoami)",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected policy error for command substitution, got %v", err)
		}
		if !strings.Contains(err.Error(), "command substitution") {
			t.Fatalf("expected command substitution in error, got: %v", err)
		}
	})

	t.Run("denied_when_redirection_present_in_allowlist_mode", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo"}
		_, err = runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo hello > out.txt",
			},
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected policy error for redirection, got %v", err)
		}
		if !strings.Contains(err.Error(), "redirections") {
			t.Fatalf("expected redirections in error, got: %v", err)
		}
	})

	t.Run("allowed_when_in_allowlist", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo", "git"}
		output, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo hello",
			},
		})
		if err != nil {
			t.Fatalf("expected exec success, got %v", err)
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
	})

	t.Run("allowed_when_allowlist_empty", func(t *testing.T) {
		agent.Policies.ShellAllowlist = nil
		output, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "echo open_allowlist",
			},
		})
		if err != nil {
			t.Fatalf("expected exec success with empty allowlist, got %v", err)
		}
		if output.Status != ToolStatusOK {
			t.Fatalf("expected ok status, got %q", output.Status)
		}
	})

	t.Run("extracts_binary_from_full_path", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo"}
		output, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "/bin/echo path_test",
			},
		})
		if err != nil {
			t.Fatalf("expected exec success with full path, got %v", err)
		}
		if output.Status != ToolStatusOK {
			t.Fatalf("expected ok status, got %q", output.Status)
		}
	})

	t.Run("allows_env_prefix_when_command_is_allowlisted", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"echo"}
		output, err := runner.Run(context.Background(), session, ai.ContentBlock{
			Type: ai.ContentTypeToolCall,
			Name: "exec",
			Arguments: map[string]any{
				"command": "FOO=bar echo env_test",
			},
		})
		if err != nil {
			t.Fatalf("expected exec success with env prefix, got %v", err)
		}
		if output.Status != ToolStatusOK {
			t.Fatalf("expected ok status, got %q", output.Status)
		}
	})

	t.Run("opencode_requires_run_subcommand", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"opencode"}
		_, err := runner.enforcePolicy("exec", map[string]any{
			"command": "opencode --format json \"write code\"",
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected opencode run-subcommand policy error, got %v", err)
		}
		if !strings.Contains(err.Error(), "run") {
			t.Fatalf("expected run-subcommand guidance, got: %v", err)
		}
	})

	t.Run("opencode_requires_format_json", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"opencode"}
		_, err := runner.enforcePolicy("exec", map[string]any{
			"command": "opencode run \"write code\"",
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected opencode format policy error, got %v", err)
		}
		if !strings.Contains(err.Error(), "--format json") {
			t.Fatalf("expected --format json guidance, got: %v", err)
		}
	})

	t.Run("opencode_rejects_interactive_flags", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"opencode"}
		_, err := runner.enforcePolicy("exec", map[string]any{
			"command": "opencode run --format json --continue \"write code\"",
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected opencode interactive-flag policy error, got %v", err)
		}
		if !strings.Contains(err.Error(), "interactive") {
			t.Fatalf("expected interactive flag guidance, got: %v", err)
		}
	})

	t.Run("opencode_rejects_dir_flag", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"opencode"}
		_, err := runner.enforcePolicy("exec", map[string]any{
			"command": "opencode run --format json --dir /tmp \"write code\"",
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected opencode --dir policy error, got %v", err)
		}
		if !strings.Contains(err.Error(), "--dir") {
			t.Fatalf("expected --dir guidance, got: %v", err)
		}
	})

	t.Run("opencode_rejects_background_true", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"opencode"}
		_, err := runner.enforcePolicy("exec", map[string]any{
			"command":    "opencode run --format json \"write code\"",
			"background": true,
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected opencode background policy error, got %v", err)
		}
		if !strings.Contains(err.Error(), "background") {
			t.Fatalf("expected background guidance, got: %v", err)
		}
	})

	t.Run("opencode_allows_valid_one_shot_json_command", func(t *testing.T) {
		agent.Policies.ShellAllowlist = []string{"opencode"}
		sanitized, err := runner.enforcePolicy("exec", map[string]any{
			"command": "opencode run --format json \"write code\"",
		})
		if err != nil {
			t.Fatalf("expected opencode policy success, got %v", err)
		}
		if _, ok := sanitized["workdir"].(string); !ok {
			t.Fatalf("expected resolved workdir in sanitized args")
		}
	})

	t.Run("opencode_missing_binary_returns_actionable_error", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		agent.Policies.ShellAllowlist = []string{"opencode"}
		_, err := runner.enforcePolicy("exec", map[string]any{
			"command": "opencode run --format json \"write code\"",
		})
		if err == nil || !IsPolicyError(err) {
			t.Fatalf("expected missing-binary policy error, got %v", err)
		}
		if !strings.Contains(err.Error(), "not found in PATH") {
			t.Fatalf("expected PATH guidance, got: %v", err)
		}
	})
}
