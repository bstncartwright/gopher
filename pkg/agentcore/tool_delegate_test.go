package agentcore

import (
	"context"
	"strings"
	"testing"
)

type fakeDelegationToolService struct {
	lastCreateReq DelegationCreateRequest
	lastListReq   DelegationListRequest
	lastKillReq   DelegationKillRequest
	lastLogReq    DelegationLogRequest
}

func (s *fakeDelegationToolService) CreateDelegationSession(_ context.Context, req DelegationCreateRequest) (DelegationSession, error) {
	s.lastCreateReq = req
	return DelegationSession{
		SessionID:       "sess-delegate-1",
		ConversationID:  "!delegate:local",
		SourceSessionID: req.SourceSessionID,
		SourceAgentID:   req.SourceAgentID,
		TargetAgentID:   req.TargetAgentID,
		KickoffMessage:  req.Message,
		Status:          "active",
	}, nil
}

func (s *fakeDelegationToolService) ListDelegationSessions(_ context.Context, req DelegationListRequest) ([]DelegationListItem, error) {
	s.lastListReq = req
	return []DelegationListItem{{
		SessionID:       "sess-delegate-1",
		ConversationID:  "session:sess-delegate-1",
		SourceSessionID: req.SourceSessionID,
		SourceAgentID:   "milo",
		TargetAgentID:   "writer",
		Status:          "active",
	}}, nil
}

func (s *fakeDelegationToolService) KillDelegationSession(_ context.Context, req DelegationKillRequest) (DelegationKillResult, error) {
	s.lastKillReq = req
	return DelegationKillResult{
		SessionID:       req.DelegationID,
		SourceSessionID: req.SourceSessionID,
		Status:          "cancelled",
		Killed:          true,
	}, nil
}

func (s *fakeDelegationToolService) GetDelegationLog(_ context.Context, req DelegationLogRequest) (DelegationLogResult, error) {
	s.lastLogReq = req
	return DelegationLogResult{
		SessionID: req.DelegationID,
		Total:     1,
		Offset:    req.Offset,
		Count:     1,
		Entries: []DelegationLogEntry{{
			Seq:       1,
			Type:      "message",
			Role:      "agent",
			Content:   "done",
			Timestamp: "2026-01-01T00:00:00Z",
		}},
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
	if fake.lastCreateReq.SourceSessionID != "sess-source" {
		t.Fatalf("source session = %q, want sess-source", fake.lastCreateReq.SourceSessionID)
	}
	if fake.lastCreateReq.SourceAgentID != strings.TrimSpace(agent.ID) {
		t.Fatalf("source agent = %q, want %q", fake.lastCreateReq.SourceAgentID, strings.TrimSpace(agent.ID))
	}
	if fake.lastCreateReq.TargetAgentID != "writer" {
		t.Fatalf("target agent = %q, want writer", fake.lastCreateReq.TargetAgentID)
	}
}

func TestDelegateToolCreateDefaultsTargetToCurrentAgent(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"delegate"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
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
		"action":  "create",
		"message": "Please help with this task.",
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastCreateReq.TargetAgentID != strings.TrimSpace(agent.ID) {
		t.Fatalf("target agent = %q, want %q", fake.lastCreateReq.TargetAgentID, strings.TrimSpace(agent.ID))
	}
}

func TestDelegateToolListUsesCurrentSessionScope(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"delegate"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
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
		"action":           "list",
		"include_inactive": true,
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastListReq.SourceSessionID != "sess-source" {
		t.Fatalf("source session = %q, want sess-source", fake.lastListReq.SourceSessionID)
	}
	if !fake.lastListReq.IncludeInactive {
		t.Fatalf("expected include_inactive=true")
	}
}

func TestDelegateToolKillUsesDelegationID(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"delegate"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
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
		"action":        "kill",
		"delegation_id": "sess-delegate-1",
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastKillReq.SourceSessionID != "sess-source" {
		t.Fatalf("source session = %q, want sess-source", fake.lastKillReq.SourceSessionID)
	}
	if fake.lastKillReq.DelegationID != "sess-delegate-1" {
		t.Fatalf("delegation id = %q, want sess-delegate-1", fake.lastKillReq.DelegationID)
	}
}

func TestDelegateToolLogUsesOffsetAndLimit(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"delegate"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
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
		"action":        "log",
		"delegation_id": "sess-delegate-1",
		"offset":        3,
		"limit":         25,
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastLogReq.SourceSessionID != "sess-source" {
		t.Fatalf("source session = %q, want sess-source", fake.lastLogReq.SourceSessionID)
	}
	if fake.lastLogReq.DelegationID != "sess-delegate-1" {
		t.Fatalf("delegation id = %q, want sess-delegate-1", fake.lastLogReq.DelegationID)
	}
	if fake.lastLogReq.Offset != 3 || fake.lastLogReq.Limit != 25 {
		t.Fatalf("offset/limit = %d/%d, want 3/25", fake.lastLogReq.Offset, fake.lastLogReq.Limit)
	}
}
