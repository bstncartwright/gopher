package ai

import (
	"os"
	"strconv"
	"strings"
	"time"
)

var refreshOpenAICodexTokenForEnv = RefreshOpenAICodexToken
var refreshGitHubCopilotTokenForEnv = RefreshGitHubCopilotToken

func GetEnvAPIKey(provider string) string {
	switch Provider(provider) {
	case ProviderOpenAI:
		return os.Getenv("OPENAI_API_KEY")
	case ProviderZAI:
		return os.Getenv("ZAI_API_KEY")
	case ProviderKimiCoding:
		return os.Getenv("KIMI_API_KEY")
	case ProviderOpenAICodex:
		return resolveOpenAICodexEnvAPIKey()
	case ProviderGitHubCopilot:
		return resolveGitHubCopilotEnvAPIKey()
	case ProviderAnthropic:
		return os.Getenv("ANTHROPIC_API_KEY")
	case ProviderMinimax:
		return os.Getenv("MINIMAX_API_KEY")
	case ProviderOllama:
		if v := os.Getenv("OLLAMA_API_KEY"); v != "" {
			return v
		}
		return ""
	default:
		return ""
	}
}

func resolveOpenAICodexEnvAPIKey() string {
	// Respect explicit key overrides first.
	if v := strings.TrimSpace(os.Getenv("OPENAI_CODEX_API_KEY")); v != "" {
		return v
	}
	access := strings.TrimSpace(os.Getenv("OPENAI_CODEX_TOKEN"))
	refresh := strings.TrimSpace(os.Getenv("OPENAI_CODEX_REFRESH_TOKEN"))
	expiresRaw := strings.TrimSpace(os.Getenv("OPENAI_CODEX_TOKEN_EXPIRES"))

	expired := access == ""
	if expiresRaw != "" {
		if expiresAt, err := strconv.ParseInt(expiresRaw, 10, 64); err == nil {
			expired = time.Now().UnixMilli() >= expiresAt
		}
	}

	if refresh != "" && expired {
		refreshed, err := refreshOpenAICodexTokenForEnv(OAuthCredentials{
			Access:  access,
			Refresh: refresh,
		})
		if err == nil {
			if strings.TrimSpace(refreshed.Access) != "" {
				access = strings.TrimSpace(refreshed.Access)
				_ = os.Setenv("OPENAI_CODEX_TOKEN", access)
			}
			if strings.TrimSpace(refreshed.Refresh) != "" {
				refresh = strings.TrimSpace(refreshed.Refresh)
				_ = os.Setenv("OPENAI_CODEX_REFRESH_TOKEN", refresh)
			}
			if refreshed.Expires > 0 {
				_ = os.Setenv("OPENAI_CODEX_TOKEN_EXPIRES", strconv.FormatInt(refreshed.Expires, 10))
			}
		}
	}
	return access
}

func resolveGitHubCopilotEnvAPIKey() string {
	// Respect explicit key overrides first.
	if v := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_API_KEY")); v != "" {
		return v
	}

	access := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_TOKEN"))
	refresh := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_REFRESH_TOKEN"))
	expiresRaw := strings.TrimSpace(os.Getenv("GITHUB_COPILOT_TOKEN_EXPIRES"))

	expired := access == ""
	if expiresRaw != "" {
		if expiresAt, err := strconv.ParseInt(expiresRaw, 10, 64); err == nil {
			expired = time.Now().UnixMilli() >= expiresAt
		}
	}

	if refresh != "" && expired {
		refreshed, err := refreshGitHubCopilotTokenForEnv(OAuthCredentials{
			Access:  access,
			Refresh: refresh,
		})
		if err == nil {
			if strings.TrimSpace(refreshed.Access) != "" {
				access = strings.TrimSpace(refreshed.Access)
				_ = os.Setenv("GITHUB_COPILOT_TOKEN", access)
			}
			if strings.TrimSpace(refreshed.Refresh) != "" {
				refresh = strings.TrimSpace(refreshed.Refresh)
				_ = os.Setenv("GITHUB_COPILOT_REFRESH_TOKEN", refresh)
			}
			if refreshed.Expires > 0 {
				_ = os.Setenv("GITHUB_COPILOT_TOKEN_EXPIRES", strconv.FormatInt(refreshed.Expires, 10))
			}
		}
	}

	if strings.TrimSpace(access) != "" {
		return access
	}
	for _, key := range []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
