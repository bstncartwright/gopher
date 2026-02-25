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
		{Type: ai.ContentTypeToolCall, ID: "call_1", Name: "read", Arguments: map[string]any{"path": "a.txt"}},
		{Type: ai.ContentTypeToolCall, ID: "call_2", Name: "read", Arguments: map[string]any{"path": "b.txt"}},
	}

	roundTwoAssistant := ai.NewAssistantMessage(agent.model)
	roundTwoAssistant.StopReason = ai.StopReasonStop
	roundTwoAssistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}

	agent.Provider = &mockProvider{rounds: []mockRound{
		{
			assistant: roundOneAssistant,
			events: []ai.AssistantMessageEvent{
				{Type: ai.EventToolCallEnd, ToolCall: &ai.ContentBlock{Type: ai.ContentTypeToolCall, ID: "call_1", Name: "read", Arguments: map[string]any{"path": "a.txt"}}},
				{Type: ai.EventToolCallEnd, ToolCall: &ai.ContentBlock{Type: ai.ContentTypeToolCall, ID: "call_2", Name: "read", Arguments: map[string]any{"path": "b.txt"}}},
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

func TestRunTurnThinkingDeltaCaptureToggle(t *testing.T) {
	for _, tc := range []struct {
		name            string
		captureThinking bool
		expectThinking  bool
	}{
		{name: "disabled", captureThinking: false, expectThinking: false},
		{name: "enabled", captureThinking: true, expectThinking: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
			agent, err := LoadAgent(workspace)
			if err != nil {
				t.Fatalf("LoadAgent() error: %v", err)
			}
			agent.CaptureThinkingDeltas = tc.captureThinking

			assistant := ai.NewAssistantMessage(agent.model)
			assistant.StopReason = ai.StopReasonStop
			assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "ok"}}

			agent.Provider = &mockProvider{rounds: []mockRound{
				{
					assistant: assistant,
					events: []ai.AssistantMessageEvent{
						{Type: ai.EventThinkingDelta, Delta: "private chain"},
						{Type: ai.EventTextDelta, Delta: "ok"},
					},
				},
			}}

			result, err := agent.RunTurn(context.Background(), agent.NewSession(), TurnInput{UserMessage: "test"})
			if err != nil {
				t.Fatalf("RunTurn() error: %v", err)
			}

			foundThinking := false
			for _, event := range result.Events {
				if event.Type == EventTypeAgentThinkingDelta {
					foundThinking = true
					break
				}
			}
			if foundThinking != tc.expectThinking {
				t.Fatalf("thinking delta presence = %t, want %t", foundThinking, tc.expectThinking)
			}
		})
	}
}

func TestRunTurnRetriesOnceOnContextOverflow(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	overflow := ai.NewAssistantMessage(agent.model)
	overflow.StopReason = ai.StopReasonError
	overflow.ErrorMessage = "too many tokens for context window"
	overflow.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: ""}}

	success := ai.NewAssistantMessage(agent.model)
	success.StopReason = ai.StopReasonStop
	success.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "recovered"}}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{assistant: overflow},
			{assistant: success},
		},
	}

	session := agent.NewSession()
	session.Messages = []ai.Message{
		{Role: ai.RoleUser, Content: "message one", Timestamp: 1},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "message two"}}, Timestamp: 2},
		{Role: ai.RoleUser, Content: "message three", Timestamp: 3},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "message four"}}, Timestamp: 4},
	}

	result, err := agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "final ask"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if result.FinalText != "recovered" {
		t.Fatalf("final text = %q, want recovered", result.FinalText)
	}
	if provider, ok := agent.Provider.(*mockProvider); !ok || provider.calls != 2 {
		t.Fatalf("expected 2 provider calls (overflow + retry), got %#v", agent.Provider)
	}
	if session.LastContextDiagnostics.OverflowRetries != 1 {
		t.Fatalf("overflow retries = %d, want 1", session.LastContextDiagnostics.OverflowRetries)
	}
}
