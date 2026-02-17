package ai

import "os"

func GetEnvAPIKey(provider string) string {
	switch Provider(provider) {
	case ProviderOpenAI:
		return os.Getenv("OPENAI_API_KEY")
	case ProviderZAI:
		return os.Getenv("ZAI_API_KEY")
	case ProviderKimiCoding:
		return os.Getenv("KIMI_API_KEY")
	case ProviderOpenAICodex:
		// openai-codex is OAuth-first; allow explicit env fallback when present.
		if v := os.Getenv("OPENAI_CODEX_API_KEY"); v != "" {
			return v
		}
		if v := os.Getenv("OPENAI_CODEX_TOKEN"); v != "" {
			return v
		}
		return ""
	case ProviderAnthropic:
		return os.Getenv("ANTHROPIC_API_KEY")
	case ProviderOllama:
		if v := os.Getenv("OLLAMA_API_KEY"); v != "" {
			return v
		}
		return ""
	default:
		return ""
	}
}
