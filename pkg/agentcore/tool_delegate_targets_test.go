package agentcore

import (
	"context"
	"testing"
)

func TestDelegateTargetsToolListsLocalAndRemoteTargets(t *testing.T) {
	tool := &delegateTargetsTool{}
	output, err := tool.Run(context.Background(), ToolInput{
		Agent: &Agent{
			ID:          "main",
			KnownAgents: []string{"main", "writer", "reviewer", "writer"},
			RemoteDelegationTargets: []RemoteDelegationTarget{
				{ID: "a2a:research", Description: "Deep research"},
				{ID: " ", Description: "ignored"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want %q", output.Status, ToolStatusOK)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", output.Result)
	}
	if got := result["agent_id"]; got != "main" {
		t.Fatalf("agent_id = %#v, want main", got)
	}
	if got := result["ephemeral_available"]; got != true {
		t.Fatalf("ephemeral_available = %#v, want true", got)
	}

	localTargets, ok := result["local_targets"].([]string)
	if !ok {
		t.Fatalf("local_targets type = %T, want []string", result["local_targets"])
	}
	if len(localTargets) != 2 || localTargets[0] != "reviewer" || localTargets[1] != "writer" {
		t.Fatalf("local_targets = %#v, want [reviewer writer]", localTargets)
	}

	remoteTargets, ok := result["remote_targets"].([]map[string]any)
	if !ok {
		t.Fatalf("remote_targets type = %T, want []map[string]any", result["remote_targets"])
	}
	if len(remoteTargets) != 1 {
		t.Fatalf("remote_targets len = %d, want 1", len(remoteTargets))
	}
	if got := remoteTargets[0]["id"]; got != "a2a:research" {
		t.Fatalf("remote_targets[0].id = %#v, want a2a:research", got)
	}
}

func TestDelegateTargetsToolErrorsWithoutAgent(t *testing.T) {
	tool := &delegateTargetsTool{}
	output, err := tool.Run(context.Background(), ToolInput{})
	if err == nil {
		t.Fatalf("expected error without agent")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want %q", output.Status, ToolStatusError)
	}
}
