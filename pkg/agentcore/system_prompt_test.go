package agentcore

import (
	"os"
	"path/filepath"
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
	if !strings.Contains(hints, "do not `sleep`/busy-wait") {
		t.Fatalf("expected non-blocking delegation hint, got: %s", hints)
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
	if !strings.Contains(section, "do not block this turn with `sleep`/poll loops") {
		t.Fatalf("expected non-blocking completion guidance, got: %s", section)
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

func TestBuildAgentSystemPromptIncludesPeerStyleGuardrails(t *testing.T) {
	prompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:  "/tmp/workspace",
		PromptMode: PromptModeMinimal,
	})
	if err != nil {
		t.Fatalf("buildAgentSystemPrompt() error: %v", err)
	}

	required := []string{
		"You are a practical collaborator running inside gopher.",
		"## Style",
		"Speak like a peer collaborator with a consistent voice and practical judgment.",
		"Ground replies in shared context, concrete next steps, and clear tradeoffs.",
		"Use natural language and brief intros, then move to the work.",
	}
	for _, needle := range required {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected style guidance %q, got: %s", needle, prompt)
		}
	}
}

func TestBuildAgentSystemPromptMessageKickoffPreferenceOnlyWhenMessageToolEnabled(t *testing.T) {
	withMessage, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:  "/tmp/workspace",
		PromptMode: PromptModeMinimal,
		Tools:      NewToolRegistry([]Tool{&messageTool{}}),
	})
	if err != nil {
		t.Fatalf("buildAgentSystemPrompt() with message tool error: %v", err)
	}
	if !strings.Contains(withMessage, "prefer using `message` for the kickoff acknowledgement") {
		t.Fatalf("expected message kickoff preference, got: %s", withMessage)
	}
	if !strings.Contains(withMessage, "This is encouraged, not required") {
		t.Fatalf("expected non-mandatory wording, got: %s", withMessage)
	}

	withoutMessage, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:  "/tmp/workspace",
		PromptMode: PromptModeMinimal,
		Tools:      NewToolRegistry(nil),
	})
	if err != nil {
		t.Fatalf("buildAgentSystemPrompt() without message tool error: %v", err)
	}
	if strings.Contains(withoutMessage, "prefer using `message` for the kickoff acknowledgement") {
		t.Fatalf("did not expect message kickoff preference without message tool, got: %s", withoutMessage)
	}
}

func TestBuildAgentSystemPromptSelfUpdateInstructionsAreExplicit(t *testing.T) {
	prompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:  "/tmp/workspace",
		PromptMode: PromptModeFull,
	})
	if err != nil {
		t.Fatalf("buildAgentSystemPrompt() error: %v", err)
	}

	required := []string{
		"## Gopher Self-Update",
		"Treat requests like \"update yourself\", \"update itself\", or \"self-update\" as explicit self-update requests.",
		"For binary updates, prefer `gopher_update` when available; otherwise run `gopher update` using available execution tools and report the actual command result.",
		"Do not replace a requested self-update with memory updates, policy notes, or future-intent promises.",
		"Memory retrieval is tool-driven: do not assume retrieved-memory snippets are auto-injected into context.",
		"Before answering prior-work, preference, or history questions, call `memory_search` to retrieve relevant memory snippets.",
		"Requests to change or upgrade your model are config changes, not impossible actions.",
		"When asked to switch models, update your agent `config.toml` `model_policy` to the requested `provider:model` value and restart the gateway or relevant runtime.",
		"Do not claim model switching is impossible just because it requires a config edit or restart.",
	}
	for _, needle := range required {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("expected self-update instruction %q, got: %s", needle, prompt)
		}
	}
}

func TestBuildAgentSystemPromptDoesNotInstructRawReplyTagEmission(t *testing.T) {
	prompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:  "/tmp/workspace",
		PromptMode: PromptModeFull,
		Heartbeat: AgentHeartbeat{
			Enabled: true,
		},
	})
	if err != nil {
		t.Fatalf("buildAgentSystemPrompt() error: %v", err)
	}
	if strings.Contains(prompt, "[[reply_to_current]]") {
		t.Fatalf("prompt should not include raw reply tag marker, got: %s", prompt)
	}
	if !strings.Contains(prompt, "HEARTBEAT_OK is internal status only") {
		t.Fatalf("expected heartbeat internal-status guidance, got: %s", prompt)
	}
	if !strings.Contains(prompt, "latest user-visible non-HEARTBEAT_OK alert") {
		t.Fatalf("expected heartbeat feedback interpretation guidance, got: %s", prompt)
	}
}

func TestBuildAgentSystemPromptIncludesTemplateMaintenanceWhenUpdatesArePending(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "TEMPLATE_UPDATES.md"), []byte("pending updates"), 0o644); err != nil {
		t.Fatalf("write TEMPLATE_UPDATES.md: %v", err)
	}

	prompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:  workspace,
		PromptMode: PromptModeFull,
	})
	if err != nil {
		t.Fatalf("buildAgentSystemPrompt() error: %v", err)
	}
	if !strings.Contains(prompt, "## Workspace Maintenance") {
		t.Fatalf("expected workspace maintenance section, got: %s", prompt)
	}
	if !strings.Contains(prompt, "read `TEMPLATE_UPDATES.md`") {
		t.Fatalf("expected template updates instruction, got: %s", prompt)
	}
}
