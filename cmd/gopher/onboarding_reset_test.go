package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunOnboardingSubcommandNonInteractive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envPath := filepath.Join(dir, "gopher.env")
	gatewayPath := filepath.Join(dir, "gopher.toml")
	nodePath := filepath.Join(dir, "node.toml")

	var out bytes.Buffer
	err := runOnboardingSubcommand([]string{
		"--non-interactive",
		"--gateway-config-path", gatewayPath,
		"--node-config-path", nodePath,
		"--env-file", envPath,
		"--auth-provider", "zai",
		"--auth-api-key", "zai-key",
		"--telegram-bot-token", "bot-token",
	}, strings.NewReader(""), &out, &out)
	if err != nil {
		t.Fatalf("runOnboardingSubcommand() error: %v", err)
	}

	gatewayBlob, err := os.ReadFile(gatewayPath)
	if err != nil {
		t.Fatalf("read gateway config: %v", err)
	}
	if !strings.Contains(string(gatewayBlob), "[gateway]") {
		t.Fatalf("gateway config missing defaults: %s", string(gatewayBlob))
	}

	nodeBlob, err := os.ReadFile(nodePath)
	if err != nil {
		t.Fatalf("read node config: %v", err)
	}
	if !strings.Contains(string(nodeBlob), "[node]") {
		t.Fatalf("node config missing defaults: %s", string(nodeBlob))
	}

	envBlob, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	envText := string(envBlob)
	if !strings.Contains(envText, "ZAI_API_KEY=zai-key") {
		t.Fatalf("expected zai auth in env: %s", envText)
	}
	if !strings.Contains(envText, "GOPHER_TELEGRAM_BOT_TOKEN=bot-token") {
		t.Fatalf("expected telegram bot token in env: %s", envText)
	}
}

func TestRunOnboardingSubcommandNonInteractiveFailsWhenAuthMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envPath := filepath.Join(dir, "gopher.env")
	gatewayPath := filepath.Join(dir, "gopher.toml")
	nodePath := filepath.Join(dir, "node.toml")

	var out bytes.Buffer
	err := runOnboardingSubcommand([]string{
		"--non-interactive",
		"--gateway-config-path", gatewayPath,
		"--node-config-path", nodePath,
		"--env-file", envPath,
	}, strings.NewReader(""), &out, &out)
	if err == nil {
		t.Fatalf("expected error when auth is missing in non-interactive mode")
	}
	if !strings.Contains(err.Error(), "auth is missing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunFactoryResetSubcommandPreservesAuthAndDeletesData(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	if err := os.WriteFile(filepath.Join(workspace, "gopher.toml"), []byte("[gateway]\n"), 0o644); err != nil {
		t.Fatalf("write gopher.toml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "node.toml"), []byte("[node]\n"), 0o644); err != nil {
		t.Fatalf("write node.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".gopher", "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir workspace .gopher: %v", err)
	}

	homeGopher := filepath.Join(home, ".gopher")
	if err := os.MkdirAll(filepath.Join(homeGopher, "agents"), 0o755); err != nil {
		t.Fatalf("mkdir home .gopher: %v", err)
	}
	authPath := filepath.Join(homeGopher, "gopher.env")
	authContent := "OPENAI_API_KEY=preserved\n"
	if err := os.WriteFile(authPath, []byte(authContent), 0o600); err != nil {
		t.Fatalf("write auth env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(homeGopher, "agents", "index.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("write agents index: %v", err)
	}

	var out bytes.Buffer
	err := runFactoryResetSubcommand([]string{
		"--yes",
		"--workspace", workspace,
		"--env-file", authPath,
	}, &out, &out)
	if err != nil {
		t.Fatalf("runFactoryResetSubcommand() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workspace, "gopher.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected workspace gopher.toml removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "node.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected workspace node.toml removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".gopher")); !os.IsNotExist(err) {
		t.Fatalf("expected workspace .gopher removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(homeGopher, "agents")); !os.IsNotExist(err) {
		t.Fatalf("expected home .gopher agents removed, stat err=%v", err)
	}

	blob, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read preserved auth env: %v", err)
	}
	if string(blob) != authContent {
		t.Fatalf("auth env not preserved: got %q want %q", string(blob), authContent)
	}
}
