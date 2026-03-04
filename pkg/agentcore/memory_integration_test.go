package agentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/memory"
)

func TestContextBuilderDoesNotInjectRetrievedMemoryInFullMode(t *testing.T) {
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
	if strings.Contains(ctx.SystemPrompt, "### retrieved memory") {
		t.Fatalf("did not expect retrieved memory section in prompt: %s", ctx.SystemPrompt)
	}
	if strings.Contains(strings.ToLower(ctx.SystemPrompt), "migration checks") {
		t.Fatalf("did not expect retrieved memory content in prompt")
	}
}

func TestContextBuilderMinimalModeSkipsRetrievedMemory(t *testing.T) {
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
		Content:    "Remember to run migration checks first.",
		Metadata:   map[string]string{"kind": "policy"},
		Importance: 0.9,
	})
	if err != nil {
		t.Fatalf("Store() error: %v", err)
	}

	session := agent.NewSession()
	ctx, err := agent.buildProviderContext(context.Background(), session, "help me deploy this service", PromptModeMinimal)
	if err != nil {
		t.Fatalf("buildProviderContext() error: %v", err)
	}
	if strings.Contains(ctx.SystemPrompt, "### retrieved memory") {
		t.Fatalf("did not expect retrieved memory section in minimal mode: %s", ctx.SystemPrompt)
	}
	if strings.Contains(strings.ToLower(ctx.SystemPrompt), "migration checks") {
		t.Fatalf("did not expect retrieved memory content in minimal mode")
	}
}

func TestMemorySearchToolRetrievesFromMemoryFiles(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.MemoryFiles == nil {
		t.Fatalf("expected memory files manager")
	}

	const marker = "zxqv-memory-probe-marker"
	if _, err := agent.MemoryFiles.AppendDailyEntry("Remember " + marker + " for deploy checks."); err != nil {
		t.Fatalf("AppendDailyEntry() error: %v", err)
	}

	tool := &memorySearchTool{}
	session := agent.NewSession()
	output, err := tool.Run(context.Background(), ToolInput{
		Agent:   agent,
		Session: session,
		Args: map[string]any{
			"query":       marker,
			"max_results": 5,
		},
	})
	if err != nil {
		t.Fatalf("memorySearchTool.Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("memorySearchTool status = %q, want ok", output.Status)
	}
	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("tool result type = %T, want map[string]any", output.Result)
	}
	rawResults, ok := result["results"]
	if !ok {
		t.Fatalf("expected results in tool output")
	}
	found := false
	switch typed := rawResults.(type) {
	case []map[string]any:
		for _, item := range typed {
			snippet, _ := item["snippet"].(string)
			if strings.Contains(strings.ToLower(snippet), marker) {
				found = true
				break
			}
		}
	case []any:
		for _, entry := range typed {
			item, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			snippet, _ := item["snippet"].(string)
			if strings.Contains(strings.ToLower(snippet), marker) {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected memory_search results to include marker %q, got: %#v", marker, rawResults)
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
