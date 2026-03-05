package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestTransformOpenAICodexModelsMirrorsOpenAIModels(t *testing.T) {
	models := transformOpenAICodexModels(modelsDevProvider{
		ID: "openai",
		Models: map[string]modelsDevModel{
			"gpt-5.3-codex": {
				ID:        "gpt-5.3-codex",
				Name:      "GPT-5.3 Codex",
				Reasoning: true,
				Modalities: modelsDevModalities{
					Input: []string{"text", "image", "pdf"},
				},
				Cost:  modelsDevCost{Input: 1.75, Output: 14, CacheRead: 0.175},
				Limit: modelsDevLimit{Context: 400000, Input: 272000, Output: 128000},
			},
			"gpt-5": {
				ID:        "gpt-5",
				Name:      "GPT-5",
				Reasoning: true,
				Modalities: modelsDevModalities{
					Input: []string{"text"},
				},
				Limit: modelsDevLimit{Context: 400000, Output: 128000},
			},
		},
	})

	if len(models) != 2 {
		t.Fatalf("derived codex models = %d, want 2", len(models))
	}
	model, ok := models["gpt-5.3-codex"]
	if !ok {
		t.Fatalf("expected gpt-5.3-codex to be derived")
	}
	if model.Provider != ai.ProviderOpenAICodex {
		t.Fatalf("provider = %q, want %q", model.Provider, ai.ProviderOpenAICodex)
	}
	if model.API != ai.APIOpenAICodexResponse {
		t.Fatalf("api = %q, want %q", model.API, ai.APIOpenAICodexResponse)
	}
	if model.BaseURL != "https://chatgpt.com/backend-api" {
		t.Fatalf("baseURL = %q", model.BaseURL)
	}
	if model.ContextWindow != 272000 {
		t.Fatalf("contextWindow = %d, want 272000", model.ContextWindow)
	}
	if _, ok := models["gpt-5"]; !ok {
		t.Fatalf("expected non-codex openai model to be mirrored into openai-codex")
	}
}

func TestTransformModelsDevProviderMapsZAICompat(t *testing.T) {
	models := transformModelsDevProvider("zai", modelsDevProvider{
		ID:  "zai-coding-plan",
		API: "https://api.z.ai/api/coding/paas/v4",
		Models: map[string]modelsDevModel{
			"glm-5": {
				ID:        "glm-5",
				Name:      "GLM-5",
				Reasoning: true,
				Modalities: modelsDevModalities{
					Input: []string{"text"},
				},
				Cost:  modelsDevCost{},
				Limit: modelsDevLimit{Context: 204800, Output: 131072},
			},
		},
	}, ai.APIOpenAICompletions, "https://api.z.ai/api/coding/paas/v4", zaiCompat())

	model := models["glm-5"]
	if model.Provider != ai.ProviderZAI {
		t.Fatalf("provider = %q, want %q", model.Provider, ai.ProviderZAI)
	}
	if model.API != ai.APIOpenAICompletions {
		t.Fatalf("api = %q, want %q", model.API, ai.APIOpenAICompletions)
	}
	if model.Compat == nil || model.Compat.SupportsDeveloperRole == nil || *model.Compat.SupportsDeveloperRole {
		t.Fatalf("expected zai compat to disable developer role")
	}
	if model.Compat.ThinkingFormat != "zai" {
		t.Fatalf("thinkingFormat = %q, want zai", model.Compat.ThinkingFormat)
	}
}

func TestBuildGopherCatalogMapsExpectedProviders(t *testing.T) {
	catalog := buildGopherCatalog(map[string]modelsDevProvider{
		"openai": {
			ID: "openai",
			Models: map[string]modelsDevModel{
				"gpt-4o-mini":   {ID: "gpt-4o-mini", Name: "GPT-4o mini", Modalities: modelsDevModalities{Input: []string{"text"}}, Limit: modelsDevLimit{Context: 128000, Output: 16384}},
				"gpt-5.3-codex": {ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Reasoning: true, Modalities: modelsDevModalities{Input: []string{"text"}}, Limit: modelsDevLimit{Input: 272000, Output: 128000}},
			},
		},
		"kimi-for-coding": {
			ID:  "kimi-for-coding",
			API: "https://api.kimi.com/coding/v1",
			Models: map[string]modelsDevModel{
				"k2p5": {ID: "k2p5", Name: "K2.5", Reasoning: true, Modalities: modelsDevModalities{Input: []string{"text"}}, Limit: modelsDevLimit{Context: 262144, Output: 32768}},
			},
		},
		"zai-coding-plan": {
			ID:  "zai-coding-plan",
			API: "https://api.z.ai/api/coding/paas/v4",
			Models: map[string]modelsDevModel{
				"glm-5": {ID: "glm-5", Name: "GLM-5", Reasoning: true, Modalities: modelsDevModalities{Input: []string{"text"}}, Limit: modelsDevLimit{Context: 204800, Output: 131072}},
			},
		},
	})

	if _, ok := catalog["openai"]["gpt-4o-mini"]; !ok {
		t.Fatalf("expected openai:gpt-4o-mini in catalog")
	}
	if _, ok := catalog["openai-codex"]["gpt-5.3-codex"]; !ok {
		t.Fatalf("expected derived openai-codex:gpt-5.3-codex in catalog")
	}
	if _, ok := catalog["kimi-coding"]["k2p5"]; !ok {
		t.Fatalf("expected kimi-coding:k2p5 in catalog")
	}
	if _, ok := catalog["zai"]["glm-5"]; !ok {
		t.Fatalf("expected zai:glm-5 in catalog")
	}
}

func TestBuildGopherCatalogSkipsMissingProviders(t *testing.T) {
	catalog := buildGopherCatalog(map[string]modelsDevProvider{})
	if len(catalog) != 0 {
		t.Fatalf("catalog len = %d, want 0", len(catalog))
	}
}

func TestFetchModelsDevProvidersRejectsBadStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	_, err := fetchModelsDevProviders(server.Client(), server.URL)
	if err == nil {
		t.Fatalf("expected error for non-200 status")
	}
	if !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("unexpected error: %v", err)
	}
}
