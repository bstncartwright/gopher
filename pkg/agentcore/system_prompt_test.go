package agentcore

import (
	"strings"
	"testing"
)

func TestBuildCollaborationSectionSingleAgentMentionsAutoCreate(t *testing.T) {
	registry := NewToolRegistry([]Tool{&delegateTool{}})
	section := buildCollaborationSection(systemPromptInput{
		AgentID:     "milo",
		KnownAgents: []string{"milo"},
		Tools:       registry,
	})
	if section == "" {
		t.Fatalf("expected collaboration section")
	}
	if !strings.Contains(section, "auto-create ephemeral subagents") {
		t.Fatalf("expected auto-create guidance, got: %s", section)
	}
	if !strings.Contains(section, "delegation.completed") || !strings.Contains(section, "delegation.failed") {
		t.Fatalf("expected async completion guidance, got: %s", section)
	}
	if strings.Contains(section, "Delegation is unavailable in a single-agent runtime") {
		t.Fatalf("did not expect single-agent unavailable warning")
	}
}

func TestBuildToolUsageHintsDelegateMentionsOmittedTarget(t *testing.T) {
	registry := NewToolRegistry([]Tool{&delegateTool{}})
	hints := buildToolUsageHints(registry)
	if !strings.Contains(hints, "omitting target auto-creates a subagent") {
		t.Fatalf("expected omitted target hint, got: %s", hints)
	}
	if !strings.Contains(hints, "optional `model_policy`") {
		t.Fatalf("expected model policy hint, got: %s", hints)
	}
	if !strings.Contains(hints, "returns after spawn") || !strings.Contains(hints, "delegation.completed") {
		t.Fatalf("expected async delegation hint, got: %s", hints)
	}
}

func TestBuildCollaborationSectionMultiAgentMentionsAsyncDelegationCompletion(t *testing.T) {
	registry := NewToolRegistry([]Tool{&delegateTool{}})
	section := buildCollaborationSection(systemPromptInput{
		AgentID:     "milo",
		KnownAgents: []string{"milo", "worker"},
		Tools:       registry,
	})
	if section == "" {
		t.Fatalf("expected collaboration section")
	}
	if !strings.Contains(section, "action:\"create\"") || !strings.Contains(section, "task-specific `message`") {
		t.Fatalf("expected create delegation guidance, got: %s", section)
	}
	if !strings.Contains(section, "`model_policy`") {
		t.Fatalf("expected model policy guidance, got: %s", section)
	}
	if !strings.Contains(section, "delegation.completed") || !strings.Contains(section, "delegation.failed") {
		t.Fatalf("expected async completion guidance, got: %s", section)
	}
}
