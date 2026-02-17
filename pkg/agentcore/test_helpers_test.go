package agentcore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func defaultConfig() AgentConfig {
	return AgentConfig{
		AgentID:            "agent-test",
		Name:               "Test Agent",
		Role:               "coder",
		ModelPolicy:        "openai:gpt-4o-mini",
		EnabledTools:       []string{"group:fs", "group:runtime"},
		MaxContextMessages: 40,
	}
}

func defaultPolicies() AgentPolicies {
	return AgentPolicies{
		FSRoots:        []string{"./"},
		CanShell:       true,
		ShellAllowlist: []string{"echo", "git", "go", "bun", "node", "bash"},
		Network: NetworkPolicy{
			Enabled:      true,
			AllowDomains: []string{"*"},
		},
		Budget: BudgetPolicy{MaxTokensPerSession: 200000},
	}
}

func createTestWorkspace(t *testing.T, config AgentConfig, policies AgentPolicies) string {
	t.Helper()

	dir := t.TempDir()
	mustWriteFile(t, filepath.Join(dir, "AGENTS.md"), "# Contract\nDo the task.")
	mustWriteFile(t, filepath.Join(dir, "soul.md"), "# Soul\nBe direct.")

	configBlob, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	policiesBlob, err := json.Marshal(policies)
	if err != nil {
		t.Fatalf("marshal policies: %v", err)
	}

	mustWriteFile(t, filepath.Join(dir, "config.json"), string(configBlob))
	mustWriteFile(t, filepath.Join(dir, "policies.json"), string(policiesBlob))
	return dir
}

func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
