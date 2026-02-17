//go:build integration

package agentcore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestOllamaSmoke(t *testing.T) {
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

	t.Run("plain_text_reply", func(t *testing.T) {
		agent, workspace := setupOllamaAgent(t, modelID, nil, false)
		_ = workspace

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

		assertHasEvent(t, result.Events, EventTypeAgentMsg)
		if len(session.Messages) == 0 {
			t.Fatalf("expected session messages to be updated")
		}
	})

	t.Run("tool_read_file", func(t *testing.T) {
		agent, workspace := setupOllamaAgent(t, modelID, []string{"group:fs"}, false)
		mustWriteFile(t, filepath.Join(workspace, "secret.txt"), "the launch code is 8675309")

		session := agent.NewSession()
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		result, err := agent.RunTurn(ctx, session, TurnInput{
			UserMessage: "Read the file secret.txt and tell me the launch code. Reply with ONLY the number.",
		})
		if err != nil {
			t.Fatalf("RunTurn() error: %v", err)
		}

		assertHasEvent(t, result.Events, EventTypeToolCall)
		assertHasEvent(t, result.Events, EventTypeToolResult)

		if !strings.Contains(result.FinalText, "8675309") {
			t.Fatalf("expected final text to contain 8675309, got %q", result.FinalText)
		}
	})

	t.Run("tool_write_and_read", func(t *testing.T) {
		agent, workspace := setupOllamaAgent(t, modelID, []string{"group:fs"}, false)

		session := agent.NewSession()
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		result, err := agent.RunTurn(ctx, session, TurnInput{
			UserMessage: "Write the text 'hello from qwen3' to a file called greeting.txt, then read it back and confirm the contents.",
		})
		if err != nil {
			t.Fatalf("RunTurn() error: %v", err)
		}

		toolCallCount := countEvents(result.Events, EventTypeToolCall)
		if toolCallCount < 1 {
			t.Fatalf("expected at least 1 tool call, got %d", toolCallCount)
		}

		content, err := os.ReadFile(filepath.Join(workspace, "greeting.txt"))
		if err != nil {
			t.Fatalf("expected greeting.txt to exist: %v", err)
		}
		if !strings.Contains(string(content), "hello from qwen3") {
			t.Fatalf("expected file to contain 'hello from qwen3', got %q", string(content))
		}
	})

	t.Run("tool_exec", func(t *testing.T) {
		agent, _ := setupOllamaAgent(t, modelID, []string{"group:fs", "group:runtime"}, true)

		session := agent.NewSession()
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		result, err := agent.RunTurn(ctx, session, TurnInput{
			UserMessage: "Run the command 'echo smoke_test_pass' and tell me exactly what it printed.",
		})
		if err != nil {
			t.Fatalf("RunTurn() error: %v", err)
		}

		assertHasEvent(t, result.Events, EventTypeToolCall)
		if !strings.Contains(result.FinalText, "smoke_test_pass") {
			t.Fatalf("expected final text to contain smoke_test_pass, got %q", result.FinalText)
		}
	})
}

func setupOllamaAgent(t *testing.T, modelID string, enabledTools []string, canShell bool) (*Agent, string) {
	t.Helper()

	config := defaultConfig()
	config.ModelPolicy = "ollama:" + modelID
	config.EnabledTools = enabledTools

	policies := defaultPolicies()
	policies.CanShell = canShell
	if !canShell {
		policies.ShellAllowlist = nil
	}

	workspace := createTestWorkspace(t, config, policies)
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	return agent, workspace
}

func assertHasEvent(t *testing.T, events []Event, eventType EventType) {
	t.Helper()
	for _, e := range events {
		if e.Type == eventType {
			return
		}
	}
	t.Fatalf("expected at least one %s event", eventType)
}

func countEvents(events []Event, eventType EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == eventType {
			n++
		}
	}
	return n
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
			continue
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
