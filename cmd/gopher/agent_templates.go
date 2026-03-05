package main

import (
	"embed"
	"fmt"
	"strings"
)

const defaultAgentModelPolicy = "openai-codex:gpt-5.4"

//go:embed default_templates/*.md
var defaultTemplateFS embed.FS

func mustDefaultTemplate(name string) string {
	blob, err := defaultTemplateFS.ReadFile("default_templates/" + name)
	if err != nil {
		panic(fmt.Sprintf("read default template %s: %v", name, err))
	}
	return strings.TrimSuffix(string(blob), "\n")
}

func defaultAgentsTemplate(_ string) string {
	return mustDefaultTemplate("AGENTS.md")
}

func defaultSoulTemplate() string {
	return mustDefaultTemplate("SOUL.md")
}

func defaultToolsTemplate() string {
	return mustDefaultTemplate("TOOLS.md")
}

func defaultIdentityTemplate() string {
	return mustDefaultTemplate("IDENTITY.md")
}

func defaultUserTemplate() string {
	return mustDefaultTemplate("USER.md")
}

func defaultSharedUserTemplate() string {
	return `# USER.md - Shared User Profile

This file is shared by all agents in this workspace collection.
Keep stable identity/preferences here so every agent starts with the same user context.

- Name:
- Preferred name:
- Pronouns (optional):
- Timezone:
- Preferences:

## Context

Track goals, active projects, communication preferences, and constraints that should apply to every agent.
`
}

func defaultHeartbeatTemplate() string {
	return mustDefaultTemplate("HEARTBEAT.md")
}

func defaultBootstrapTemplate() string {
	return mustDefaultTemplate("BOOTSTRAP.md")
}

func defaultConfigTemplate(agentID string) string {
	return fmt.Sprintf(`agent_id = %q
name = %q
role = "assistant"
model_policy = %q
reasoning_level = "medium"
max_context_messages = 40

[context_management]
mode = "safeguard"
enable_pruning = true
enable_compaction = true
enable_overflow_retry = true
overflow_retry_limit = 3
reserve_min_tokens = 20000
model_compaction_summary = true
compaction_summary_timeout_ms = 12000
compaction_chunk_token_target = 1800

[policies]
allow_cross_agent_fs = true
can_shell = true
shell_allowlist = []

[policies.network]
enabled = true
block_domains = []

[policies.budget]
max_tokens_per_session = 200000
`, agentID, agentID, defaultAgentModelPolicy)
}
