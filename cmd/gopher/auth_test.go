package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestAuthSetListUnsetProvider(t *testing.T) {
	t.Parallel()

	envPath := filepath.Join(t.TempDir(), "gopher.env")
	var out bytes.Buffer

	if err := runAuthSubcommand([]string{
		"set",
		"--env-file", envPath,
		"--provider", "zai",
		"--api-key", "secret-123",
	}, &out, &out); err != nil {
		t.Fatalf("set provider failed: %v", err)
	}

	var listed bytes.Buffer
	if err := runAuthSubcommand([]string{
		"list",
		"--env-file", envPath,
	}, &listed, &listed); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(listed.String(), "zai: configured") {
		t.Fatalf("expected zai configured in list output, got: %s", listed.String())
	}

	if err := runAuthSubcommand([]string{
		"unset",
		"--env-file", envPath,
		"--provider", "zai",
	}, &out, &out); err != nil {
		t.Fatalf("unset provider failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	if strings.Contains(string(data), "ZAI_API_KEY=") {
		t.Fatalf("expected ZAI_API_KEY to be removed, got: %s", string(data))
	}
}

func TestAuthSetRawKey(t *testing.T) {
	t.Parallel()

	envPath := filepath.Join(t.TempDir(), "gopher.env")
	var out bytes.Buffer
	if err := runAuthSubcommand([]string{
		"set",
		"--env-file", envPath,
		"--key", "CUSTOM_TOKEN",
		"--value", "abc",
	}, &out, &out); err != nil {
		t.Fatalf("set raw key failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	if !strings.Contains(string(data), "CUSTOM_TOKEN=abc") {
		t.Fatalf("expected CUSTOM_TOKEN to be set, got: %s", string(data))
	}
}

func TestAuthLoginOpenAICodex(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "gopher.env")
	restore := loginOpenAICodexForAuth
	t.Cleanup(func() {
		loginOpenAICodexForAuth = restore
	})
	loginOpenAICodexForAuth = func(callbacks ai.OAuthLoginCallbacks) (ai.OAuthCredentials, error) {
		if callbacks.OnAuth != nil {
			callbacks.OnAuth(ai.OAuthAuthInfo{
				URL:          "https://auth.openai.com/oauth/authorize?example=1",
				Instructions: "Open this URL in your browser and complete login.",
			})
		}
		return ai.OAuthCredentials{
			Access:  "oauth-access",
			Refresh: "oauth-refresh",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		}, nil
	}

	var out bytes.Buffer
	if err := runAuthSubcommand([]string{
		"login",
		"--env-file", envPath,
		"--provider", "openai-codex",
	}, &out, &out); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	envText := string(data)
	if !strings.Contains(envText, "OPENAI_CODEX_TOKEN=oauth-access") {
		t.Fatalf("expected OPENAI_CODEX_TOKEN in env file, got: %s", envText)
	}
	if !strings.Contains(envText, "OPENAI_CODEX_REFRESH_TOKEN=oauth-refresh") {
		t.Fatalf("expected OPENAI_CODEX_REFRESH_TOKEN in env file, got: %s", envText)
	}
	if !strings.Contains(envText, "OPENAI_CODEX_TOKEN_EXPIRES=") {
		t.Fatalf("expected OPENAI_CODEX_TOKEN_EXPIRES in env file, got: %s", envText)
	}
	if !strings.Contains(out.String(), "logged in openai-codex") {
		t.Fatalf("expected success output, got: %s", out.String())
	}
}

func TestAuthLoginGitHubCopilot(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "gopher.env")
	restore := loginGitHubCopilotForAuth
	t.Cleanup(func() {
		loginGitHubCopilotForAuth = restore
	})
	loginGitHubCopilotForAuth = func(callbacks ai.OAuthLoginCallbacks) (ai.OAuthCredentials, error) {
		if callbacks.OnAuth != nil {
			callbacks.OnAuth(ai.OAuthAuthInfo{
				URL:          "https://github.com/login/device",
				Instructions: "Enter code: ABCD-EFGH",
			})
		}
		return ai.OAuthCredentials{
			Access:  "copilot-access",
			Refresh: "copilot-refresh",
			Expires: time.Now().Add(time.Hour).UnixMilli(),
		}, nil
	}

	var out bytes.Buffer
	if err := runAuthSubcommand([]string{
		"login",
		"--env-file", envPath,
		"--provider", "github-copilot",
	}, &out, &out); err != nil {
		t.Fatalf("login failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	envText := string(data)
	if !strings.Contains(envText, "GITHUB_COPILOT_TOKEN=copilot-access") {
		t.Fatalf("expected GITHUB_COPILOT_TOKEN in env file, got: %s", envText)
	}
	if !strings.Contains(envText, "GITHUB_COPILOT_REFRESH_TOKEN=copilot-refresh") {
		t.Fatalf("expected GITHUB_COPILOT_REFRESH_TOKEN in env file, got: %s", envText)
	}
	if !strings.Contains(envText, "GITHUB_COPILOT_TOKEN_EXPIRES=") {
		t.Fatalf("expected GITHUB_COPILOT_TOKEN_EXPIRES in env file, got: %s", envText)
	}
	if !strings.Contains(out.String(), "logged in github-copilot") {
		t.Fatalf("expected success output, got: %s", out.String())
	}
}

func TestAuthLoginUnsupportedProvider(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	err := runAuthSubcommand([]string{"login", "--provider", "openai"}, &out, &out)
	if err == nil {
		t.Fatalf("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "interactive oauth login is not supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthDefaultEnvFileUsesEnvOverride(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "custom.env")
	t.Setenv("GOPHER_ENV_FILE", envPath)

	var out bytes.Buffer
	if err := runAuthSubcommand([]string{
		"set",
		"--provider", "zai",
		"--api-key", "secret-123",
	}, &out, &out); err != nil {
		t.Fatalf("set provider with GOPHER_ENV_FILE override failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	if !strings.Contains(string(data), "ZAI_API_KEY=secret-123") {
		t.Fatalf("expected ZAI_API_KEY in env file, got: %s", string(data))
	}
}

func TestAuthDefaultEnvFileUsesUserHomeForNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires non-root execution")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOPHER_ENV_FILE", "")
	envPath := filepath.Join(home, ".gopher", "gopher.env")

	var out bytes.Buffer
	if err := runAuthSubcommand([]string{
		"set",
		"--provider", "zai",
		"--api-key", "secret-123",
	}, &out, &out); err != nil {
		t.Fatalf("set provider with default home env file failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	if !strings.Contains(string(data), "ZAI_API_KEY=secret-123") {
		t.Fatalf("expected ZAI_API_KEY in env file, got: %s", string(data))
	}
}
