package agentcore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAgentDiscoversSkillsFromDefaultRoot(t *testing.T) {
	isolateSkillEnv(t)
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	writeSkillFile(t, filepath.Join(workspace, ".agents", "skills", "accessibility", "SKILL.md"), `
---
name: fixing-accessibility
description: Fix accessibility issues.
---
# Accessibility

Use semantic markup and keyboard support.
`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	if len(agent.skills) != 1 {
		t.Fatalf("expected 1 discovered skill, got %d", len(agent.skills))
	}
	if agent.skills[0].Name != "fixing-accessibility" {
		t.Fatalf("unexpected skill name: %q", agent.skills[0].Name)
	}
	if !strings.HasSuffix(agent.skills[0].Location, "/.agents/skills/accessibility/SKILL.md") {
		t.Fatalf("unexpected skill location: %q", agent.skills[0].Location)
	}
	if !strings.Contains(agent.skills[0].Instruction, "semantic markup") {
		t.Fatalf("expected parsed instruction body, got: %q", agent.skills[0].Instruction)
	}
}

func TestLoadAgentUsesConfiguredSkillPaths(t *testing.T) {
	isolateSkillEnv(t)
	config := defaultConfig()
	config.SkillsPaths = []string{"custom/skills"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	writeSkillFile(t, filepath.Join(workspace, "custom", "skills", "metadata", "SKILL.md"), `
---
name: fixing-metadata
description: Ship complete metadata.
---
Check title and meta tags.
`)
	writeSkillFile(t, filepath.Join(workspace, ".agents", "skills", "ignored", "SKILL.md"), `
---
name: ignored-skill
description: Should not load when custom paths are set.
---
Ignore me.
`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	if len(agent.skills) != 1 {
		t.Fatalf("expected 1 skill from configured path, got %d", len(agent.skills))
	}
	if agent.skills[0].Name != "fixing-metadata" {
		t.Fatalf("unexpected skill loaded: %q", agent.skills[0].Name)
	}
}

func TestLoadAgentSkipsInvalidSkillFiles(t *testing.T) {
	isolateSkillEnv(t)
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	writeSkillFile(t, filepath.Join(workspace, ".agents", "skills", "bad", "SKILL.md"), `
# Missing frontmatter
Do the thing.
`)
	writeSkillFile(t, filepath.Join(workspace, ".agents", "skills", "good", "SKILL.md"), `
---
name: good-skill
description: Valid metadata.
---
Valid instructions.
`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if len(agent.skills) != 1 {
		t.Fatalf("expected invalid skill to be skipped, got %d skills", len(agent.skills))
	}
	if agent.skills[0].Name != "good-skill" {
		t.Fatalf("expected good-skill, got %q", agent.skills[0].Name)
	}
}

func TestBuildProviderContextUsesPiStyleSkillPromptAndExplicitInvocation(t *testing.T) {
	isolateSkillEnv(t)
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	writeSkillFile(t, filepath.Join(workspace, ".agents", "skills", "a11y", "SKILL.md"), `
---
name: fixing-accessibility
description: Fix accessibility issues in UI code.
---
# Accessibility Checklist

- Ensure labels match controls.
`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	session := &Session{ID: "s-skills"}

	ctx, err := agent.buildProviderContext(context.Background(), session, "Please use $fixing-accessibility on this form.")
	if err != nil {
		t.Fatalf("buildProviderContext() error: %v", err)
	}
	if !strings.Contains(ctx.SystemPrompt, "<available_skills>") {
		t.Fatalf("expected skills XML section, got: %s", ctx.SystemPrompt)
	}
	if !strings.Contains(ctx.SystemPrompt, "<name>fixing-accessibility</name>") {
		t.Fatalf("expected listed skill metadata, got: %s", ctx.SystemPrompt)
	}
	if strings.Contains(ctx.SystemPrompt, "# Accessibility Checklist") {
		t.Fatalf("did not expect skill body in system prompt without explicit invocation, got: %s", ctx.SystemPrompt)
	}
	if len(ctx.Messages) == 0 {
		t.Fatalf("expected at least one message")
	}
	lastMessage, ok := ctx.Messages[len(ctx.Messages)-1].Content.(string)
	if !ok {
		t.Fatalf("expected final user message content to be string")
	}
	if strings.Contains(lastMessage, "<skill name=") {
		t.Fatalf("did not expect skill command expansion for normal prompt, got: %s", lastMessage)
	}

	ctxExplicit, err := agent.buildProviderContext(context.Background(), session, "/skill:fixing-accessibility review this form")
	if err != nil {
		t.Fatalf("buildProviderContext(explicit skill command) error: %v", err)
	}
	if len(ctxExplicit.Messages) == 0 {
		t.Fatalf("expected messages in explicit context")
	}
	expanded, ok := ctxExplicit.Messages[len(ctxExplicit.Messages)-1].Content.(string)
	if !ok {
		t.Fatalf("expected explicit user content to be string")
	}
	if !strings.Contains(expanded, `<skill name="fixing-accessibility"`) {
		t.Fatalf("expected /skill command expansion, got: %s", expanded)
	}
	if !strings.Contains(expanded, "# Accessibility Checklist") {
		t.Fatalf("expected expanded skill body, got: %s", expanded)
	}
}

func TestBuildProviderContextOmitsDisabledSkillsFromPrompt(t *testing.T) {
	isolateSkillEnv(t)
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	writeSkillFile(t, filepath.Join(workspace, ".agents", "skills", "hidden", "SKILL.md"), `
---
name: hidden-skill
description: Hidden from model invocation.
disable-model-invocation: true
---
# Hidden Skill
`)
	writeSkillFile(t, filepath.Join(workspace, ".agents", "skills", "visible", "SKILL.md"), `
---
name: visible-skill
description: Visible in model prompt.
---
# Visible Skill
`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	session := &Session{ID: "s-hidden"}

	ctx, err := agent.buildProviderContext(context.Background(), session, "hello")
	if err != nil {
		t.Fatalf("buildProviderContext() error: %v", err)
	}
	if !strings.Contains(ctx.SystemPrompt, "<name>visible-skill</name>") {
		t.Fatalf("expected visible skill in prompt, got: %s", ctx.SystemPrompt)
	}
	if strings.Contains(ctx.SystemPrompt, "<name>hidden-skill</name>") {
		t.Fatalf("did not expect hidden skill in prompt, got: %s", ctx.SystemPrompt)
	}
}

func writeSkillFile(t *testing.T, path string, content string) {
	t.Helper()
	mustWriteFile(t, path, strings.TrimSpace(content)+"\n")
}

func isolateSkillEnv(t *testing.T) {
	t.Helper()
	t.Setenv(agentSkillsPathEnv, "")
	t.Setenv("HOME", t.TempDir())
}
