package main

import (
	"strings"
	"testing"
)

func TestDefaultIdentityTemplateUsesUnconfiguredScaffold(t *testing.T) {
	template := defaultIdentityTemplate()

	mustContain := []string{
		"# IDENTITY.md - Who Am I?",
		"- **Name:**",
		"- **Creature:**",
		"- **Vibe:**",
		"- **Emoji:**",
		"- **Avatar:**",
		"- Save this file at the workspace root as `IDENTITY.md`.",
		"- For avatars, use a workspace-relative path like `avatars/gopher.png`.",
	}
	for _, needle := range mustContain {
		if !strings.Contains(template, needle) {
			t.Fatalf("default identity template missing %q", needle)
		}
	}

	mustNotContain := []string{
		"Name: Main",
		"Role: Practical AI coding and systems assistant",
	}
	for _, needle := range mustNotContain {
		if strings.Contains(template, needle) {
			t.Fatalf("default identity template unexpectedly contains %q", needle)
		}
	}
}

func TestDefaultSoulTemplateSetsPeerVoicePresence(t *testing.T) {
	template := defaultSoulTemplate()

	required := []string{
		"# SOUL.md - Who You Are",
		"_You're not a chatbot. You're becoming someone._",
		"## Core Truths",
		"**Have opinions.**",
		"## Boundaries",
		"If you change this file, tell the user — it's your soul, and they should know.",
	}
	for _, needle := range required {
		if !strings.Contains(template, needle) {
			t.Fatalf("default soul template missing %q", needle)
		}
	}
}

func TestDefaultBootstrapTemplateEncouragesNaturalTaskFirstOnboarding(t *testing.T) {
	template := defaultBootstrapTemplate()

	required := []string{
		"# BOOTSTRAP.md - Hello, World",
		"_You just woke up. Time to figure out who you are._",
		"Hey. I just came online. Who am I? Who are you?",
		"## Connect (Optional)",
		"**WhatsApp** — link their personal account (you'll show a QR code)",
	}
	for _, needle := range required {
		if !strings.Contains(template, needle) {
			t.Fatalf("default bootstrap template missing %q", needle)
		}
	}
}
