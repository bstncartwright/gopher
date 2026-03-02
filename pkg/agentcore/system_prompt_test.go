package agentcore

import (
	"regexp"
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

func TestBuildAgentSystemPromptIncludesConcreteDateAndTime(t *testing.T) {
	prompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:    "/tmp/workspace",
		PromptMode:   PromptModeMinimal,
		UserTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("buildAgentSystemPrompt() error: %v", err)
	}

	if !strings.Contains(prompt, "## Current Date & Time") {
		t.Fatalf("expected current date section, got: %s", prompt)
	}
	if !strings.Contains(prompt, "Time zone: UTC") {
		t.Fatalf("expected UTC timezone line, got: %s", prompt)
	}

	datePattern := regexp.MustCompile(`Current date: \d{4}-\d{2}-\d{2}`)
	if !datePattern.MatchString(prompt) {
		t.Fatalf("expected formatted current date line, got: %s", prompt)
	}
	if strings.Contains(prompt, "Current time:") {
		t.Fatalf("did not expect current time line, got: %s", prompt)
	}
	if strings.Contains(prompt, "Current timestamp:") {
		t.Fatalf("did not expect current timestamp line, got: %s", prompt)
	}
}
