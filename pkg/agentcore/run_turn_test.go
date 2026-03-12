package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestRunTurnForwardsProviderOptions(t *testing.T) {
	config := defaultConfig()
	config.ProviderOptions = map[string]any{"service_tier": "fast"}
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
	if got := provider.options[0].ProviderOptions["service_tier"]; got != "fast" {
		t.Fatalf("provider options service_tier=%#v, want %q", got, "fast")
	}
}

func TestRunTurnReturnsErrorOnProviderErrorStopReason(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonError
	assistant.ErrorMessage = "No tool call found for function call output with call_id call_abc."

	agent.Provider = &mockProvider{
		rounds: []mockRound{{assistant: assistant}},
	}

	result, runErr := agent.RunTurn(context.Background(), agent.NewSession(), TurnInput{UserMessage: "trigger error"})
	if runErr == nil {
		t.Fatalf("expected RunTurn() to return an error")
	}
	if !strings.Contains(runErr.Error(), "No tool call found") {
		t.Fatalf("expected provider error to be surfaced, got %v", runErr)
	}
	for _, event := range result.Events {
		if event.Type == EventTypeAgentMsg {
			t.Fatalf("did not expect final agent.message on provider error")
		}
	}
	hasErrorEvent := false
	for _, event := range result.Events {
		if event.Type == EventTypeError {
			hasErrorEvent = true
			break
		}
	}
	if !hasErrorEvent {
		t.Fatalf("expected error event to be emitted")
	}
}

func TestRunTurnUsesStreamErrorWhenProviderErrorMessageEmpty(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	streamErr := ai.NewAssistantMessage(agent.model)
	streamErr.StopReason = ai.StopReasonError
	streamErr.ErrorMessage = "No tool call found for function call output with call_id call_stream."

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonError
	assistant.ErrorMessage = ""

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{
				assistant: assistant,
				events: []ai.AssistantMessageEvent{
					{Type: ai.EventError, Reason: ai.StopReasonError, Error: &streamErr},
				},
			},
		},
	}

	_, runErr := agent.RunTurn(context.Background(), agent.NewSession(), TurnInput{UserMessage: "trigger stream error"})
	if runErr == nil {
		t.Fatalf("expected RunTurn() to return an error")
	}
	if !strings.Contains(runErr.Error(), "No tool call found") {
		t.Fatalf("expected stream error fallback message, got %v", runErr)
	}
}

func TestRunTurnTracesHostedWebSearchWithoutLocalToolExecution(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	capture := &aliasCaptureTool{name: "web_search"}
	agent.Tools = NewToolRegistry([]Tool{capture})

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}
	agent.Provider = &mockProvider{
		rounds: []mockRound{{
			assistant: assistant,
			events: []ai.AssistantMessageEvent{
				{Type: ai.EventWebSearchStart, WebSearch: &ai.HostedWebSearchCall{ID: "ws_1", Query: "weather denver", Status: "in_progress"}},
				{Type: ai.EventWebSearchEnd, WebSearch: &ai.HostedWebSearchCall{ID: "ws_1", Query: "weather denver", Status: "completed"}},
			},
		}},
	}

	session := agent.NewSession()
	result, err := agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "check weather"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if capture.ran {
		t.Fatalf("expected hosted web_search to avoid local tool execution")
	}
	if result.FinalText != "done" {
		t.Fatalf("final text = %q, want done", result.FinalText)
	}
	if countEvents(result.Events, EventTypeToolCall) != 1 {
		t.Fatalf("tool call events = %d, want 1", countEvents(result.Events, EventTypeToolCall))
	}
	if countEvents(result.Events, EventTypeToolResult) != 1 {
		t.Fatalf("tool result events = %d, want 1", countEvents(result.Events, EventTypeToolResult))
	}
	callPayload := eventPayloadForType(result.Events, EventTypeToolCall)
	if got := strings.TrimSpace(fmt.Sprint(callPayload["backend"])); got != "provider_native" {
		t.Fatalf("tool.call backend = %q, want provider_native", got)
	}
	for _, msg := range session.Messages {
		if msg.Role == ai.RoleToolResult {
			t.Fatalf("did not expect provider-native web_search to persist toolResult context message")
		}
	}
}

func TestRunTurnRetriesHostedWebSearchWithMCPFallback(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	capture := &aliasCaptureTool{name: "web_search"}
	agent.Tools = NewToolRegistry([]Tool{capture})

	first := ai.NewAssistantMessage(agent.model)
	first.StopReason = ai.StopReasonError
	first.ErrorMessage = "web_search is not supported for this model"
	second := ai.NewAssistantMessage(agent.model)
	second.StopReason = ai.StopReasonStop
	second.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "fallback ok"}}
	provider := &mockProvider{
		rounds: []mockRound{
			{assistant: first},
			{assistant: second},
		},
	}
	agent.Provider = provider

	result, err := agent.RunTurn(context.Background(), agent.NewSession(), TurnInput{UserMessage: "search this"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if result.FinalText != "fallback ok" {
		t.Fatalf("final text = %q, want fallback ok", result.FinalText)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want 2", provider.calls)
	}
	if len(provider.contexts) != 2 {
		t.Fatalf("provider contexts = %d, want 2", len(provider.contexts))
	}
	if hostedWebSearchMode(provider.contexts[0].Tools) == NativeWebSearchModeDisabled {
		t.Fatalf("expected first request to use hosted web_search")
	}
	if hostedWebSearchMode(provider.contexts[1].Tools) != NativeWebSearchModeDisabled {
		t.Fatalf("expected retry request to disable hosted web_search")
	}
}

func countEvents(events []Event, eventType EventType) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func eventPayloadForType(events []Event, eventType EventType) map[string]any {
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		if payload, ok := event.Payload.(map[string]any); ok {
			return payload
		}
	}
	return nil
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

func TestRunTurnEmitsCommentaryMessageBeforeToolCalls(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"fs"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "a.txt"), "A")

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	roundOneAssistant := ai.NewAssistantMessage(agent.model)
	roundOneAssistant.Phase = ai.AssistantPhaseCommentary
	roundOneAssistant.StopReason = ai.StopReasonToolUse
	roundOneAssistant.Content = []ai.ContentBlock{
		{Type: ai.ContentTypeText, Text: "Looking that up now."},
		{Type: ai.ContentTypeToolCall, ID: "call_1", Name: "read", Arguments: map[string]any{"path": "a.txt"}},
	}

	roundTwoAssistant := ai.NewAssistantMessage(agent.model)
	roundTwoAssistant.StopReason = ai.StopReasonStop
	roundTwoAssistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}

	agent.Provider = &mockProvider{rounds: []mockRound{
		{assistant: roundOneAssistant},
		{assistant: roundTwoAssistant},
	}}

	result, err := agent.RunTurn(context.Background(), agent.NewSession(), TurnInput{UserMessage: "read a.txt"})
	if err != nil {
		t.Fatalf("RunTurn() error: %v", err)
	}
	if result.FinalText != "done" {
		t.Fatalf("final text = %q, want done", result.FinalText)
	}

	gotTypes := make([]EventType, 0, len(result.Events))
	for _, event := range result.Events {
		gotTypes = append(gotTypes, event.Type)
	}

	expectedPrefix := []EventType{EventTypeAgentMsg, EventTypeToolCall, EventTypeToolResult, EventTypeAgentMsg}
	if len(gotTypes) < len(expectedPrefix) {
		t.Fatalf("event count = %d, want at least %d (%v)", len(gotTypes), len(expectedPrefix), gotTypes)
	}
	for i, want := range expectedPrefix {
		if gotTypes[i] != want {
			t.Fatalf("event[%d] = %q, want %q (all=%v)", i, gotTypes[i], want, gotTypes)
		}
	}

	firstPayload := eventPayloadForType(result.Events, EventTypeAgentMsg)
	if got := strings.TrimSpace(fmt.Sprint(firstPayload["text"])); got != "Looking that up now." {
		t.Fatalf("first agent.message text = %q, want commentary text", got)
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

func TestRunTurnKeepsNormalToolResultContextPayloadAndEmittedResult(t *testing.T) {
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
	var contextToolResultPayload string
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

	for _, msg := range session.Messages {
		if msg.Role != ai.RoleToolResult {
			continue
		}
		blocks, ok := msg.ContentBlocks()
		if !ok || len(blocks) == 0 {
			continue
		}
		contextToolResultPayload = blocks[0].Text
	}
	if emittedToolResultContentLen == 0 {
		t.Fatalf("expected emitted tool.result payload content")
	}
	if contextToolResultPayload == "" {
		t.Fatalf("expected tool result payload in session context")
	}
	if strings.Contains(contextToolResultPayload, "emergency_context_cap") {
		t.Fatalf("normal tool payload should not trigger emergency cap")
	}
	if session.LastContextDiagnostics.ToolResultTruncation != 0 {
		t.Fatalf("tool_result_truncation_count = %d, want 0", session.LastContextDiagnostics.ToolResultTruncation)
	}
}

func TestRunTurnAppliesEmergencyToolResultCapForPathologicalPayload(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	hugeContent := strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", 12000)
	mustWriteFile(t, filepath.Join(workspace, "huge.txt"), hugeContent)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	roundOne := ai.NewAssistantMessage(agent.model)
	roundOne.StopReason = ai.StopReasonToolUse
	roundOne.Content = []ai.ContentBlock{
		{Type: ai.ContentTypeToolCall, ID: "read_huge", Name: "read", Arguments: map[string]any{"path": "huge.txt"}},
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
	result, err := agent.RunTurn(context.Background(), session, TurnInput{UserMessage: "read huge file"})
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
	if emittedToolResultContentLen <= toolResultEmergencyMaxChars {
		t.Fatalf("expected emitted tool result content to exceed emergency cap, len=%d", emittedToolResultContentLen)
	}

	foundEmergencyEnvelope := false
	for _, msg := range session.Messages {
		if msg.Role != ai.RoleToolResult {
			continue
		}
		blocks, ok := msg.ContentBlocks()
		if !ok || len(blocks) == 0 {
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(blocks[0].Text), &envelope); err != nil {
			t.Fatalf("expected JSON envelope for emergency cap payload: %v", err)
		}
		if truncated, _ := envelope["truncated"].(bool); !truncated {
			continue
		}
		if reason, _ := envelope["reason"].(string); reason != "emergency_context_cap" {
			t.Fatalf("reason = %q, want emergency_context_cap", reason)
		}
		foundEmergencyEnvelope = true
	}
	if !foundEmergencyEnvelope {
		t.Fatalf("expected emergency cap envelope in context tool result")
	}
	if session.LastContextDiagnostics.ToolResultTruncation != 1 {
		t.Fatalf("tool_result_truncation_count = %d, want 1", session.LastContextDiagnostics.ToolResultTruncation)
	}
}
