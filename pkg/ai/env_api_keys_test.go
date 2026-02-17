package ai

import (
	"testing"
)

func TestGetEnvAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ZAI_API_KEY", "zai-key")
	t.Setenv("KIMI_API_KEY", "kimi-key")
	t.Setenv("OPENAI_CODEX_API_KEY", "codex-key")
	t.Setenv("OLLAMA_API_KEY", "ollama-key")

	if got := GetEnvAPIKey(string(ProviderOpenAI)); got != "openai-key" {
		t.Fatalf("expected openai key, got %q", got)
	}
	if got := GetEnvAPIKey(string(ProviderZAI)); got != "zai-key" {
		t.Fatalf("expected zai key, got %q", got)
	}
	if got := GetEnvAPIKey(string(ProviderKimiCoding)); got != "kimi-key" {
		t.Fatalf("expected kimi key, got %q", got)
	}
	if got := GetEnvAPIKey(string(ProviderOpenAICodex)); got != "codex-key" {
		t.Fatalf("expected codex key, got %q", got)
	}
}
