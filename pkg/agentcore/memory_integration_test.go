package agentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/memory"
)

func TestContextBuilderInjectsRetrievedMemory(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.LongTermMemory == nil {
		t.Fatalf("expected long-term memory manager")
	}

	err = agent.LongTermMemory.Store(context.Background(), memory.MemoryRecord{
		Type:       memory.MemorySemantic,
		Scope:      memory.AgentScope(agent.ID),
		SessionID:  "s-history",
		AgentID:    agent.ID,
		Content:    "Deployments in this repo should run migration checks first.",
		Metadata:   map[string]string{"kind": "policy"},
		Importance: 0.9,
	})
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}

	session := agent.NewSession()
	ctx, err := agent.buildProviderContext(context.Background(), session, "help me deploy this service")
	if err != nil {
		t.Fatalf("buildProviderContext() error: %v", err)
	}
	if !strings.Contains(ctx.SystemPrompt, "### retrieved memory") {
		t.Fatalf("expected retrieved memory section in prompt: %s", ctx.SystemPrompt)
	}
	if !strings.Contains(strings.ToLower(ctx.SystemPrompt), "migration checks") {
		t.Fatalf("expected retrieved memory content in prompt")
	}
}

func TestRunTurnPersistsLongTermMemory(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.LongTermMemory == nil {
		t.Fatalf("expected long-term memory manager")
	}

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "I will keep lint strict."}}
	agent.Provider = &mockProvider{rounds: []mockRound{{assistant: assistant}}}

	session := agent.NewSession()
	_, err = agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "remember: keep lint strict"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}

	records, err := agent.LongTermMemory.Retrieve(context.Background(), memory.MemoryQuery{
		SessionID: session.ID,
		AgentID:   agent.ID,
		Topic:     "lint",
		Limit:     20,
		Scopes:    []memory.MemoryScope{memory.AgentScope(agent.ID), memory.ScopeGlobal},
	})
	if err != nil {
		t.Fatalf("Retrieve() error: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("expected persisted memory records")
	}

	hasSemanticOrEpisodic := false
	for _, record := range records {
		if record.Type == memory.MemorySemantic || record.Type == memory.MemoryEpisodic {
			hasSemanticOrEpisodic = true
			break
		}
	}
	if !hasSemanticOrEpisodic {
		t.Fatalf("expected semantic or episodic memory in retrieval result")
	}
}
