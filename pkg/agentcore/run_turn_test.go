package agentcore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type mockProvider struct {
	rounds   []mockRound
	calls    int
	contexts []ai.Context
	options  []ai.SimpleStreamOptions
}

type countingSessionFlusher struct {
	calls int
}

func (f *countingSessionFlusher) FlushSession(_ context.Context, _ string) error {
	f.calls++
	return nil
}

type summaryFallbackProvider struct {
	mainRounds []mockRound
	mainCalls  int
}

func (p *summaryFallbackProvider) Stream(_ ai.Model, conversation ai.Context, _ *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.CreateAssistantMessageEventStream()
	if strings.Contains(strings.ToLower(conversation.SystemPrompt), "compressing prior conversation history") {
		msg := ai.NewAssistantMessage(ai.Model{ID: "mock", API: ai.APIOpenAIResponses, Provider: ai.ProviderOpenAI})
		msg.StopReason = ai.StopReasonError
		msg.ErrorMessage = "summary model unavailable"
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: &msg})
			stream.End(&msg)
		}()
		return stream
	}

	idx := p.mainCalls
	p.mainCalls++
	if idx >= len(p.mainRounds) {
		msg := ai.NewAssistantMessage(ai.Model{ID: "mock", API: ai.APIOpenAIResponses, Provider: ai.ProviderOpenAI})
		msg.StopReason = ai.StopReasonError
		msg.ErrorMessage = "unexpected provider call"
		go func() {
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopReasonError, Error: &msg})
			stream.End(&msg)
		}()
		return stream
	}
	round := p.mainRounds[idx]
	go func() {
		msg := round.assistant
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: msg.StopReason, Message: &msg})
	}()
	return stream
}

type mockRound struct {
	assistant ai.AssistantMessage
	events    []ai.AssistantMessageEvent
}

type blockingTool struct {
	started chan string
	release <-chan struct{}
}

func (t *blockingTool) Name() string { return "blocking" }

func (t *blockingTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        "blocking",
		Description: "blocks until released",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{"type": "string"},
			},
			"required":             []string{"id"},
			"additionalProperties": false,
		},
	}
}

func (t *blockingTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	id, _ := input.Args["id"].(string)
	select {
	case t.started <- id:
	case <-ctx.Done():
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": ctx.Err().Error()}}, ctx.Err()
	}
	select {
	case <-t.release:
	case <-ctx.Done():
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": ctx.Err().Error()}}, ctx.Err()
	}
	return ToolOutput{Status: ToolStatusOK, Result: map[string]any{"id": id}}, nil
}

func (m *mockProvider) Stream(_ ai.Model, conversation ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	idx := m.calls
	m.calls++
	m.contexts = append(m.contexts, conversation)
	if options != nil {
		m.options = append(m.options, *options)
	} else {
		m.options = append(m.options, ai.SimpleStreamOptions{})
	}

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

func TestRunTurnUsesConfiguredReasoningLevel(t *testing.T) {
	config := defaultConfig()
	config.ReasoningLevel = "medium"
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}

	provider := &mockProvider{
		rounds: []mockRound{{assistant: assistant}},
	}
	agent.Provider = provider

	if _, err := agent.RunTurn(context.Background(), agent.NewSession(), TurnInput{UserMessage: "test"}); err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}

	if len(provider.options) == 0 {
		t.Fatalf("expected at least one provider call option")
	}
	if got := provider.options[0].Reasoning; got != ai.ThinkingMedium {
		t.Fatalf("provider reasoning=%q, want %q", got, ai.ThinkingMedium)
	}
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

func TestRunTurnExecutesMultipleToolCallsInParallel(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	release := make(chan struct{})
	started := make(chan string, 2)
	agent.Tools = NewToolRegistry([]Tool{&blockingTool{started: started, release: release}})

	roundOneAssistant := ai.NewAssistantMessage(agent.model)
	roundOneAssistant.StopReason = ai.StopReasonToolUse
	roundOneAssistant.Content = []ai.ContentBlock{
		{Type: ai.ContentTypeToolCall, ID: "call_1", Name: "blocking", Arguments: map[string]any{"id": "one"}},
		{Type: ai.ContentTypeToolCall, ID: "call_2", Name: "blocking", Arguments: map[string]any{"id": "two"}},
	}
	roundTwoAssistant := ai.NewAssistantMessage(agent.model)
	roundTwoAssistant.StopReason = ai.StopReasonStop
	roundTwoAssistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}

	agent.Provider = &mockProvider{rounds: []mockRound{
		{assistant: roundOneAssistant},
		{assistant: roundTwoAssistant},
	}}

	done := make(chan error, 1)
	var turnResult TurnResult
	go func() {
		var runErr error
		turnResult, runErr = agent.RunTurn(context.Background(), agent.NewSession(), TurnInput{UserMessage: "test parallel"})
		done <- runErr
	}()

	seen := map[string]bool{}
	timeout := time.After(300 * time.Millisecond)
	for len(seen) < 2 {
		select {
		case id := <-started:
			seen[id] = true
		case <-timeout:
			t.Fatalf("expected both tool calls to start before release; started=%v", seen)
		}
	}
	close(release)

	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("RunTurn() error: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("RunTurn() did not complete after releasing tools")
	}

	if turnResult.FinalText != "done" {
		t.Fatalf("final text = %q, want done", turnResult.FinalText)
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
	disableModelSummary := false
	agent.Config.ContextManagement.ModelCompactionSummary = &disableModelSummary

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

func TestRunTurnRetriesUpToThreeTimesAndFlushesOnce(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	disableModelSummary := false
	agent.Config.ContextManagement.ModelCompactionSummary = &disableModelSummary

	overflow := ai.NewAssistantMessage(agent.model)
	overflow.StopReason = ai.StopReasonError
	overflow.ErrorMessage = "context_length_exceeded"
	overflow.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: ""}}
	success := ai.NewAssistantMessage(agent.model)
	success.StopReason = ai.StopReasonStop
	success.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "recovered after third retry"}}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{assistant: overflow},
			{assistant: overflow},
			{assistant: overflow},
			{assistant: success},
		},
	}
	flusher := &countingSessionFlusher{}
	agent.SessionMemoryFlusher = flusher

	session := agent.NewSession()
	for i := 0; i < 12; i++ {
		role := ai.RoleUser
		if i%2 == 1 {
			role = ai.RoleAssistant
		}
		if role == ai.RoleAssistant {
			session.Messages = append(session.Messages, ai.Message{
				Role:      role,
				Content:   []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "assistant message"}},
				Timestamp: int64(i + 1),
			})
			continue
		}
		session.Messages = append(session.Messages, ai.Message{
			Role:      role,
			Content:   "user message",
			Timestamp: int64(i + 1),
		})
	}

	result, err := agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "retry hard"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if result.FinalText != "recovered after third retry" {
		t.Fatalf("final text = %q", result.FinalText)
	}
	if provider, ok := agent.Provider.(*mockProvider); !ok || provider.calls != 4 {
		t.Fatalf("expected 4 provider calls (3 overflows + success), got %#v", agent.Provider)
	}
	if flusher.calls != 1 {
		t.Fatalf("expected exactly one memory flush before retries, got %d", flusher.calls)
	}
	if session.LastContextDiagnostics.OverflowRetries != 3 {
		t.Fatalf("overflow retries = %d, want 3", session.LastContextDiagnostics.OverflowRetries)
	}
	if session.LastContextDiagnostics.OverflowStage != "retry_3" {
		t.Fatalf("overflow stage = %q, want retry_3", session.LastContextDiagnostics.OverflowStage)
	}
}

func TestRunTurnCompactionSummaryFallsBackWhenModelSummaryFails(t *testing.T) {
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
	success.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "fallback worked"}}

	agent.Provider = &summaryFallbackProvider{
		mainRounds: []mockRound{
			{assistant: overflow},
			{assistant: success},
		},
	}

	session := agent.NewSession()
	for i := 0; i < 8; i++ {
		session.Messages = append(session.Messages, ai.Message{
			Role:      ai.RoleUser,
			Content:   "message for compaction",
			Timestamp: int64(i + 1),
		})
	}
	result, err := agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "trigger overflow"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if result.FinalText != "fallback worked" {
		t.Fatalf("final text = %q, want fallback worked", result.FinalText)
	}
	if session.LastContextDiagnostics.SummaryStrategy != "deterministic_fallback" {
		t.Fatalf("summary strategy = %q, want deterministic_fallback", session.LastContextDiagnostics.SummaryStrategy)
	}
	if len(session.LastContextDiagnostics.Warnings) == 0 {
		t.Fatalf("expected warning indicating model summary fallback")
	}
}

func TestRunTurnCapsToolResultContextPayloadButKeepsEmittedResult(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	largeContent := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 600)
	mustWriteFile(t, filepath.Join(workspace, "big.txt"), largeContent)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	roundOne := ai.NewAssistantMessage(agent.model)
	roundOne.StopReason = ai.StopReasonToolUse
	roundOne.Content = []ai.ContentBlock{
		{Type: ai.ContentTypeToolCall, ID: "read_big", Name: "read", Arguments: map[string]any{"path": "big.txt"}},
	}
	roundTwo := ai.NewAssistantMessage(agent.model)
	roundTwo.StopReason = ai.StopReasonStop
	roundTwo.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{assistant: roundOne},
			{assistant: roundTwo},
		},
	}

	session := agent.NewSession()
	result, err := agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "read big file"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if result.FinalText != "done" {
		t.Fatalf("final text = %q, want done", result.FinalText)
	}

	var emittedToolResultContentLen int
	for _, event := range result.Events {
		if event.Type != EventTypeToolResult {
			continue
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			continue
		}
		resultMap, ok := payload["result"].(map[string]any)
		if !ok {
			continue
		}
		content, _ := resultMap["content"].(string)
		emittedToolResultContentLen = len(content)
	}
	if emittedToolResultContentLen <= agent.Config.ContextManagement.ToolResultContextMaxCharsValue() {
		t.Fatalf("expected emitted tool.result payload to keep full content, len=%d", emittedToolResultContentLen)
	}

	foundContextTruncated := false
	for _, msg := range session.Messages {
		if msg.Role != ai.RoleToolResult {
			continue
		}
		blocks, ok := msg.ContentBlocks()
		if !ok || len(blocks) == 0 {
			continue
		}
		if len(blocks[0].Text) > agent.Config.ContextManagement.ToolResultContextMaxCharsValue()+200 {
			t.Fatalf("expected context toolResult text to be capped, len=%d", len(blocks[0].Text))
		}
		if strings.Contains(blocks[0].Text, "\"truncated\": true") {
			foundContextTruncated = true
		}
	}
	if !foundContextTruncated {
		t.Fatalf("expected context payload truncation envelope in toolResult message")
	}
}
