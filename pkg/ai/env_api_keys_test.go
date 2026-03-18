package ai

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestGetEnvAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ZAI_API_KEY", "zai-key")
	t.Setenv("KIMI_API_KEY", "kimi-key")
	t.Setenv("OPENAI_CODEX_API_KEY", "codex-key")
	t.Setenv("GITHUB_COPILOT_API_KEY", "copilot-key")
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
	if got := GetEnvAPIKey(string(ProviderGitHubCopilot)); got != "copilot-key" {
		t.Fatalf("expected copilot key, got %q", got)
	}
}

func TestGetEnvAPIKeyOpenAICodexRefreshesExpiredToken(t *testing.T) {
	restore := refreshOpenAICodexTokenForEnv
	t.Cleanup(func() {
		refreshOpenAICodexTokenForEnv = restore
	})

	t.Setenv("OPENAI_CODEX_TOKEN", "expired-token")
	t.Setenv("OPENAI_CODEX_REFRESH_TOKEN", "refresh-token")
	t.Setenv("OPENAI_CODEX_TOKEN_EXPIRES", strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10))

	refreshOpenAICodexTokenForEnv = func(credentials OAuthCredentials) (OAuthCredentials, error) {
		if credentials.Refresh != "refresh-token" {
			t.Fatalf("expected refresh token, got %q", credentials.Refresh)
		}
		return OAuthCredentials{
			Access:  "new-access",
			Refresh: "new-refresh",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		}, nil
	}

	if got := GetEnvAPIKey(string(ProviderOpenAICodex)); got != "new-access" {
		t.Fatalf("expected refreshed token, got %q", got)
	}
	if got := os.Getenv("OPENAI_CODEX_REFRESH_TOKEN"); got != "new-refresh" {
		t.Fatalf("expected refreshed refresh token, got %q", got)
	}
}

func TestGetEnvAPIKeyOpenAICodexRefreshesWhenTokenMissing(t *testing.T) {
	restore := refreshOpenAICodexTokenForEnv
	t.Cleanup(func() {
		refreshOpenAICodexTokenForEnv = restore
	})

	t.Setenv("OPENAI_CODEX_TOKEN", "")
	t.Setenv("OPENAI_CODEX_REFRESH_TOKEN", "refresh-token")

	refreshOpenAICodexTokenForEnv = func(credentials OAuthCredentials) (OAuthCredentials, error) {
		return OAuthCredentials{
			Access:  "new-access-2",
			Refresh: credentials.Refresh,
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		}, nil
	}

	if got := GetEnvAPIKey(string(ProviderOpenAICodex)); got != "new-access-2" {
		t.Fatalf("expected refreshed token, got %q", got)
	}
}

func TestGetEnvAPIKeyGitHubCopilotRefreshesExpiredToken(t *testing.T) {
	restore := refreshGitHubCopilotTokenForEnv
	t.Cleanup(func() {
		refreshGitHubCopilotTokenForEnv = restore
	})

	t.Setenv("GITHUB_COPILOT_TOKEN", "expired-token")
	t.Setenv("GITHUB_COPILOT_REFRESH_TOKEN", "refresh-token")
	t.Setenv("GITHUB_COPILOT_TOKEN_EXPIRES", strconv.FormatInt(time.Now().Add(-time.Minute).UnixMilli(), 10))

	refreshGitHubCopilotTokenForEnv = func(credentials OAuthCredentials) (OAuthCredentials, error) {
		if credentials.Refresh != "refresh-token" {
			t.Fatalf("expected refresh token, got %q", credentials.Refresh)
		}
		return OAuthCredentials{
			Access:  "copilot-access",
			Refresh: "copilot-refresh",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		}, nil
	}

	if got := GetEnvAPIKey(string(ProviderGitHubCopilot)); got != "copilot-access" {
		t.Fatalf("expected refreshed copilot token, got %q", got)
	}
	if got := os.Getenv("GITHUB_COPILOT_REFRESH_TOKEN"); got != "copilot-refresh" {
		t.Fatalf("expected refreshed copilot refresh token, got %q", got)
	}
}

func TestGetEnvAPIKeyGitHubCopilotRespectsFallbackPrecedence(t *testing.T) {
	t.Setenv("GITHUB_COPILOT_API_KEY", "")
	t.Setenv("GITHUB_COPILOT_TOKEN", "")
	t.Setenv("GITHUB_COPILOT_REFRESH_TOKEN", "")
	t.Setenv("COPILOT_GITHUB_TOKEN", "copilot-env")
	t.Setenv("GH_TOKEN", "gh-token")
	t.Setenv("GITHUB_TOKEN", "github-token")

	if got := GetEnvAPIKey(string(ProviderGitHubCopilot)); got != "copilot-env" {
		t.Fatalf("expected COPILOT_GITHUB_TOKEN precedence, got %q", got)
	}

	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	if got := GetEnvAPIKey(string(ProviderGitHubCopilot)); got != "gh-token" {
		t.Fatalf("expected GH_TOKEN fallback, got %q", got)
	}

	t.Setenv("GH_TOKEN", "")
	if got := GetEnvAPIKey(string(ProviderGitHubCopilot)); got != "github-token" {
		t.Fatalf("expected GITHUB_TOKEN fallback, got %q", got)
	}
}
