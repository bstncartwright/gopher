package agentcore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestSessionRuntimeAdapterRoutesUserToRunTurn(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"fs"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	finalAssistant := ai.NewAssistantMessage(agent.model)
	finalAssistant.StopReason = ai.StopReasonStop
	finalAssistant.Content = []ai.ContentBlock{
		{Type: ai.ContentTypeText, Text: "adapter-ok"},
	}
	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{assistant: finalAssistant},
		},
	}

	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: NewSessionRuntimeAdapter(agent),
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

	if err := manager.SendEvent(context.Background(), sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "say adapter-ok"},
	}); err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected at least creation + user + agent events, got %d", len(events))
	}

	foundUser := false
	foundAgent := false
	for _, event := range events {
		if event.Type != sessionrt.EventMessage {
			continue
		}
		msg, ok := event.Payload.(sessionrt.Message)
		if !ok {
			t.Fatalf("expected session message payload type, got %T", event.Payload)
		}
		if msg.Role == sessionrt.RoleUser {
			foundUser = true
		}
		if msg.Role == sessionrt.RoleAgent && msg.Content == "adapter-ok" {
			foundAgent = true
		}
	}
	if !foundUser {
		t.Fatalf("expected user message event in session history")
	}
	if !foundAgent {
		t.Fatalf("expected agent response event in session history")
	}
}

func TestLatestPromptMessageAcceptsTargetedAgentMessage(t *testing.T) {
	events := []sessionrt.Event{
		{
			Type: sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleAgent,
				Content: "status update",
			},
		},
		{
			Type: sessionrt.EventMessage,
			Payload: map[string]any{
				"role":            "agent",
				"content":         "please handle this",
				"target_actor_id": "agent:writer",
			},
		},
	}

	msg, ok := latestPromptMessage(events, "agent:writer")
	if !ok {
		t.Fatalf("expected targeted agent message to be accepted")
	}
	if msg.Role != sessionrt.RoleAgent {
		t.Fatalf("message role = %q, want agent", msg.Role)
	}
	if msg.TargetActorID != "agent:writer" {
		t.Fatalf("target actor id = %q, want agent:writer", msg.TargetActorID)
	}
	if msg.Content != "please handle this" {
		t.Fatalf("content = %q, want %q", msg.Content, "please handle this")
	}
}

func TestLatestPromptMessageSkipsUntargetedAgentMessage(t *testing.T) {
	events := []sessionrt.Event{
		{
			Type: sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleAgent,
				Content: "status update",
			},
		},
	}

	if msg, ok := latestPromptMessage(events, "agent:writer"); ok {
		t.Fatalf("expected no prompt message, got %+v", msg)
	}
}

func TestToolResultMessageFromPayloadSkipsProviderNativeSearchResults(t *testing.T) {
	msg, ok := toolResultMessageFromPayload(sessionrt.Event{
		ID: "evt-1",
		Payload: map[string]any{
			"name":    "web_search",
			"backend": "provider_native",
			"status":  "ok",
			"result":  map[string]any{"query": "weather"},
		},
	})
	if ok {
		t.Fatalf("expected provider-native web_search replay payload to be skipped, got %#v", msg)
	}
}

func TestWithTurnTimeoutAddsDeadlineWhenMissing(t *testing.T) {
	prev := sessionRuntimeTurnTimeout
	sessionRuntimeTurnTimeout = time.Second
	defer func() { sessionRuntimeTurnTimeout = prev }()

	ctx, cancel := withTurnTimeout(context.Background())
	defer cancel()

	if _, ok := ctx.Deadline(); !ok {
		t.Fatalf("expected deadline to be added")
	}
}

func TestWithTurnTimeoutPreservesExistingDeadline(t *testing.T) {
	prev := sessionRuntimeTurnTimeout
	sessionRuntimeTurnTimeout = time.Second
	defer func() { sessionRuntimeTurnTimeout = prev }()

	base, baseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer baseCancel()
	baseDeadline, _ := base.Deadline()

	ctx, cancel := withTurnTimeout(base)
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("expected existing deadline to be preserved")
	}
	if !deadline.Equal(baseDeadline) {
		t.Fatalf("deadline changed: got %s want %s", deadline, baseDeadline)
	}
}

func TestSessionRuntimeTurnTimeoutDefault(t *testing.T) {
	if sessionRuntimeTurnTimeout != 5*time.Minute {
		t.Fatalf("default timeout = %s, want %s", sessionRuntimeTurnTimeout, 5*time.Minute)
	}
}

func TestSessionRuntimeAdapterDeltaCaptureOptions(t *testing.T) {
	config := defaultConfig()
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.CaptureThinkingDeltas = true

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "final"}}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{
				assistant: assistant,
				events: []ai.AssistantMessageEvent{
					{Type: ai.EventTextDelta, Delta: "fi"},
					{Type: ai.EventThinkingDelta, Delta: "private"},
					{Type: ai.EventTextDelta, Delta: "nal"},
				},
			},
		},
	}

	input := sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "agent:test",
		History: []sessionrt.Event{
			{
				Type:    sessionrt.EventMessage,
				Payload: sessionrt.Message{Role: sessionrt.RoleUser, Content: "run"},
			},
		},
	}

	defaultAdapter := NewSessionRuntimeAdapter(agent)
	defaultOut, err := defaultAdapter.Step(context.Background(), input)
	if err != nil {
		t.Fatalf("default adapter Step() error: %v", err)
	}
	if hasEventType(defaultOut.Events, sessionrt.EventAgentDelta) {
		t.Fatalf("default adapter should not emit agent deltas")
	}
	if hasEventType(defaultOut.Events, sessionrt.EventAgentThinkingDelta) {
		t.Fatalf("default adapter should not emit thinking deltas")
	}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{
				assistant: assistant,
				events: []ai.AssistantMessageEvent{
					{Type: ai.EventTextDelta, Delta: "fi"},
					{Type: ai.EventThinkingDelta, Delta: "private"},
					{Type: ai.EventTextDelta, Delta: "nal"},
				},
			},
		},
	}

	optAdapter := NewSessionRuntimeAdapterWithOptions(agent, SessionRuntimeAdapterOptions{
		CaptureDeltas:   true,
		CaptureThinking: true,
	})
	optOut, err := optAdapter.Step(context.Background(), input)
	if err != nil {
		t.Fatalf("options adapter Step() error: %v", err)
	}
	if !hasEventType(optOut.Events, sessionrt.EventAgentDelta) {
		t.Fatalf("expected options adapter to emit agent delta events")
	}
	if !hasEventType(optOut.Events, sessionrt.EventAgentThinkingDelta) {
		t.Fatalf("expected options adapter to emit thinking delta events")
	}
}

func TestSessionRuntimeAdapterStepStreamEmitsMappedEvents(t *testing.T) {
	config := defaultConfig()
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.CaptureThinkingDeltas = true

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "final"}}

	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{
				assistant: assistant,
				events: []ai.AssistantMessageEvent{
					{Type: ai.EventTextDelta, Delta: "fi"},
					{Type: ai.EventTextDelta, Delta: "nal"},
				},
			},
		},
	}

	adapter := NewSessionRuntimeAdapterWithOptions(agent, SessionRuntimeAdapterOptions{
		CaptureDeltas: true,
	})

	input := sessionrt.AgentInput{
		SessionID: "sess-stream",
		ActorID:   "agent:test",
		History: []sessionrt.Event{
			{
				Type:    sessionrt.EventMessage,
				Payload: sessionrt.Message{Role: sessionrt.RoleUser, Content: "run"},
			},
		},
	}

	events := make([]sessionrt.Event, 0, 4)
	if err := adapter.StepStream(context.Background(), input, func(event sessionrt.Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("StepStream() error: %v", err)
	}

	if !hasEventType(events, sessionrt.EventAgentDelta) {
		t.Fatalf("expected streamed agent delta event")
	}
	if !hasEventType(events, sessionrt.EventMessage) {
		t.Fatalf("expected streamed final message event")
	}
	if !hasEventType(events, sessionrt.EventStatePatch) {
		t.Fatalf("expected streamed state patch event")
	}
}

func TestSessionRuntimeAdapterStepEmitsStatePatch(t *testing.T) {
	config := defaultConfig()
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "done"}}
	agent.Provider = &mockProvider{
		rounds: []mockRound{
			{assistant: assistant},
		},
	}

	adapter := NewSessionRuntimeAdapter(agent)
	out, err := adapter.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-state-patch",
		ActorID:   sessionrt.ActorID(agent.ID),
		History: []sessionrt.Event{
			{
				Type:    sessionrt.EventMessage,
				Payload: sessionrt.Message{Role: sessionrt.RoleUser, Content: "run"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}

	foundPatch := false
	for _, event := range out.Events {
		if event.Type != sessionrt.EventStatePatch {
			continue
		}
		payload, ok := event.Payload.(map[string]any)
		if !ok {
			t.Fatalf("state patch payload type = %T, want map[string]any", event.Payload)
		}
		if got, _ := payload["model_id"].(string); strings.TrimSpace(got) == "" {
			t.Fatalf("state patch missing model_id: %#v", payload)
		}
		foundPatch = true
	}
	if !foundPatch {
		t.Fatalf("expected state patch event from adapter")
	}
}

func TestSessionRuntimeAdapterHydratesHistoryOnFirstTurn(t *testing.T) {
	config := defaultConfig()
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	assistant := ai.NewAssistantMessage(agent.model)
	assistant.StopReason = ai.StopReasonStop
	assistant.Content = []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "fresh reply"}}
	provider := &mockProvider{
		rounds: []mockRound{
			{assistant: assistant},
		},
	}
	agent.Provider = provider

	adapter := NewSessionRuntimeAdapter(agent)
	now := time.Now().UTC()
	_, err = adapter.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-restart",
		ActorID:   sessionrt.ActorID(agent.ID),
		History: []sessionrt.Event{
			{
				Type:      sessionrt.EventMessage,
				From:      "user:me",
				Timestamp: now.Add(-3 * time.Minute),
				Seq:       1,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleUser,
					Content: "earlier question",
				},
			},
			{
				Type:      sessionrt.EventMessage,
				From:      sessionrt.ActorID(agent.ID),
				Timestamp: now.Add(-2 * time.Minute),
				Seq:       2,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: "earlier answer",
				},
			},
			{
				Type:      sessionrt.EventMessage,
				From:      "user:me",
				Timestamp: now.Add(-1 * time.Minute),
				Seq:       3,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleUser,
					Content: "latest prompt",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}

	if len(provider.contexts) != 1 {
		t.Fatalf("provider contexts = %d, want 1", len(provider.contexts))
	}
	messages := provider.contexts[0].Messages
	if len(messages) != 3 {
		t.Fatalf("provider message count = %d, want 3", len(messages))
	}
	if messages[0].Role != ai.RoleUser || strings.TrimSpace(messages[0].Content.(string)) != "earlier question" {
		t.Fatalf("message[0] mismatch: %#v", messages[0])
	}
	if messages[1].Role != ai.RoleAssistant || strings.TrimSpace(messages[1].Content.(string)) != "earlier answer" {
		t.Fatalf("message[1] mismatch: %#v", messages[1])
	}
	if messages[2].Role != ai.RoleUser || strings.TrimSpace(messages[2].Content.(string)) != "latest prompt" {
		t.Fatalf("message[2] mismatch: %#v", messages[2])
	}
}

func hasEventType(events []sessionrt.Event, target sessionrt.EventType) bool {
	for _, event := range events {
		if event.Type == target {
			return true
		}
	}
	return false
}

func TestPromptMessageFromPayloadDecodesPersistedAttachments(t *testing.T) {
	payload, err := json.Marshal(sessionrt.Message{
		Role:    sessionrt.RoleUser,
		Content: "",
		Attachments: []sessionrt.Attachment{{
			Name:     "photo.jpg",
			MIMEType: "image/jpeg",
			Data:     []byte("img"),
		}},
	})
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	msg, ok := promptMessageFromPayload(decoded)
	if !ok {
		t.Fatalf("expected promptMessageFromPayload() to succeed")
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1", len(msg.Attachments))
	}
	if string(msg.Attachments[0].Data) != "img" {
		t.Fatalf("attachment data = %q", string(msg.Attachments[0].Data))
	}
}

func TestSessionRuntimeAdapterACPRuntimeExecutesCommand(t *testing.T) {
	config := defaultConfig()
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.Config.Runtime.Type = "acp"
	agent.Config.Runtime.ACP.Agent = "codex"
	agent.Config.Runtime.ACP.Command = "python3"
	agent.Config.Runtime.ACP.Args = []string{"-c", "import os;print('acp:'+os.getenv('ACP_PROMPT',''))"}
	agent.Config.Runtime.ACP.Env = map[string]string{"ACP_PROMPT": "{prompt}"}

	adapter := NewSessionRuntimeAdapter(agent)
	output, err := adapter.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-acp",
		ActorID:   "main",
		History: []sessionrt.Event{{
			Type:    sessionrt.EventMessage,
			Payload: sessionrt.Message{Role: sessionrt.RoleUser, Content: "ship it"},
		}},
	})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}
	if len(output.Events) == 0 {
		t.Fatalf("expected response events")
	}
	msg, ok := output.Events[0].Payload.(sessionrt.Message)
	if !ok {
		t.Fatalf("payload type = %T, want sessionrt.Message", output.Events[0].Payload)
	}
	if msg.Content != "acp:ship it" {
		t.Fatalf("content = %q, want %q", msg.Content, "acp:ship it")
	}
}

func TestSessionRuntimeAdapterACPRuntimeBuiltinOpenCodeUsesAgentPlaceholder(t *testing.T) {
	config := defaultConfig()
	config.Runtime.Type = "acp"
	config.Runtime.ACP.Builtin = "opencode"
	config.Runtime.ACP.Command = "python3"
	config.Runtime.ACP.Args = []string{"-c", "import sys;print(sys.argv[1])", "{agent}"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	adapter := NewSessionRuntimeAdapter(agent)
	output, err := adapter.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-acp",
		ActorID:   "main",
		History: []sessionrt.Event{{
			Type:    sessionrt.EventMessage,
			Payload: sessionrt.Message{Role: sessionrt.RoleUser, Content: "ship it"},
		}},
	})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}
	if len(output.Events) == 0 {
		t.Fatalf("expected response events")
	}
	msg, ok := output.Events[0].Payload.(sessionrt.Message)
	if !ok {
		t.Fatalf("payload type = %T, want sessionrt.Message", output.Events[0].Payload)
	}
	if msg.Content != "opencode" {
		t.Fatalf("content = %q, want %q", msg.Content, "opencode")
	}
}
