package agentcore

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestContextBuilderOrderingTruncationAndDeterminism(t *testing.T) {
	config := defaultConfig()
	config.MaxContextMessages = 2
	workspace := createTestWorkspace(t, config, defaultPolicies())
	mustWriteFile(t, workspace+"/memory/working.json", `{"b":2,"a":1}`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	session := &Session{
		ID: "s-test",
		Messages: []Message{
			{Role: ai.RoleUser, Content: "u1", Timestamp: 1},
			{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "a1"}}, Timestamp: 2},
			{Role: ai.RoleUser, Content: "u2", Timestamp: 3},
		},
		WorkingState: map[string]any{},
	}

	ctxA, err := agent.buildProviderContext(context.Background(), session, "new")
	if err != nil {
		t.Fatalf("buildProviderContext() error: %v", err)
	}
	ctxB, err := agent.buildProviderContext(context.Background(), session, "new")
	if err != nil {
		t.Fatalf("buildProviderContext() second error: %v", err)
	}

	if !reflect.DeepEqual(ctxA, ctxB) {
		t.Fatalf("expected deterministic context output")
	}

	agentsIndex := strings.Index(ctxA.SystemPrompt, "### AGENTS.md")
	soulIndex := strings.Index(ctxA.SystemPrompt, "### soul.md")
	memoryIndex := strings.Index(ctxA.SystemPrompt, "### working memory")
	if !(agentsIndex >= 0 && soulIndex > agentsIndex && memoryIndex > soulIndex) {
		t.Fatalf("system prompt sections out of order: %s", ctxA.SystemPrompt)
	}

	if len(ctxA.Messages) != 3 {
		t.Fatalf("expected 3 messages (2 bounded + 1 new), got %d", len(ctxA.Messages))
	}
	if ctxA.Messages[0].Role != ai.RoleAssistant || ctxA.Messages[1].Role != ai.RoleUser || ctxA.Messages[2].Role != ai.RoleUser {
		t.Fatalf("unexpected role order: %#v", []ai.MessageRole{ctxA.Messages[0].Role, ctxA.Messages[1].Role, ctxA.Messages[2].Role})
	}
}
