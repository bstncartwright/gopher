package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/pelletier/go-toml/v2"
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
	wantKnown := []string{"planner", "writer"}
	for actorID, agent := range runtime.Agents {
		if !reflect.DeepEqual(agent.KnownAgents, wantKnown) {
			t.Fatalf("agent %q known agents = %#v, want %#v", actorID, agent.KnownAgents, wantKnown)
		}
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

func TestDiscoverGatewayAgentWorkspacesCreatesMainWorkspaceWhenMissing(t *testing.T) {
	workspace := t.TempDir()

	workspaces, err := discoverGatewayAgentWorkspaces(workspace)
	if err != nil {
		t.Fatalf("discoverGatewayAgentWorkspaces() error: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("workspaces len = %d, want 1", len(workspaces))
	}
	if got, want := workspaces[0], filepath.Join(workspace, "agents", "main"); got != want {
		t.Fatalf("workspace = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(workspace, "agents", "USER.md")); err != nil {
		t.Fatalf("expected shared USER.md to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "agents", "main", "config.toml")); err != nil {
		t.Fatalf("expected default config.toml to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "agents", "main", "policies.toml")); !os.IsNotExist(err) {
		t.Fatalf("did not expect policies.toml in default workspace, stat err=%v", err)
	}
	configBlob, err := os.ReadFile(filepath.Join(workspace, "agents", "main", "config.toml"))
	if err != nil {
		t.Fatalf("read default config.toml: %v", err)
	}
	if !strings.Contains(string(configBlob), "allow_cross_agent_fs = true") {
		t.Fatalf("expected main config to default allow_cross_agent_fs=true")
	}
}

func TestDiscoverGatewayAgentWorkspacesEnsuresSharedUserProfileForExistingWorkspace(t *testing.T) {
	workspace := t.TempDir()
	createGatewayTestAgentWorkspace(t, filepath.Join(workspace, "agents", "main"), "main")

	workspaces, err := discoverGatewayAgentWorkspaces(workspace)
	if err != nil {
		t.Fatalf("discoverGatewayAgentWorkspaces() error: %v", err)
	}
	if len(workspaces) != 1 {
		t.Fatalf("workspaces len = %d, want 1", len(workspaces))
	}
	if _, err := os.Stat(filepath.Join(workspace, "agents", "USER.md")); err != nil {
		t.Fatalf("expected shared USER.md to exist for existing workspace: %v", err)
	}
}

func TestLoadGatewayAgentRuntimeRecoversBlankMainAgentConfig(t *testing.T) {
	workspace := t.TempDir()
	mainPath := filepath.Join(workspace, "agents", "main")
	mustWriteFile(t, filepath.Join(mainPath, "AGENTS.md"), "# AGENTS\n")
	mustWriteFile(t, filepath.Join(mainPath, "SOUL.md"), "# SOUL\n")
	mustWriteFile(t, filepath.Join(mainPath, "config.toml"), `name = "main"
role = "assistant"
model_policy = ""
enabled_tools = ["group:fs", "group:runtime", "group:collaboration", "cron", "group:web"]
max_context_messages = 0

[policies]
can_shell = false
shell_allowlist = []
fs_roots = []
allow_cross_agent_fs = false
apply_patch_enabled = false
`)

	runtime, err := loadGatewayAgentRuntime(workspace)
	if err != nil {
		t.Fatalf("loadGatewayAgentRuntime() error: %v", err)
	}
	if runtime.DefaultActorID != sessionrt.ActorID("main") {
		t.Fatalf("default actor = %q, want main", runtime.DefaultActorID)
	}
	if _, ok := runtime.Agents[sessionrt.ActorID("main")]; !ok {
		t.Fatalf("expected recovered main agent to be loaded")
	}

	configBlob, err := os.ReadFile(filepath.Join(mainPath, "config.toml"))
	if err != nil {
		t.Fatalf("read repaired config.toml: %v", err)
	}
	var config map[string]any
	if err := toml.Unmarshal(configBlob, &config); err != nil {
		t.Fatalf("repaired config.toml should be valid TOML: %v", err)
	}
	if got, _ := config["agent_id"].(string); got != "main" {
		t.Fatalf("repaired config agent_id=%q, want main", got)
	}
	if got, _ := config["model_policy"].(string); got != defaultAgentModelPolicy {
		t.Fatalf("repaired config model_policy=%q, want %q", got, defaultAgentModelPolicy)
	}
}

func TestLoadGatewayAgentRuntimeDoesNotRecoverNonBlankModelPolicy(t *testing.T) {
	workspace := t.TempDir()
	mainPath := filepath.Join(workspace, "agents", "main")
	mustWriteFile(t, filepath.Join(mainPath, "AGENTS.md"), "# AGENTS\n")
	mustWriteFile(t, filepath.Join(mainPath, "SOUL.md"), "# SOUL\n")
	mustWriteFile(t, filepath.Join(mainPath, "config.toml"), `agent_id = "main"
name = "main"
role = "assistant"
model_policy = "openai"
`)

	_, err := loadGatewayAgentRuntime(workspace)
	if err == nil {
		t.Fatalf("expected invalid model policy error")
	}
	if !strings.Contains(err.Error(), `invalid model_policy "openai"`) {
		t.Fatalf("expected invalid model_policy error, got: %v", err)
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
	config.Policies = &policies

	configBlob, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	mustWriteFile(t, filepath.Join(dir, "AGENTS.md"), "# AGENTS\nDo the task.")
	mustWriteFile(t, filepath.Join(dir, "SOUL.md"), "# soul\nStay concise.")
	mustWriteFile(t, filepath.Join(dir, "config.json"), string(configBlob))
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
