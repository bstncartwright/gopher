package agentcore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type mockProvider struct {
	rounds   []mockRound
	calls    int
	contexts []ai.Context
}

type mockRound struct {
	assistant ai.AssistantMessage
	events    []ai.AssistantMessageEvent
}

func (m *mockProvider) Stream(_ ai.Model, conversation ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	idx := m.calls
	m.calls++
	m.contexts = append(m.contexts, conversation)

	stream := ai.CreateAssistantMessageEventStream()
	if idx >= len(m.rounds) {
		msg := ai.NewAssistantMessage(ai.Model{ID: "mock", API: ai.APIOpenAIResponses, Provider: ai.ProviderOpenAI})
		msg.StopReason = ai.StopReasonError
		msg.ErrorMessage = "unexpected provider call"
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: &msg})
			stream.End(&msg)
		}()
		return stream
	}

	round := m.rounds[idx]
	go func() {
		for _, event := range round.events {
			stream.Push(event)
		}
		msg := round.assistant
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: msg.StopReason, Message: &msg})
	}()
	return stream
}

func TestRunTurnToolLoopEmitsExpectedOrder(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"fs"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "a.txt"), "A")
	mustWriteFile(t, filepath.Join(workspace, "b.txt"), "B")

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	roundOneAssistant := ai.NewAssistantMessage(agent.model)
	roundOneAssistant.StopReason = ai.StopReasonToolUse
	roundOneAssistant.Content = []ai.ContentBlock{
		{Type: ai.ContentTypeToolCall, ID: "call_1", Name: "fs.read", Arguments: map[string]any{"path": "a.txt"}},
		{Type: ai.ContentTypeToolCall, ID: "call_2", Name: "fs.read", Arguments: map[string]any{"path": "b.txt"}},
	}

	roundTwoAssistant := ai.NewAssistantMessage(agent.model)
	roundTwoAssistant.StopReason = ai.StopReasonStop
	roundTwoAssistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}

	agent.Provider = &mockProvider{rounds: []mockRound{
		{
			assistant: roundOneAssistant,
			events: []ai.AssistantMessageEvent{
				{Type: ai.EventToolCallEnd, ToolCall: &ai.ContentBlock{Type: ai.ContentTypeToolCall, ID: "call_1", Name: "fs.read", Arguments: map[string]any{"path": "a.txt"}}},
				{Type: ai.EventToolCallEnd, ToolCall: &ai.ContentBlock{Type: ai.ContentTypeToolCall, ID: "call_2", Name: "fs.read", Arguments: map[string]any{"path": "b.txt"}}},
			},
		},
		{
			assistant: roundTwoAssistant,
			events: []ai.AssistantMessageEvent{
				{Type: ai.EventTextDelta, Delta: "do"},
				{Type: ai.EventTextDelta, Delta: "ne"},
			},
		},
	}}

	session := agent.NewSession()
	result, err := agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "read files"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if result.FinalText != "done" {
		t.Fatalf("expected final text done, got %q", result.FinalText)
	}

	toolCallCount := 0
	toolResultCount := 0
	agentMessageIndex := -1
	for idx, event := range result.Events {
		switch event.Type {
		case EventTypeToolCall:
			toolCallCount++
		case EventTypeToolResult:
			toolResultCount++
		case EventTypeAgentMsg:
			agentMessageIndex = idx
		}
	}
	if toolCallCount != 2 {
		t.Fatalf("expected 2 tool.call events, got %d", toolCallCount)
	}
	if toolResultCount != 2 {
		t.Fatalf("expected 2 tool.result events, got %d", toolResultCount)
	}
	if agentMessageIndex == -1 {
		t.Fatalf("expected final agent.message event")
	}

	for idx := 0; idx < agentMessageIndex; idx++ {
		if result.Events[idx].Type == EventTypeAgentMsg {
			t.Fatalf("agent.message should be the final message event")
		}
	}

	if len(session.Messages) == 0 {
		t.Fatalf("expected session history to be updated")
	}
}
