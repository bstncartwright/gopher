package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestAgentCreateListDeleteLifecycle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(dir, "agents", "index.json")
	workspaceRoot := filepath.Join(dir, "workspaces")

	var out bytes.Buffer
	if err := runAgentSubcommand([]string{
		"create",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
		"--id", "planner",
		"--user-id", "tg:planner",
	}, &out, &out); err != nil {
		t.Fatalf("create planner failed: %v", err)
	}

	plannerWorkspace := filepath.Join(workspaceRoot, "planner")
	for _, name := range []string{
		"AGENTS.md", "SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md", "BOOTSTRAP.md", "config.toml",
	} {
		if _, err := os.Stat(filepath.Join(plannerWorkspace, name)); err != nil {
			t.Fatalf("expected workspace file %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(plannerWorkspace, "policies.toml")); !os.IsNotExist(err) {
		t.Fatalf("did not expect policies.toml scaffold file, stat err=%v", err)
	}
	for _, date := range []string{
		time.Now().Format("2006-01-02"),
		time.Now().AddDate(0, 0, -1).Format("2006-01-02"),
	} {
		if _, err := os.Stat(filepath.Join(plannerWorkspace, "memory", date+".md")); err != nil {
			t.Fatalf("expected memory note %s: %v", date, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "USER.md")); err != nil {
		t.Fatalf("expected shared USER.md in workspace root: %v", err)
	}

	var listOut bytes.Buffer
	if err := runAgentSubcommand([]string{
		"list",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
	}, &listOut, &listOut); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if got := listOut.String(); !strings.Contains(got, "planner | active | tg:planner") {
		t.Fatalf("unexpected list output: %s", got)
	}

	if err := runAgentSubcommand([]string{
		"delete",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
		"--id", "planner",
	}, &out, &out); err != nil {
		t.Fatalf("soft delete failed: %v", err)
	}
	if _, err := os.Stat(plannerWorkspace); err != nil {
		t.Fatalf("workspace should still exist after soft delete: %v", err)
	}

	if err := runAgentSubcommand([]string{
		"create",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
		"--id", "builder",
		"--user-id", "tg:builder",
	}, &out, &out); err != nil {
		t.Fatalf("create builder failed: %v", err)
	}
	builderWorkspace := filepath.Join(workspaceRoot, "builder")
	if err := runAgentSubcommand([]string{
		"remove",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
		"--id", "builder",
		"--hard",
	}, &out, &out); err != nil {
		t.Fatalf("hard delete failed: %v", err)
	}
	if _, err := os.Stat(builderWorkspace); !os.IsNotExist(err) {
		t.Fatalf("workspace should be removed after hard delete, stat err=%v", err)
	}

	var listAfterDelete bytes.Buffer
	if err := runAgentSubcommand([]string{
		"list",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
	}, &listAfterDelete, &listAfterDelete); err != nil {
		t.Fatalf("list after delete failed: %v", err)
	}
	if got := listAfterDelete.String(); !strings.Contains(got, "planner | deleted") || !strings.Contains(got, "builder | deleted") {
		t.Fatalf("expected deleted agents in list output, got: %s", got)
	}
}

func TestAgentCreateValidatesInputs(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := runAgentSubcommand([]string{
		"create",
		"--id", "bad/id",
		"--user-id", "tg:planner",
	}, &out, &out)
	if err == nil {
		t.Fatalf("expected invalid id error")
	}
}

func TestRunRoutesAgentSubcommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(dir, "agents", "index.json")

	var out bytes.Buffer
	if err := run([]string{"agent", "list", "--registry-path", registryPath}, &out, &out); err != nil {
		t.Fatalf("run(agent list) failed: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "no agents found") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestAgentCreateWritesAdaptedDefaultTemplates(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(dir, "agents", "index.json")
	workspaceRoot := filepath.Join(dir, "workspaces")

	var out bytes.Buffer
	if err := runAgentSubcommand([]string{
		"create",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
		"--id", "writer",
		"--user-id", "tg:writer",
	}, &out, &out); err != nil {
		t.Fatalf("create writer failed: %v", err)
	}

	workspace := filepath.Join(workspaceRoot, "writer")
	agentsBlob, err := os.ReadFile(filepath.Join(workspace, "AGENTS.md"))
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	agentsText := string(agentsBlob)
	if !strings.Contains(agentsText, "## Session Start (required)") {
		t.Fatalf("expected OpenClaw-style session start section in AGENTS.md")
	}
	if !strings.Contains(agentsText, "## Gopher Runtime Notes") {
		t.Fatalf("expected gopher runtime notes in AGENTS.md")
	}
	if !strings.Contains(agentsText, "Agent id: writer") {
		t.Fatalf("expected agent id in AGENTS.md")
	}

	configBlob, err := os.ReadFile(filepath.Join(workspace, "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	var config map[string]any
	if err := toml.Unmarshal(configBlob, &config); err != nil {
		t.Fatalf("config.toml should be valid TOML: %v", err)
	}
	if got, _ := config["agent_id"].(string); got != "writer" {
		t.Fatalf("config agent_id=%q, want writer", got)
	}
	if got, _ := config["model_policy"].(string); got != "openai:gpt-5.3-codex" {
		t.Fatalf("config model_policy=%q, want openai:gpt-5.3-codex", got)
	}
	if got, _ := config["reasoning_level"].(string); got != "medium" {
		t.Fatalf("config reasoning_level=%q, want medium", got)
	}

	policiesRaw, ok := config["policies"].(map[string]any)
	if !ok {
		t.Fatalf("expected [policies] table in config.toml")
	}
	if got, _ := policiesRaw["allow_cross_agent_fs"].(bool); !got {
		t.Fatalf("writer allow_cross_agent_fs=%t, want true", got)
	}
	if _, exists := policiesRaw["fs_roots"]; exists {
		t.Fatalf("did not expect explicit fs_roots default in config.toml")
	}

	sharedUserBlob, err := os.ReadFile(filepath.Join(workspaceRoot, "USER.md"))
	if err != nil {
		t.Fatalf("read shared USER.md: %v", err)
	}
	if !strings.Contains(string(sharedUserBlob), "Shared User Profile") {
		t.Fatalf("expected shared user template in workspace root USER.md")
	}
}

func TestAgentCreateMainDefaultsCrossAgentFSTrue(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	registryPath := filepath.Join(dir, "agents", "index.json")
	workspaceRoot := filepath.Join(dir, "workspaces")

	var out bytes.Buffer
	if err := runAgentSubcommand([]string{
		"create",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
		"--id", "main",
	}, &out, &out); err != nil {
		t.Fatalf("create main failed: %v", err)
	}

	configPath := filepath.Join(workspaceRoot, "main", "config.toml")
	configBlob, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read %s: %v", configPath, err)
	}
	var config map[string]any
	if err := toml.Unmarshal(configBlob, &config); err != nil {
		t.Fatalf("config.toml should be valid TOML: %v", err)
	}
	policiesRaw, ok := config["policies"].(map[string]any)
	if !ok {
		t.Fatalf("expected [policies] table in config.toml")
	}
	if got, _ := policiesRaw["allow_cross_agent_fs"].(bool); !got {
		t.Fatalf("main allow_cross_agent_fs=%t, want true", got)
	}
}
