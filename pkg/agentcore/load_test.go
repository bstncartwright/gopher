package agentcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestLoadAgentMissingRequiredFiles(t *testing.T) {
	required := []string{"AGENTS.md", "soul.md", "config.json", "policies.json"}
	for _, name := range required {
		t.Run(name, func(t *testing.T) {
			workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
			if err := os.Remove(filepath.Join(workspace, name)); err != nil {
				t.Fatalf("remove %s: %v", name, err)
			}

			_, err := LoadAgent(workspace)
			if err == nil {
				t.Fatalf("expected error for missing %s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("expected error to mention %s, got: %v", name, err)
			}
		})
	}
}

func TestLoadAgentInvalidModelPolicyFormat(t *testing.T) {
	config := defaultConfig()
	config.ModelPolicy = "gpt-4o-mini"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected invalid model_policy error")
	}
	if !strings.Contains(err.Error(), "provider:model") {
		t.Fatalf("expected provider:model guidance, got: %v", err)
	}
}

func TestLoadAgentModelPolicyAllowsColonInModelID(t *testing.T) {
	config := defaultConfig()
	config.ModelPolicy = "ollama:qwen3:0.6b"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	original := modelsToMap(ai.GetModels("ollama"))
	updated := modelsToMap(ai.GetModels("ollama"))
	updated["qwen3:0.6b"] = ai.Model{
		ID:            "qwen3:0.6b",
		Name:          "Qwen3 0.6B",
		API:           ai.APIOpenAICompletions,
		Provider:      ai.ProviderOllama,
		BaseURL:       "http://localhost:11434/v1",
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ai.ModelCost{},
		ContextWindow: 32768,
		MaxTokens:     8192,
	}
	ai.SetModels(ai.ProviderOllama, updated)
	defer ai.SetModels(ai.ProviderOllama, original)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.model.ID != "qwen3:0.6b" {
		t.Fatalf("expected qwen3 model, got %q", agent.model.ID)
	}
}

func modelsToMap(models []ai.Model) map[string]ai.Model {
	out := make(map[string]ai.Model, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out
}
