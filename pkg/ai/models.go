package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

var (
	modelRegistryMu sync.RWMutex
	modelRegistry   = map[Provider]map[string]Model{}
)

const openAICodexGPT54ContextWindow = 1_050_000

func init() {
	loadGeneratedModels()
}

func loadGeneratedModels() {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()

	if strings.TrimSpace(generatedModelsJSON) == "" {
		modelRegistry = map[Provider]map[string]Model{}
		return
	}

	decoded := map[string]map[string]Model{}
	if err := json.Unmarshal([]byte(generatedModelsJSON), &decoded); err != nil {
		fmt.Fprintf(os.Stderr, "ai: failed to load generated models JSON: %v\n", err)
		return
	}

	out := make(map[Provider]map[string]Model, len(decoded))
	for providerName, models := range decoded {
		provider := Provider(providerName)
		providerModels := make(map[string]Model, len(models))
		for id, model := range models {
			if model.Provider == "" {
				model.Provider = provider
			}
			model = applyDefaultResponsesCompat(model)
			providerModels[id] = model
		}
		out[provider] = providerModels
	}

	// Curated local Ollama entry.
	if _, ok := out[ProviderOllama]; !ok {
		out[ProviderOllama] = map[string]Model{}
	}
	out[ProviderOllama]["gpt-oss:20b"] = Model{
		ID:        "gpt-oss:20b",
		Name:      "Ollama GPT-OSS 20B",
		API:       APIOpenAICompletions,
		Provider:  ProviderOllama,
		BaseURL:   "http://localhost:11434/v1",
		Reasoning: true,
		Input:     []string{"text"},
		Cost: ModelCost{
			Input:      0,
			Output:     0,
			CacheRead:  0,
			CacheWrite: 0,
		},
		ContextWindow: 128000,
		MaxTokens:     16000,
		Compat: &OpenAICompletionsCompat{
			SupportsStore:           boolPtr(false),
			SupportsDeveloperRole:   boolPtr(false),
			SupportsReasoningEffort: boolPtr(false),
			MaxTokensField:          "max_tokens",
			ThinkingFormat:          "openai",
		},
	}

	// Patch GPT-5.4 Codex metadata until the generated catalog carries the
	// current Codex context window instead of the older fallback limit.
	if _, ok := out[ProviderOpenAICodex]; !ok {
		out[ProviderOpenAICodex] = map[string]Model{}
	}
	gpt54 := out[ProviderOpenAICodex]["gpt-5.4"]
	if gpt54.ID == "" {
		gpt54 = Model{
			ID:        "gpt-5.4",
			Name:      "GPT-5.4",
			API:       APIOpenAICodexResponse,
			Provider:  ProviderOpenAICodex,
			BaseURL:   "https://chatgpt.com/backend-api",
			Reasoning: true,
			Input:     []string{"text", "image"},
			Cost: ModelCost{
				Input:      2.5,
				Output:     15,
				CacheRead:  0.25,
				CacheWrite: 0,
			},
			MaxTokens: 128000,
		}
	}
	gpt54.ContextWindow = openAICodexGPT54ContextWindow
	if gpt54.MaxTokens <= 0 {
		gpt54.MaxTokens = 128000
	}
	gpt54 = applyDefaultResponsesCompat(gpt54)
	out[ProviderOpenAICodex]["gpt-5.4"] = gpt54

	mergeGitHubCopilotModels(out)
	mergeMinimaxModels(out)

	modelRegistry = out
}

func SetModels(provider Provider, models map[string]Model) {
	modelRegistryMu.Lock()
	defer modelRegistryMu.Unlock()
	if modelRegistry == nil {
		modelRegistry = map[Provider]map[string]Model{}
	}
	cp := make(map[string]Model, len(models))
	for id, model := range models {
		model = applyDefaultResponsesCompat(model)
		cp[id] = model
	}
	modelRegistry[provider] = cp
}

func applyDefaultResponsesCompat(model Model) Model {
	if model.API != APIOpenAIResponses && model.API != APIOpenAICodexResponse {
		return model
	}
	if model.Provider != ProviderOpenAI && model.Provider != ProviderOpenAICodex {
		return model
	}
	if model.ResponsesCompat == nil {
		model.ResponsesCompat = &OpenAIResponsesCompat{}
	}
	if model.ResponsesCompat.SupportsHostedWebSearch == nil {
		model.ResponsesCompat.SupportsHostedWebSearch = boolPtr(true)
	}
	return model
}

func mergeGitHubCopilotModels(out map[Provider]map[string]Model) {
	if _, ok := out[ProviderGitHubCopilot]; !ok {
		out[ProviderGitHubCopilot] = map[string]Model{}
	}

	headers := defaultGitHubCopilotHeaders()
	addModel := func(model Model) {
		model.Headers = cloneStringMap(headers)
		if existing, ok := out[ProviderGitHubCopilot][model.ID]; ok && existing.ContextWindow > 0 {
			model.ContextWindow = existing.ContextWindow
		}
		out[ProviderGitHubCopilot][model.ID] = model
	}

	addModel(Model{
		ID:            "gpt-4o",
		Name:          "GPT-4o",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Reasoning:     false,
		Input:         []string{"text", "image"},
		Cost:          ModelCost{},
		ContextWindow: 128000,
		MaxTokens:     16384,
		Compat:        githubCopilotCompat(false),
	})
	addModel(Model{
		ID:            "gpt-4.1",
		Name:          "GPT-4.1",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Reasoning:     false,
		Input:         []string{"text", "image"},
		Cost:          ModelCost{},
		ContextWindow: 1047576,
		MaxTokens:     32768,
		Compat:        githubCopilotCompat(false),
	})
	addModel(Model{
		ID:            "gpt-5.3-codex",
		Name:          "GPT-5.3 Codex",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Reasoning:     true,
		Input:         []string{"text", "image"},
		Cost:          ModelCost{},
		ContextWindow: 400000,
		MaxTokens:     128000,
		Compat:        githubCopilotCompat(true),
	})
	addModel(Model{
		ID:            "gpt-5.4",
		Name:          "GPT-5.4",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Reasoning:     true,
		Input:         []string{"text", "image"},
		Cost:          ModelCost{},
		ContextWindow: 400000,
		MaxTokens:     128000,
		Compat:        githubCopilotCompat(true),
	})
	addModel(Model{
		ID:            "gpt-5.4-mini",
		Name:          "GPT-5.4 mini",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Reasoning:     true,
		Input:         []string{"text", "image"},
		Cost:          ModelCost{},
		ContextWindow: 400000,
		MaxTokens:     128000,
		Compat:        githubCopilotCompat(true),
	})
	addModel(Model{
		ID:            "gemini-2.5-pro",
		Name:          "Gemini 2.5 Pro",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 1000000,
		MaxTokens:     32000,
		Compat:        githubCopilotCompat(true),
	})
	addModel(Model{
		ID:            "grok-code-fast-1",
		Name:          "Grok Code Fast 1",
		API:           APIOpenAICompletions,
		Provider:      ProviderGitHubCopilot,
		BaseURL:       defaultGitHubCopilotBaseURL,
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ModelCost{},
		ContextWindow: 128000,
		MaxTokens:     64000,
		Compat: &OpenAICompletionsCompat{
			SupportsStore:           boolPtr(false),
			SupportsDeveloperRole:   boolPtr(false),
			SupportsReasoningEffort: boolPtr(false),
			ThinkingFormat:          "openai",
		},
	})
}

func mergeMinimaxModels(out map[Provider]map[string]Model) {
	if _, ok := out[ProviderMinimax]; !ok {
		out[ProviderMinimax] = map[string]Model{}
	}

	baseURL := "https://api.minimax.io/anthropic"
	addModel := func(id, name string, reasoning bool) {
		out[ProviderMinimax][id] = Model{
			ID:            id,
			Name:          name,
			API:           APIAnthropicMessages,
			Provider:      ProviderMinimax,
			BaseURL:       baseURL,
			Reasoning:     reasoning,
			Input:         []string{"text"},
			Cost:          ModelCost{},
			ContextWindow: 204800,
			MaxTokens:     8192,
		}
	}

	addModel("MiniMax-M2.7", "MiniMax M2.7", true)
	addModel("MiniMax-M2.7-highspeed", "MiniMax M2.7 Highspeed", true)
	addModel("MiniMax-M2.5", "MiniMax M2.5", true)
	addModel("MiniMax-M2.5-highspeed", "MiniMax M2.5 Highspeed", true)
	addModel("MiniMax-M2.1", "MiniMax M2.1", true)
	addModel("MiniMax-M2.1-highspeed", "MiniMax M2.1 Highspeed", true)
	addModel("MiniMax-M2", "MiniMax M2", true)
}

func githubCopilotCompat(reasoning bool) *OpenAICompletionsCompat {
	compat := &OpenAICompletionsCompat{
		SupportsStore:         boolPtr(false),
		SupportsDeveloperRole: boolPtr(false),
		ThinkingFormat:        "openai",
	}
	if reasoning {
		compat.SupportsReasoningEffort = boolPtr(true)
	}
	return compat
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func GetModel(provider, modelID string) (Model, bool) {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	providerModels, ok := modelRegistry[Provider(provider)]
	if !ok {
		return Model{}, false
	}
	model, ok := providerModels[modelID]
	return model, ok
}

func ResolveModelPolicy(raw string) (Model, error) {
	policy := strings.TrimSpace(raw)
	parts := strings.SplitN(policy, ":", 2)
	if len(parts) != 2 {
		return Model{}, fmt.Errorf("invalid model_policy %q: expected provider:model", raw)
	}

	providerName := strings.TrimSpace(parts[0])
	modelID := strings.TrimSpace(parts[1])
	if providerName == "" || modelID == "" {
		return Model{}, fmt.Errorf("invalid model_policy %q: provider and model are required", raw)
	}

	model, ok := GetModel(providerName, modelID)
	if !ok {
		return Model{}, fmt.Errorf("model not found for model_policy %q", raw)
	}
	return model, nil
}

func GetModels(provider string) []Model {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	providerModels, ok := modelRegistry[Provider(provider)]
	if !ok {
		return nil
	}
	out := make([]Model, 0, len(providerModels))
	for _, model := range providerModels {
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func GetProviders() []Provider {
	modelRegistryMu.RLock()
	defer modelRegistryMu.RUnlock()
	out := make([]Provider, 0, len(modelRegistry))
	for provider := range modelRegistry {
		out = append(out, provider)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

func CalculateCost(model Model, usage *Usage) CostBreakdown {
	if usage == nil {
		return CostBreakdown{}
	}
	cost := CostBreakdown{
		Input:      (model.Cost.Input / 1_000_000.0) * float64(usage.Input),
		Output:     (model.Cost.Output / 1_000_000.0) * float64(usage.Output),
		CacheRead:  (model.Cost.CacheRead / 1_000_000.0) * float64(usage.CacheRead),
		CacheWrite: (model.Cost.CacheWrite / 1_000_000.0) * float64(usage.CacheWrite),
	}
	cost.Total = cost.Input + cost.Output + cost.CacheRead + cost.CacheWrite
	return cost
}

func SupportsXHigh(model Model) bool {
	if strings.Contains(model.ID, "gpt-5.2") || strings.Contains(model.ID, "gpt-5.3") || strings.Contains(model.ID, "gpt-5.4") {
		return true
	}
	if model.API == APIAnthropicMessages {
		return strings.Contains(model.ID, "opus-4-6") || strings.Contains(model.ID, "opus-4.6")
	}
	return false
}

func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

func boolPtr(v bool) *bool {
	return &v
}
