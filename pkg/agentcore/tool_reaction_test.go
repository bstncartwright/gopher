package agentcore

import (
	"context"
	"strings"
	"testing"
)

type fakeReactionToolService struct {
	lastReq ReactionSendRequest
}

func (s *fakeReactionToolService) SendReaction(_ context.Context, req ReactionSendRequest) (ReactionSendResult, error) {
	s.lastReq = req
	return ReactionSendResult{
		Sent:           true,
		ConversationID: "telegram:123",
		TargetEventID:  "42",
		Emoji:          strings.TrimSpace(req.Emoji),
	}, nil
}

func TestReactionToolSendsReaction(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"reaction"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeReactionToolService{}
	agent.ReactionService = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	session.ID = "sess-reaction"

	output, err := runner.Run(context.Background(), session, toolCall("reaction", map[string]any{
		"emoji": "👍",
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastReq.SessionID != "sess-reaction" {
		t.Fatalf("session id = %q, want sess-reaction", fake.lastReq.SessionID)
	}
	if fake.lastReq.Emoji != "👍" {
		t.Fatalf("emoji = %q, want 👍", fake.lastReq.Emoji)
	}
}

func TestReactionToolRequiresArgs(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"reaction"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.ReactionService = &fakeReactionToolService{}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	_, err = runner.Run(context.Background(), session, toolCall("reaction", map[string]any{
		"target_event_id": "42",
	}))
	if err == nil {
		t.Fatalf("expected validation error for missing emoji")
	}
}

func TestReactionToolUnavailableWhenServiceMissing(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"reaction"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	_, err = runner.Run(context.Background(), session, toolCall("reaction", map[string]any{
		"emoji": "👍",
	}))
	if err == nil {
		t.Fatalf("expected unavailable tool error")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %q, want tool not registered", err.Error())
	}
}
