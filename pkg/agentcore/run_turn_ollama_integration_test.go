//go:build integration

package agentcore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestRunTurnWithOllamaQwen3Provider(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping ollama integration test in short mode")
	}

	if _, err := exec.LookPath("ollama"); err != nil {
		t.Skip("ollama CLI not installed")
	}

	models, err := listOllamaModels()
	if err != nil {
		t.Skipf("unable to query ollama models: %v", err)
	}

	modelID := chooseSmallQwen3Model(models)
	if modelID == "" {
		t.Skip("no qwen3 model found locally in ollama (expected e.g. qwen3:0.6b or qwen3:1.7b)")
	}

	restoreModels := registerOllamaModelForTest(modelID)
	defer restoreModels()

	config := defaultConfig()
	config.ModelPolicy = "ollama:" + modelID
	config.EnabledTools = nil

	policies := defaultPolicies()
	policies.CanShell = false
	policies.ShellAllowlist = nil

	workspace := createTestWorkspace(t, config, policies)
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	session := agent.NewSession()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	result, err := agent.RunTurn(ctx, session, TurnInput{UserMessage: "Reply with exactly OK."})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}

	if strings.TrimSpace(result.FinalText) == "" {
		t.Fatalf("expected non-empty final text")
	}
	if !strings.Contains(strings.ToLower(result.FinalText), "ok") {
		t.Fatalf("expected response to contain OK, got %q", result.FinalText)
	}

	hasMessageEvent := false
	for _, event := range result.Events {
		if event.Type == EventTypeAgentMsg {
			hasMessageEvent = true
			break
		}
	}
	if !hasMessageEvent {
		t.Fatalf("expected at least one agent.message event")
	}

	if len(session.Messages) == 0 {
		t.Fatalf("expected session messages to be updated")
	}
}

func listOllamaModels() ([]string, error) {
	cmd := exec.Command("ollama", "list")
	if host := strings.TrimSpace(os.Getenv("OLLAMA_HOST")); host != "" {
		cmd.Env = append(os.Environ(), "OLLAMA_HOST="+host)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("ollama list failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) <= 1 {
		return nil, nil
	}

	models := make([]string, 0, len(lines)-1)
	for idx, line := range lines {
		if idx == 0 {
			continue // header
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		models = append(models, fields[0])
	}
	return models, nil
}

func chooseSmallQwen3Model(models []string) string {
	if len(models) == 0 {
		return ""
	}

	preferred := []string{"qwen3:0.6b", "qwen3:1.7b", "qwen3:4b", "qwen3:8b"}
	index := map[string]string{}
	for _, model := range models {
		index[strings.ToLower(model)] = model
	}
	for _, want := range preferred {
		if found, ok := index[strings.ToLower(want)]; ok {
			return found
		}
	}
	for _, model := range models {
		if strings.HasPrefix(strings.ToLower(model), "qwen3:") {
			return model
		}
	}
	return ""
}

func registerOllamaModelForTest(modelID string) func() {
	original := modelsToMap(ai.GetModels("ollama"))
	updated := modelsToMap(ai.GetModels("ollama"))
	if _, exists := updated[modelID]; !exists {
		updated[modelID] = ai.Model{
			ID:            modelID,
			Name:          modelID,
			API:           ai.APIOpenAICompletions,
			Provider:      ai.ProviderOllama,
			BaseURL:       "http://localhost:11434/v1",
			Reasoning:     true,
			Input:         []string{"text"},
			Cost:          ai.ModelCost{},
			ContextWindow: 65536,
			MaxTokens:     8192,
		}
	}
	ai.SetModels(ai.ProviderOllama, updated)
	return func() {
		ai.SetModels(ai.ProviderOllama, original)
	}
}

func modelsToMap(models []ai.Model) map[string]ai.Model {
	out := make(map[string]ai.Model, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out
}
