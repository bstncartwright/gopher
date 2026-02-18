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
