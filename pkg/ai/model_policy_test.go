package ai

import (
	"strings"
	"testing"
)

func TestResolveModelPolicyValidBuiltInModel(t *testing.T) {
	model, err := ResolveModelPolicy("openai:gpt-4o-mini")
	if err != nil {
		t.Fatalf("ResolveModelPolicy() error: %v", err)
	}
	if model.Provider != ProviderOpenAI {
		t.Fatalf("provider = %q, want %q", model.Provider, ProviderOpenAI)
	}
	if model.ID != "gpt-4o-mini" {
		t.Fatalf("model ID = %q, want gpt-4o-mini", model.ID)
	}
}

func TestResolveModelPolicyValidCuratedOpenAICodexOverride(t *testing.T) {
	model, err := ResolveModelPolicy("openai-codex:gpt-5.4")
	if err != nil {
		t.Fatalf("ResolveModelPolicy() error: %v", err)
	}
	if model.Provider != ProviderOpenAICodex {
		t.Fatalf("provider = %q, want %q", model.Provider, ProviderOpenAICodex)
	}
	if model.ID != "gpt-5.4" {
		t.Fatalf("model ID = %q, want gpt-5.4", model.ID)
	}
}

func TestResolveModelPolicyRejectsInvalidFormat(t *testing.T) {
	_, err := ResolveModelPolicy("gpt-4o-mini")
	if err == nil {
		t.Fatalf("expected invalid model_policy error")
	}
	if !strings.Contains(err.Error(), `invalid model_policy "gpt-4o-mini": expected provider:model`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveModelPolicyRejectsEmptyProviderOrModel(t *testing.T) {
	_, err := ResolveModelPolicy("openai:")
	if err == nil {
		t.Fatalf("expected invalid model_policy error")
	}
	if !strings.Contains(err.Error(), `invalid model_policy "openai:": provider and model are required`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveModelPolicyAllowsColonInModelID(t *testing.T) {
	original := modelsToMap(GetModels(string(ProviderOllama)))
	updated := modelsToMap(GetModels(string(ProviderOllama)))
	updated["qwen3:0.6b"] = Model{
		ID:            "qwen3:0.6b",
		Name:          "Qwen3 0.6B",
		API:           APIOpenAICompletions,
		Provider:      ProviderOllama,
		BaseURL:       "http://localhost:11434/v1",
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 32768,
		MaxTokens:     8192,
	}
	SetModels(ProviderOllama, updated)
	defer SetModels(ProviderOllama, original)

	model, err := ResolveModelPolicy("ollama:qwen3:0.6b")
	if err != nil {
		t.Fatalf("ResolveModelPolicy() error: %v", err)
	}
	if model.ID != "qwen3:0.6b" {
		t.Fatalf("model ID = %q, want qwen3:0.6b", model.ID)
	}
}

func TestResolveModelPolicyRejectsUnknownModel(t *testing.T) {
	_, err := ResolveModelPolicy("openai:not-a-real-model")
	if err == nil {
		t.Fatalf("expected missing model error")
	}
	if !strings.Contains(err.Error(), `model not found for model_policy "openai:not-a-real-model"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func modelsToMap(models []Model) map[string]Model {
	out := make(map[string]Model, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out
}
