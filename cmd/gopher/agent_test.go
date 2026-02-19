package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		"--matrix-user", "@planner:example.com",
	}, &out, &out); err != nil {
		t.Fatalf("create planner failed: %v", err)
	}

	plannerWorkspace := filepath.Join(workspaceRoot, "planner")
	for _, name := range []string{
		"AGENTS.md", "SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md", "BOOTSTRAP.md", "config.json", "policies.json",
	} {
		if _, err := os.Stat(filepath.Join(plannerWorkspace, name)); err != nil {
			t.Fatalf("expected workspace file %s: %v", name, err)
		}
	}

	var listOut bytes.Buffer
	if err := runAgentSubcommand([]string{
		"list",
		"--registry-path", registryPath,
		"--workspace-root", workspaceRoot,
	}, &listOut, &listOut); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if got := listOut.String(); !strings.Contains(got, "planner | active | @planner:example.com") {
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
		"--matrix-user", "@builder:example.com",
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
		"--matrix-user", "@planner:example.com",
	}, &out, &out)
	if err == nil {
		t.Fatalf("expected invalid id error")
	}

	err = runAgentSubcommand([]string{
		"create",
		"--id", "planner",
		"--matrix-user", "planner:example.com",
	}, &out, &out)
	if err == nil {
		t.Fatalf("expected invalid matrix user id error")
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
		"--matrix-user", "@writer:example.com",
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

	configBlob, err := os.ReadFile(filepath.Join(workspace, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(configBlob, &config); err != nil {
		t.Fatalf("config.json should be valid JSON: %v", err)
	}
	if got, _ := config["agent_id"].(string); got != "writer" {
		t.Fatalf("config agent_id=%q, want writer", got)
	}
	if got, _ := config["model_policy"].(string); got != "zai:glm-5" {
		t.Fatalf("config model_policy=%q, want zai:glm-5", got)
	}
}
