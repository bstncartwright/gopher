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
}
