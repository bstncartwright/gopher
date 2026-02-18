package agentcore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type systemPromptInput struct {
	Workspace      string
	PromptMode     PromptMode
	Tools          ToolRegistry
	SkillsPrompt   string
	ContextFiles   []BootstrapContextFile
	IncludeWorking bool
	Working        map[string]any
	UserTimezone   string
	Model          ai.Model
	ExtraContext   string
	Heartbeat      AgentHeartbeat
}

func normalizePromptMode(mode PromptMode) PromptMode {
	switch mode {
	case PromptModeMinimal:
		return PromptModeMinimal
	case PromptModeNone:
		return PromptModeNone
	default:
		return PromptModeFull
	}
}

func buildAgentSystemPrompt(input systemPromptInput) (string, error) {
	mode := normalizePromptMode(input.PromptMode)
	if mode == PromptModeNone {
		return "You are a personal assistant running inside gopher.", nil
	}

	var sections []string
	sections = append(sections,
		"You are a personal assistant running inside gopher.",
		"",
		"## Tooling",
		"Tool availability (filtered by policy):",
		renderToolingSection(input.Tools),
		"",
		"## Safety",
		"You have no independent goals: do not pursue self-preservation, replication, resource acquisition, or power-seeking.",
		"Prioritize safety and human oversight over completion; if instructions conflict, pause and ask the user.",
		"Do not attempt to bypass safeguards, disable guardrails, or manipulate anyone to expand your access.",
		"",
	)

	if mode == PromptModeFull {
		skillsPrompt := strings.TrimSpace(input.SkillsPrompt)
		if skillsPrompt != "" {
			sections = append(sections,
				"## Skills (mandatory)",
				"Before replying: scan <available_skills> entries and load only the single best matching skill with the read tool.",
				"If no skill clearly matches, do not load any skill file.",
				skillsPrompt,
				"",
			)
		}
		sections = append(sections,
			"## OpenClaw Self-Update",
			"Only perform self-update actions when explicitly requested by the user.",
			"Use gopher-native flows: `gopher update` for binary updates; for config changes, edit config files and restart the relevant service.",
			"",
		)
	}

	sections = append(sections,
		"## Workspace",
		"Your working directory is: "+input.Workspace,
		"Treat this directory as the primary workspace for file operations unless explicitly instructed otherwise.",
		"",
	)

	if mode == PromptModeFull {
		if docsPath := detectDocsPath(input.Workspace); docsPath != "" {
			sections = append(sections,
				"## Documentation",
				"Local docs: "+docsPath,
				"Source: https://github.com/open-claw/open-claw",
				"For runtime behavior, configuration, or architecture questions, consult local docs first.",
				"",
			)
		}
	}

	sections = append(sections,
		"## Workspace Files (injected)",
		"Workspace bootstrap files are included below in Project Context.",
		"",
	)

	timezone := resolvePromptTimezone(input.UserTimezone)
	if timezone != "" {
		sections = append(sections,
			"## Current Date & Time",
			"Time zone: "+timezone,
			"",
		)
	}

	if mode == PromptModeFull && input.Heartbeat.Enabled {
		sections = append(sections,
			"## Reply Tags",
			"Use reply tags only when the active channel supports them.",
			"Preferred form: [[reply_to_current]] as the first token.",
			"",
			"## Heartbeats",
			"If a heartbeat poll arrives and nothing needs attention, reply exactly: HEARTBEAT_OK",
			"If something needs attention, reply with the alert and do not include HEARTBEAT_OK.",
			"",
		)
	}

	if strings.TrimSpace(input.ExtraContext) != "" {
		header := "## Group Chat Context"
		if mode == PromptModeMinimal {
			header = "## Subagent Context"
		}
		sections = append(sections, header, strings.TrimSpace(input.ExtraContext), "")
	}

	sections = append(sections,
		"## Runtime",
		buildRuntimeSection(input),
		"## Reasoning",
		buildReasoningLine(input.Model),
	)

	if len(input.ContextFiles) > 0 {
		sections = append(sections, "", "# Project Context", "", "The following project context files have been loaded:", "")
		for _, file := range input.ContextFiles {
			displayPath := file.Path
			if rel, err := filepath.Rel(input.Workspace, file.Path); err == nil && !strings.HasPrefix(rel, "..") {
				displayPath = rel
			}
			sections = append(sections, "## "+displayPath, "", strings.TrimSpace(file.Content), "")
		}
	}

	if input.IncludeWorking {
		blob, err := json.MarshalIndent(input.Working, "", "  ")
		if err != nil {
			return "", err
		}
		sections = append(sections,
			"## Working Memory (gopher extension)",
			"```json",
			string(blob),
			"```",
		)
	}

	return strings.Join(sections, "\n"), nil
}

func renderToolingSection(registry ToolRegistry) string {
	if registry == nil {
		return "- no tools enabled"
	}
	schemas := registry.Schemas()
	if len(schemas) == 0 {
		return "- no tools enabled"
	}
	lines := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		description := strings.TrimSpace(schema.Description)
		if description == "" {
			lines = append(lines, "- "+schema.Name)
			continue
		}
		lines = append(lines, "- "+schema.Name+": "+description)
	}
	return strings.Join(lines, "\n")
}

func buildRuntimeSection(input systemPromptInput) string {
	host := "unknown"
	if value, err := os.Hostname(); err == nil && strings.TrimSpace(value) != "" {
		host = strings.TrimSpace(value)
	}
	model := string(input.Model.Provider) + "/" + input.Model.ID
	if input.Model.ID == "" {
		model = "unknown"
	}

	repoRoot := detectRepoRoot(input.Workspace)
	parts := []string{
		"host=" + host,
		"os=" + runtime.GOOS + " (" + runtime.GOARCH + ")",
		"go=" + runtime.Version(),
		"model=" + model,
		"workspace=" + input.Workspace,
		"thinking=off",
	}
	if repoRoot != "" {
		parts = append(parts, "repo="+repoRoot)
	}
	return strings.Join(parts, " | ")
}

func buildReasoningLine(model ai.Model) string {
	if model.Reasoning {
		return "Reasoning is available for this model."
	}
	return "Reasoning is disabled for this model."
}

func resolvePromptTimezone(configured string) string {
	value := strings.TrimSpace(configured)
	if value != "" {
		if _, err := time.LoadLocation(value); err == nil {
			return value
		}
	}
	local := strings.TrimSpace(time.Now().Location().String())
	if local == "" || strings.EqualFold(local, "local") {
		return "UTC"
	}
	return local
}

func detectDocsPath(workspace string) string {
	for current := filepath.Clean(workspace); ; {
		candidate := filepath.Join(current, "docs")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func detectRepoRoot(workspace string) string {
	for current := filepath.Clean(workspace); ; {
		candidate := filepath.Join(current, ".git")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}
