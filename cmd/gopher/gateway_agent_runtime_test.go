package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestDiscoverGatewayAgentWorkspacesFindsPerAgentDirectories(t *testing.T) {
	workspace := t.TempDir()
	plannerPath := filepath.Join(workspace, "agents", "planner")
	writerPath := filepath.Join(workspace, "agents", "writer")
	createGatewayTestAgentWorkspace(t, plannerPath, "planner")
	createGatewayTestAgentWorkspace(t, writerPath, "writer")

	workspaces, err := discoverGatewayAgentWorkspaces(workspace)
	if err != nil {
		t.Fatalf("discoverGatewayAgentWorkspaces() error: %v", err)
	}
	if len(workspaces) != 2 {
		t.Fatalf("workspaces len = %d, want 2", len(workspaces))
	}
	if workspaces[0] != plannerPath {
		t.Fatalf("workspaces[0] = %q, want %q", workspaces[0], plannerPath)
	}
	if workspaces[1] != writerPath {
		t.Fatalf("workspaces[1] = %q, want %q", workspaces[1], writerPath)
	}
}

func TestLoadGatewayAgentRuntimeUsesLexicographicDefaultActor(t *testing.T) {
	workspace := t.TempDir()
	createGatewayTestAgentWorkspace(t, filepath.Join(workspace, "agents", "writer"), "writer")
	createGatewayTestAgentWorkspace(t, filepath.Join(workspace, "agents", "planner"), "planner")

	runtime, err := loadGatewayAgentRuntime(workspace)
	if err != nil {
		t.Fatalf("loadGatewayAgentRuntime() error: %v", err)
	}
	if runtime.DefaultActorID != sessionrt.ActorID("planner") {
		t.Fatalf("default actor = %q, want planner", runtime.DefaultActorID)
	}
	if len(runtime.Agents) != 2 {
		t.Fatalf("agents len = %d, want 2", len(runtime.Agents))
	}
}

func TestLoadGatewayAgentRuntimeRejectsDuplicateAgentIDs(t *testing.T) {
	workspace := t.TempDir()
	createGatewayTestAgentWorkspace(t, filepath.Join(workspace, "agents", "first"), "planner")
	createGatewayTestAgentWorkspace(t, filepath.Join(workspace, "agents", "second"), "planner")

	_, err := loadGatewayAgentRuntime(workspace)
	if err == nil {
		t.Fatalf("expected duplicate agent id error")
	}
	if !strings.Contains(err.Error(), "duplicate agent id") {
		t.Fatalf("expected duplicate agent id error, got: %v", err)
	}
}

func TestDiscoverGatewayAgentWorkspacesRejectsLegacyNestedPath(t *testing.T) {
	workspace := t.TempDir()
	legacyPath := filepath.Join(workspace, ".gopher", "agents", "planner")
	createGatewayTestAgentWorkspace(t, legacyPath, "planner")

	_, err := discoverGatewayAgentWorkspaces(workspace)
	if err == nil {
		t.Fatalf("expected error for legacy nested path")
	}
	if !strings.Contains(err.Error(), "/agents/<agent_id>") {
		t.Fatalf("expected agents path guidance, got: %v", err)
	}
}

func createGatewayTestAgentWorkspace(t *testing.T, dir, agentID string) {
	t.Helper()
	config := agentcore.AgentConfig{
		AgentID:            agentID,
		Name:               "Test " + agentID,
		Role:               "assistant",
		ModelPolicy:        "openai:gpt-4o-mini",
		EnabledTools:       []string{"group:fs"},
		MaxContextMessages: 40,
	}
	policies := agentcore.AgentPolicies{
		FSRoots: []string{"./"},
		Network: agentcore.NetworkPolicy{
			Enabled:      false,
			AllowDomains: []string{},
		},
		Budget: agentcore.BudgetPolicy{MaxTokensPerSession: 10000},
	}

	configBlob, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	policiesBlob, err := json.Marshal(policies)
	if err != nil {
		t.Fatalf("marshal policies: %v", err)
	}

	mustWriteFile(t, filepath.Join(dir, "AGENTS.md"), "# AGENTS\nDo the task.")
	mustWriteFile(t, filepath.Join(dir, "SOUL.md"), "# soul\nStay concise.")
	mustWriteFile(t, filepath.Join(dir, "config.json"), string(configBlob))
	mustWriteFile(t, filepath.Join(dir, "policies.json"), string(policiesBlob))
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
