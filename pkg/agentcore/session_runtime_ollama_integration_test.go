//go:build integration

package agentcore

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestSessionRuntimeOllamaQwen3MultiToolFlow(t *testing.T) {
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
	t.Logf("model=%s", modelID)

	restoreModels := registerOllamaModelForTest(modelID)
	defer restoreModels()

	agent, workspace := setupOllamaSessionRuntimeAgent(t, modelID)

	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: newRunTurnSessionAdapter(agent),
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: sessionrt.ActorID(agent.ID), Type: sessionrt.ActorAgent},
			{ID: "user:me", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	t.Logf("session_id=%s workspace=%s", created.ID, workspace)

	steps := []struct {
		name     string
		prompt   string
		wantTool string
		optional bool
	}{
		{
			name: "write",
			prompt: strings.Join([]string{
				"Use ONLY the write tool.",
				"Create multi_tool.txt with exactly one line: alpha",
				"After the tool call, reply with WRITE_DONE.",
			}, "\n"),
			wantTool: "write",
		},
		{
			name: "edit",
			prompt: strings.Join([]string{
				"Use ONLY the edit tool.",
				"Replace alpha with alpha edited in multi_tool.txt.",
				"After the tool call, reply with EDIT_DONE.",
			}, "\n"),
			wantTool: "edit",
		},
		{
			name: "read",
			prompt: strings.Join([]string{
				"Use the read tool on multi_tool.txt.",
				"After the tool call, reply with READ_DONE and include the file content.",
			}, "\n"),
			wantTool: "read",
		},
		{
			name: "exec",
			prompt: strings.Join([]string{
				"Use the exec tool with command: bash -lc 'cat multi_tool.txt'.",
				"After the tool call, reply with EXEC_DONE and include command output.",
			}, "\n"),
			wantTool: "exec",
		},
	}

	for _, step := range steps {
		t.Logf("step=%s start", step.name)
		beforeEvents, err := store.List(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("List(before %s) error: %v", step.name, err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		err = manager.SendEvent(ctx, sessionrt.Event{
			SessionID: created.ID,
			From:      "user:me",
			Type:      sessionrt.EventMessage,
			Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: step.prompt},
		})
		cancel()
		if err != nil {
			t.Fatalf("SendEvent(%s) error: %v", step.name, err)
		}

		afterEvents, err := store.List(context.Background(), created.ID)
		if err != nil {
			t.Fatalf("List(after %s) error: %v", step.name, err)
		}
		if len(afterEvents) <= len(beforeEvents) {
			t.Fatalf("expected session to grow after step %s", step.name)
		}

		newEvents := afterEvents[len(beforeEvents):]
		t.Logf("step=%s new_events=%d", step.name, len(newEvents))
		for _, event := range newEvents {
			t.Logf("event seq=%d type=%s from=%s %s", event.Seq, event.Type, event.From, summarizeSessionEvent(event))
		}
		if !eventsContainToolCall(newEvents, step.wantTool) && !step.optional {
			t.Fatalf("expected tool %q call during step %s", step.wantTool, step.name)
		}
		t.Logf("step=%s complete", step.name)
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List(final) error: %v", err)
	}
	toolCalls := collectToolCalls(events)
	if len(toolCalls) < 4 {
		t.Fatalf("expected at least 4 distinct tools, got %d (%v)", len(toolCalls), toolCalls)
	}
	for _, required := range []string{"write", "edit", "read", "exec"} {
		if toolCalls[required] == 0 {
			t.Fatalf("expected required tool call %q, got %v", required, toolCalls)
		}
	}
	t.Logf("tool_calls=%s", formatToolCalls(toolCalls))

	fileData, err := os.ReadFile(filepath.Join(workspace, "multi_tool.txt"))
	if err != nil {
		t.Fatalf("expected multi_tool.txt to exist: %v", err)
	}
	text := string(fileData)
	if !strings.Contains(text, "alpha edited") {
		t.Fatalf("expected file to contain edited content, got %q", text)
	}
	if toolCalls["apply_patch"] > 0 && !strings.Contains(text, "patched line") {
		t.Fatalf("expected file to contain patched line after apply_patch call, got %q", text)
	}
	t.Logf("final_file_contents=%q", text)
}

func setupOllamaSessionRuntimeAgent(t *testing.T, modelID string) (*Agent, string) {
	t.Helper()

	config := defaultConfig()
	config.ModelPolicy = "ollama:" + modelID
	config.EnabledTools = []string{"group:fs", "group:runtime"}

	policies := defaultPolicies()
	policies.CanShell = true
	policies.ApplyPatchEnabled = true

	workspace := createTestWorkspace(t, config, policies)
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	return agent, workspace
}

func eventsContainToolCall(events []sessionrt.Event, toolName string) bool {
	for _, event := range events {
		if event.Type != sessionrt.EventToolCall {
			continue
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			continue
		}
		name, _ := payload["name"].(string)
		if name == toolName {
			return true
		}
	}
	return false
}

func collectToolCalls(events []sessionrt.Event) map[string]int {
	out := map[string]int{}
	for _, event := range events {
		if event.Type != sessionrt.EventToolCall {
			continue
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			continue
		}
		name, _ := payload["name"].(string)
		if name != "" {
			out[name]++
		}
	}
	return out
}

func summarizeSessionEvent(event sessionrt.Event) string {
	switch event.Type {
	case sessionrt.EventMessage:
		msg, ok := event.Payload.(sessionrt.Message)
		if !ok {
			return ""
		}
		return fmt.Sprintf("role=%s content=%q", msg.Role, truncateText(msg.Content, 120))
	case sessionrt.EventToolCall:
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			return ""
		}
		name, _ := payload["name"].(string)
		return fmt.Sprintf("tool=%s", name)
	case sessionrt.EventToolResult:
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			return ""
		}
		name, _ := payload["name"].(string)
		status, _ := payload["status"].(string)
		return fmt.Sprintf("tool=%s status=%s", name, status)
	case sessionrt.EventError:
		payload, ok := event.Payload.(sessionrt.ErrorPayload)
		if !ok {
			return ""
		}
		return fmt.Sprintf("error=%q", truncateText(payload.Message, 120))
	default:
		return ""
	}
}

func formatToolCalls(toolCalls map[string]int) string {
	keys := make([]string, 0, len(toolCalls))
	for key := range toolCalls {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, toolCalls[key]))
	}
	return strings.Join(parts, ", ")
}

func truncateText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
