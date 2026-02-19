package agentcore

import (
	"context"
	"strings"
	"testing"
)

type fakeDelegationToolService struct {
	lastReq DelegationCreateRequest
}

func (s *fakeDelegationToolService) CreateDelegationSession(_ context.Context, req DelegationCreateRequest) (DelegationSession, error) {
	s.lastReq = req
	return DelegationSession{
		SessionID:      "sess-delegate-1",
		ConversationID: "!delegate:local",
		SourceAgentID:  req.SourceAgentID,
		TargetAgentID:  req.TargetAgentID,
		KickoffMessage: req.Message,
	}, nil
}

func TestDelegateToolCreateUsesCurrentSessionID(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"delegate"}
	policies := defaultPolicies()
	workspace := createTestWorkspace(t, config, policies)
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeDelegationToolService{}
	agent.Delegation = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	session.ID = "sess-source"

	output, err := runner.Run(context.Background(), session, toolCall("delegate", map[string]any{
		"action":       "create",
		"target_agent": "writer",
		"message":      "Please help with this task.",
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastReq.SourceSessionID != "sess-source" {
		t.Fatalf("source session = %q, want sess-source", fake.lastReq.SourceSessionID)
	}
	if fake.lastReq.SourceAgentID != strings.TrimSpace(agent.ID) {
		t.Fatalf("source agent = %q, want %q", fake.lastReq.SourceAgentID, strings.TrimSpace(agent.ID))
	}
	if fake.lastReq.TargetAgentID != "writer" {
		t.Fatalf("target agent = %q, want writer", fake.lastReq.TargetAgentID)
	}
}
