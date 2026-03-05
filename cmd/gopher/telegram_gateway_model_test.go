package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestHandleTelegramModelPolicyCommandRejectsUnknownModelWithoutWritingConfig(t *testing.T) {
	workspace := t.TempDir()
	agentWorkspace := filepath.Join(workspace, "agents", "main")
	createGatewayTestAgentWorkspace(t, agentWorkspace, "main")

	configPath := filepath.Join(agentWorkspace, "config.json")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config before update: %v", err)
	}

	runtime := &gatewayAgentRuntime{
		DefaultActorID: "main",
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"main": {
				ID:        "main",
				Workspace: agentWorkspace,
				Config: agentcore.AgentConfig{
					ModelPolicy: "openai:gpt-4o-mini",
				},
			},
		},
	}

	_, err = handleTelegramModelPolicyCommand(
		context.Background(),
		workspace,
		runtime,
		gateway.ModelPolicyCommandRequest{
			AgentID:              "main",
			RequestedModelPolicy: "openai:not-a-real-model",
		},
		nil,
		log.New(io.Discard, "", 0),
	)
	if err == nil {
		t.Fatalf("expected unknown model error")
	}
	if !strings.Contains(err.Error(), `model not found for model_policy "openai:not-a-real-model"`) {
		t.Fatalf("unexpected error: %v", err)
	}

	after, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatalf("read config after failed update: %v", readErr)
	}
	if string(after) != string(before) {
		t.Fatalf("config changed after failed update")
	}
	if got := runtime.Agents["main"].Config.ModelPolicy; got != "openai:gpt-4o-mini" {
		t.Fatalf("runtime model_policy = %q, want unchanged", got)
	}
}

func TestHandleTelegramModelPolicyCommandUpdatesValidModelPolicy(t *testing.T) {
	workspace := t.TempDir()
	agentWorkspace := filepath.Join(workspace, "agents", "main")
	mustWriteFile(t, filepath.Join(agentWorkspace, "config.toml"), `agent_id = "main"
name = "main"
role = "assistant"
model_policy = "openai:gpt-4o-mini"
`)

	result, err := handleTelegramModelPolicyCommand(
		context.Background(),
		workspace,
		nil,
		gateway.ModelPolicyCommandRequest{
			AgentID:              "main",
			RequestedModelPolicy: "openai:gpt-4o",
		},
		func(context.Context) error { return nil },
		log.New(io.Discard, "", 0),
	)
	if err != nil {
		t.Fatalf("handleTelegramModelPolicyCommand() error: %v", err)
	}
	if !result.Updated {
		t.Fatalf("expected update to be marked changed")
	}
	if !result.RestartScheduled {
		t.Fatalf("expected restart to be scheduled")
	}
	if result.CurrentModelPolicy != "openai:gpt-4o" {
		t.Fatalf("current model_policy = %q, want openai:gpt-4o", result.CurrentModelPolicy)
	}

	current, err := readAgentModelPolicy(filepath.Join(agentWorkspace, "config.toml"))
	if err != nil {
		t.Fatalf("readAgentModelPolicy() error: %v", err)
	}
	if current != "openai:gpt-4o" {
		t.Fatalf("persisted model_policy = %q, want openai:gpt-4o", current)
	}
}
