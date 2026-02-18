package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/agentcore"
)

func TestParseIdentityProfile(t *testing.T) {
	profile := parseIdentityProfile(`
# IDENTITY
- Name: River
- Role: planner
- Style/Vibe: playful
- Avatar Personality: pirate captain
- Avatar (optional path/URL/data URI): ./avatar.png
`)
	if profile.Name != "River" {
		t.Fatalf("name = %q, want River", profile.Name)
	}
	if profile.Role != "planner" {
		t.Fatalf("role = %q, want planner", profile.Role)
	}
	if profile.StyleVibe != "playful" {
		t.Fatalf("style = %q, want playful", profile.StyleVibe)
	}
	if profile.AvatarPersonality != "pirate captain" {
		t.Fatalf("avatar personality = %q, want pirate captain", profile.AvatarPersonality)
	}
	if profile.Avatar != "./avatar.png" {
		t.Fatalf("avatar = %q, want ./avatar.png", profile.Avatar)
	}
}

func TestUpsertIdentityAvatarReplacesExistingLine(t *testing.T) {
	updated := upsertIdentityAvatar("- Avatar (optional path/URL/data URI):\n- Name: test\n", "./avatar.png")
	if !strings.Contains(updated, "- Avatar (optional path/URL/data URI): ./avatar.png") {
		t.Fatalf("updated content missing avatar line: %q", updated)
	}
}

func TestBuildAgentAvatarPromptUsesStyleVibe(t *testing.T) {
	agent := &agentcore.Agent{ID: "writer", Name: "Writer", Role: "assistant"}
	prompt := buildAgentAvatarPrompt(agent, identityProfile{StyleVibe: "calm and analytical"})
	if !strings.Contains(prompt, "personality variant: calm and analytical") {
		t.Fatalf("prompt personality variant mismatch: %q", prompt)
	}
}

func TestBuildAgentAvatarPromptPrefersAvatarPersonality(t *testing.T) {
	agent := &agentcore.Agent{ID: "writer", Name: "Writer", Role: "assistant"}
	prompt := buildAgentAvatarPrompt(agent, identityProfile{
		StyleVibe:         "calm and analytical",
		AvatarPersonality: "chaotic wizard",
	})
	if !strings.Contains(prompt, "personality variant: chaotic wizard") {
		t.Fatalf("prompt personality variant mismatch: %q", prompt)
	}
}

func TestEnsureAgentAvatarUsesExistingIdentityAvatarFile(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	dir := t.TempDir()
	avatarPath := filepath.Join(dir, "avatar.png")
	if err := os.WriteFile(avatarPath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644); err != nil {
		t.Fatalf("write avatar file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("- Avatar: ./avatar.png\n"), 0o644); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	agent := &agentcore.Agent{ID: "writer", Workspace: dir}
	avatar, err := ensureAgentAvatar(context.Background(), agent)
	if err != nil {
		t.Fatalf("ensureAgentAvatar error: %v", err)
	}
	if len(avatar.Data) == 0 {
		t.Fatalf("expected avatar data")
	}
}

func TestEnsureAgentAvatarNoAvatarAndNoAPIKeyReturnsEmpty(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "IDENTITY.md"), []byte("- Name: test\n"), 0o644); err != nil {
		t.Fatalf("write identity file: %v", err)
	}
	agent := &agentcore.Agent{ID: "writer", Workspace: dir}
	avatar, err := ensureAgentAvatar(context.Background(), agent)
	if err != nil {
		t.Fatalf("ensureAgentAvatar error: %v", err)
	}
	if avatar.MXCURL != "" || len(avatar.Data) != 0 {
		t.Fatalf("expected empty avatar, got %+v", avatar)
	}
}

func TestAvatarGenerationModelOverride(t *testing.T) {
	t.Setenv("GOPHER_AGENT_AVATAR_MODEL", "gpt-image-1")
	if got := avatarGenerationModel(); got != "gpt-image-1" {
		t.Fatalf("avatarGenerationModel() = %q, want gpt-image-1", got)
	}
}
