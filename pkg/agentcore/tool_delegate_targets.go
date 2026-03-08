package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type delegateTargetsTool struct{}

func (t *delegateTargetsTool) Name() string {
	return "delegate_targets"
}

func (t *delegateTargetsTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "List the agents and remote targets currently available for delegation from this agent, excluding self from local targets.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *delegateTargetsTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	if input.Agent == nil {
		err := fmt.Errorf("agent is required")
		slog.Error("delegate_targets_tool: agent is required")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	agentID := strings.TrimSpace(input.Agent.ID)
	localTargets := make([]string, 0)
	for _, candidate := range normalizeUniqueStrings(input.Agent.KnownAgents) {
		if candidate == agentID {
			continue
		}
		localTargets = append(localTargets, candidate)
	}

	remoteTargets := make([]map[string]any, 0, len(input.Agent.RemoteDelegationTargets))
	for _, target := range input.Agent.RemoteDelegationTargets {
		id := strings.TrimSpace(target.ID)
		if id == "" {
			continue
		}
		remoteTargets = append(remoteTargets, map[string]any{
			"id":          id,
			"description": strings.TrimSpace(target.Description),
		})
	}

	result := map[string]any{
		"agent_id":                   agentID,
		"local_targets":              localTargets,
		"remote_targets":             remoteTargets,
		"ephemeral_available":        true,
		"target_agent_optional":      true,
		"recommended_tool":           "delegate",
		"local_target_count":         len(localTargets),
		"remote_target_count":        len(remoteTargets),
		"delegate_target_total":      len(localTargets) + len(remoteTargets),
		"known_agents_in_runtime":    normalizeUniqueStrings(input.Agent.KnownAgents),
		"current_agent_excluded":     agentID != "",
		"delegation_guidance":        "Use delegate action:create with target_agent for a specific worker, or omit target_agent to auto-create an ephemeral worker.",
		"supports_remote_delegation": len(remoteTargets) > 0,
	}

	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}
