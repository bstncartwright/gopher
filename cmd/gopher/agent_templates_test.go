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
