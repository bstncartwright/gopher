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

	workspaceFilesIndex := strings.Index(ctxA.SystemPrompt, "## Workspace Files (injected)")
	projectContextIndex := strings.Index(ctxA.SystemPrompt, "# Project Context")
	workingMemoryIndex := strings.Index(ctxA.SystemPrompt, "## Working Memory (gopher extension)")
	if !(workspaceFilesIndex >= 0 && projectContextIndex > workspaceFilesIndex && workingMemoryIndex > projectContextIndex) {
		t.Fatalf("system prompt sections out of order: %s", ctxA.SystemPrompt)
	}
	if !strings.Contains(ctxA.SystemPrompt, "## AGENTS.md") {
		t.Fatalf("expected AGENTS.md to be injected in project context")
	}
	if strings.Contains(ctxA.SystemPrompt, "## Heartbeats") {
		t.Fatalf("did not expect heartbeat section when heartbeat is disabled")
	}
	if contextHasTool(ctxA, "message") {
		t.Fatalf("did not expect message tool in provider context when message service is unavailable")
	}

	if len(ctxA.Messages) != 4 {
		t.Fatalf("expected 4 messages (full token-budgeted history + 1 new), got %d", len(ctxA.Messages))
	}
	if ctxA.Messages[0].Role != ai.RoleUser || ctxA.Messages[1].Role != ai.RoleAssistant || ctxA.Messages[2].Role != ai.RoleUser || ctxA.Messages[3].Role != ai.RoleUser {
		t.Fatalf("unexpected role order: %#v", []ai.MessageRole{ctxA.Messages[0].Role, ctxA.Messages[1].Role, ctxA.Messages[2].Role, ctxA.Messages[3].Role})
	}
}

func TestContextBuilderIncludesHeartbeatSectionWhenConfigured(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{Every: "5m"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	session := &Session{ID: "s-hb"}

	ctx, err := agent.buildProviderContext(context.Background(), session, "heartbeat")
	if err != nil {
		t.Fatalf("buildProviderContext() error: %v", err)
	}
	if !strings.Contains(ctx.SystemPrompt, "## Heartbeats") {
		t.Fatalf("expected heartbeat section in system prompt")
	}
}

func TestContextBuilderPromptModes(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	session := &Session{ID: "s-modes"}

	minimalCtx, err := agent.buildProviderContext(context.Background(), session, "hello", PromptModeMinimal)
	if err != nil {
		t.Fatalf("buildProviderContext(minimal) error: %v", err)
	}
	if strings.Contains(minimalCtx.SystemPrompt, "## Heartbeats") {
		t.Fatalf("did not expect heartbeats section in minimal mode")
	}
	if strings.Contains(minimalCtx.SystemPrompt, "## Reply Tags") {
		t.Fatalf("did not expect reply tags section in minimal mode")
	}
	if !strings.Contains(minimalCtx.SystemPrompt, "## AGENTS.md") || !strings.Contains(minimalCtx.SystemPrompt, "## TOOLS.md") {
		t.Fatalf("expected AGENTS.md and TOOLS.md in minimal project context")
	}
	if strings.Contains(minimalCtx.SystemPrompt, "## SOUL.md") {
		t.Fatalf("did not expect SOUL.md in minimal project context")
	}

	noneCtx, err := agent.buildProviderContext(context.Background(), session, "hello", PromptModeNone)
	if err != nil {
		t.Fatalf("buildProviderContext(none) error: %v", err)
	}
	if strings.TrimSpace(noneCtx.SystemPrompt) != "You are a personal assistant running inside gopher." {
		t.Fatalf("unexpected none-mode system prompt: %s", noneCtx.SystemPrompt)
	}
}

func TestContextBuilderIncludesMessageToolWhenServiceAvailable(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:collaboration"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.MessageService = &testMessageToolService{}
	agent.ReactionService = &testReactionToolService{}
	session := &Session{ID: "s-message"}

	ctx, err := agent.buildProviderContext(context.Background(), session, "hello")
	if err != nil {
		t.Fatalf("buildProviderContext() error: %v", err)
	}
	if !contextHasTool(ctx, "message") {
		t.Fatalf("expected message tool in provider context when message service is available")
	}
	if !contextHasTool(ctx, "reaction") {
		t.Fatalf("expected reaction tool in provider context when reaction service is available")
	}
}

func contextHasTool(ctx ai.Context, name string) bool {
	for _, tool := range ctx.Tools {
		if strings.TrimSpace(tool.Name) == name {
			return true
		}
	}
	return false
}

type testMessageToolService struct{}

func (s *testMessageToolService) SendMessage(_ context.Context, req MessageSendRequest) (MessageSendResult, error) {
	return MessageSendResult{
		Sent:            true,
		ConversationID:  "test-conversation",
		Text:            strings.TrimSpace(req.Text),
		AttachmentCount: len(req.Attachments),
	}, nil
}

type testReactionToolService struct{}

func (s *testReactionToolService) SendReaction(_ context.Context, req ReactionSendRequest) (ReactionSendResult, error) {
	return ReactionSendResult{
		Sent:           true,
		ConversationID: "test-conversation",
		TargetEventID:  "1",
		Emoji:          strings.TrimSpace(req.Emoji),
	}, nil
}
