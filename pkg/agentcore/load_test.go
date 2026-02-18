package agentcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestLoadAgentMissingRequiredFiles(t *testing.T) {
	required := []string{"config.json", "policies.json"}
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

func TestLoadAgentRejectsFSRootsOutsideWorkspace(t *testing.T) {
	policies := defaultPolicies()
	policies.FSRoots = []string{"../outside"}
	workspace := createTestWorkspace(t, defaultConfig(), policies)

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected fs root escape error")
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected escape error, got: %v", err)
	}
}

func TestLoadAgentAllowsFSRootsOutsideWorkspaceWhenEnabled(t *testing.T) {
	otherWorkspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())

	policies := defaultPolicies()
	policies.FSRoots = []string{"./", otherWorkspace}
	policies.AllowCrossAgentFS = true
	workspace := createTestWorkspace(t, defaultConfig(), policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	otherWorkspace = evalSymlinksOrAncestor(filepath.Clean(otherWorkspace))
	found := false
	for _, root := range agent.allowedFSRoots {
		if root == otherWorkspace {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected cross-agent fs root %q in allowed roots: %#v", otherWorkspace, agent.allowedFSRoots)
	}
}

func TestLoadAgentHeartbeatDefaultsDisabled(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Heartbeat.Enabled {
		t.Fatalf("heartbeat should be disabled by default")
	}
}

func TestLoadAgentHeartbeatConfigParsesEveryPromptAndAckLimit(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every:       "10m",
		Prompt:      "check now",
		AckMaxChars: 144,
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !agent.Heartbeat.Enabled {
		t.Fatalf("heartbeat should be enabled")
	}
	if agent.Heartbeat.Every != 10*time.Minute {
		t.Fatalf("heartbeat every = %s, want 10m", agent.Heartbeat.Every)
	}
	if agent.Heartbeat.Prompt != "check now" {
		t.Fatalf("heartbeat prompt = %q, want check now", agent.Heartbeat.Prompt)
	}
	if agent.Heartbeat.AckMaxChars != 144 {
		t.Fatalf("heartbeat ack max = %d, want 144", agent.Heartbeat.AckMaxChars)
	}
}

func TestLoadAgentHeartbeatEveryBareNumberTreatsMinutes(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{Every: "15"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Heartbeat.Every != 15*time.Minute {
		t.Fatalf("heartbeat every = %s, want 15m", agent.Heartbeat.Every)
	}
}

func TestLoadAgentHeartbeatInvalidEveryReturnsError(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{Every: "not-a-duration"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected heartbeat duration error")
	}
	if !strings.Contains(err.Error(), "config.heartbeat.every") {
		t.Fatalf("expected heartbeat path in error, got: %v", err)
	}
}

func modelsToMap(models []ai.Model) map[string]ai.Model {
	out := make(map[string]ai.Model, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out
}
