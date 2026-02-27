package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
)

func TestParseGatewayRunFlagsDefaults(t *testing.T) {
	inputs, err := parseGatewayRunFlags(nil)
	if err != nil {
		t.Fatalf("parseGatewayRunFlags() error: %v", err)
	}
	if inputs.ConfigPath != "" {
		t.Fatalf("config path = %q, want empty", inputs.ConfigPath)
	}
	if inputs.Overrides.NodeID != nil ||
		inputs.Overrides.GatewayNodeID != nil ||
		inputs.Overrides.NATSURL != nil ||
		inputs.Overrides.TelegramEnabled != nil ||
		inputs.Overrides.TelegramBotToken != nil ||
		inputs.Overrides.TelegramPollInterval != nil ||
		inputs.Overrides.TelegramPollTimeout != nil ||
		inputs.Overrides.TelegramAllowedUserID != nil ||
		inputs.Overrides.TelegramAllowedChatID != nil ||
		inputs.Overrides.TelegramMode != nil ||
		inputs.Overrides.TelegramWebhookListen != nil ||
		inputs.Overrides.TelegramWebhookPath != nil ||
		inputs.Overrides.TelegramWebhookURL != nil ||
		inputs.Overrides.TelegramWebhookSecret != nil {
		t.Fatalf("expected no overrides, got %#v", inputs.Overrides)
	}
}

func TestParseGatewayRunFlagsTelegramOverrides(t *testing.T) {
	inputs, err := parseGatewayRunFlags([]string{
		"--config", "custom.toml",
		"--node-id", "gw-1",
		"--gateway-id", "router-1",
		"--nats-url", "nats://127.0.0.1:4222",
		"--telegram-enabled", "true",
		"--telegram-bot-token", "token-123",
		"--telegram-poll-interval", "5s",
		"--telegram-poll-timeout", "40s",
		"--telegram-allowed-user-id", "1001",
		"--telegram-allowed-chat-id", "2002",
		"--telegram-mode", "webhook",
		"--telegram-webhook-listen-addr", "127.0.0.1:29330",
		"--telegram-webhook-path", "/_gopher/telegram/webhook",
		"--telegram-webhook-url", "https://example.ts.net/_gopher/telegram/webhook",
		"--telegram-webhook-secret", "secret",
		"--capability", "tool:gpu",
	})
	if err != nil {
		t.Fatalf("parseGatewayRunFlags() error: %v", err)
	}
	if inputs.ConfigPath != "custom.toml" {
		t.Fatalf("config path = %q, want custom.toml", inputs.ConfigPath)
	}
	if inputs.Overrides.NodeID == nil || *inputs.Overrides.NodeID != "gw-1" {
		t.Fatalf("node override = %#v", inputs.Overrides.NodeID)
	}
	if inputs.Overrides.GatewayNodeID == nil || *inputs.Overrides.GatewayNodeID != "router-1" {
		t.Fatalf("gateway override = %#v", inputs.Overrides.GatewayNodeID)
	}
	if inputs.Overrides.NATSURL == nil || *inputs.Overrides.NATSURL != "nats://127.0.0.1:4222" {
		t.Fatalf("nats override = %#v", inputs.Overrides.NATSURL)
	}
	if inputs.Overrides.TelegramEnabled == nil || !*inputs.Overrides.TelegramEnabled {
		t.Fatalf("telegram enabled override = %#v", inputs.Overrides.TelegramEnabled)
	}
	if inputs.Overrides.TelegramBotToken == nil || *inputs.Overrides.TelegramBotToken != "token-123" {
		t.Fatalf("telegram bot token override = %#v", inputs.Overrides.TelegramBotToken)
	}
	if inputs.Overrides.TelegramPollInterval == nil || *inputs.Overrides.TelegramPollInterval != 5*time.Second {
		t.Fatalf("telegram poll interval override = %#v", inputs.Overrides.TelegramPollInterval)
	}
	if inputs.Overrides.TelegramPollTimeout == nil || *inputs.Overrides.TelegramPollTimeout != 40*time.Second {
		t.Fatalf("telegram poll timeout override = %#v", inputs.Overrides.TelegramPollTimeout)
	}
	if inputs.Overrides.TelegramAllowedUserID == nil || *inputs.Overrides.TelegramAllowedUserID != "1001" {
		t.Fatalf("telegram allowed user override = %#v", inputs.Overrides.TelegramAllowedUserID)
	}
	if inputs.Overrides.TelegramAllowedChatID == nil || *inputs.Overrides.TelegramAllowedChatID != "2002" {
		t.Fatalf("telegram allowed chat override = %#v", inputs.Overrides.TelegramAllowedChatID)
	}
	if inputs.Overrides.TelegramMode == nil || *inputs.Overrides.TelegramMode != "webhook" {
		t.Fatalf("telegram mode override = %#v", inputs.Overrides.TelegramMode)
	}
	if inputs.Overrides.TelegramWebhookListen == nil || *inputs.Overrides.TelegramWebhookListen != "127.0.0.1:29330" {
		t.Fatalf("telegram webhook listen override = %#v", inputs.Overrides.TelegramWebhookListen)
	}
	if inputs.Overrides.TelegramWebhookPath == nil || *inputs.Overrides.TelegramWebhookPath != "/_gopher/telegram/webhook" {
		t.Fatalf("telegram webhook path override = %#v", inputs.Overrides.TelegramWebhookPath)
	}
	if inputs.Overrides.TelegramWebhookURL == nil || *inputs.Overrides.TelegramWebhookURL != "https://example.ts.net/_gopher/telegram/webhook" {
		t.Fatalf("telegram webhook url override = %#v", inputs.Overrides.TelegramWebhookURL)
	}
	if inputs.Overrides.TelegramWebhookSecret == nil || *inputs.Overrides.TelegramWebhookSecret != "secret" {
		t.Fatalf("telegram webhook secret override = %#v", inputs.Overrides.TelegramWebhookSecret)
	}
	if inputs.Overrides.Capabilities == nil || len(*inputs.Overrides.Capabilities) != 1 {
		t.Fatalf("capability override = %#v", inputs.Overrides.Capabilities)
	}
	wantCap := scheduler.Capability{Kind: scheduler.CapabilityTool, Name: "gpu"}
	if (*inputs.Overrides.Capabilities)[0] != wantCap {
		t.Fatalf("capability[0] = %+v, want %+v", (*inputs.Overrides.Capabilities)[0], wantCap)
	}
}

func TestParseGatewayRunFlagsRejectsBadCapability(t *testing.T) {
	if _, err := parseGatewayRunFlags([]string{"--capability", "badformat"}); err == nil {
		t.Fatalf("expected parse error for invalid capability")
	}
}

func TestRunGatewaySubcommandHelp(t *testing.T) {
	var out bytes.Buffer
	if err := runGatewaySubcommand([]string{"run", "--help"}, &out, io.Discard); err != nil {
		t.Fatalf("runGatewaySubcommand(run --help) error: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "gopher gateway run") {
		t.Fatalf("help output missing run usage: %q", got)
	}
	if !strings.Contains(got, "--telegram-enabled") {
		t.Fatalf("help output missing telegram flag: %q", got)
	}
	if !strings.Contains(got, "--telegram-mode") {
		t.Fatalf("help output missing telegram mode flag: %q", got)
	}
}

func TestRunGatewayConfigSubcommandInitWritesTelegramTemplate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gopher.toml")
	var out bytes.Buffer
	if err := runGatewayConfigSubcommand([]string{"init", "--path", target}, &out, io.Discard); err != nil {
		t.Fatalf("runGatewayConfigSubcommand(init) error: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read generated config error: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "[gateway.telegram]") {
		t.Fatalf("generated config missing telegram block: %q", text)
	}
	if !strings.Contains(text, "[gateway.telegram.webhook]") {
		t.Fatalf("generated config missing telegram webhook block: %q", text)
	}
}

func TestEnsureGatewayRunConfigExistsCreatesDefaultPrimary(t *testing.T) {
	dir := t.TempDir()
	if err := ensureGatewayRunConfigExists(dir, ""); err != nil {
		t.Fatalf("ensureGatewayRunConfigExists() error: %v", err)
	}

	target := filepath.Join(dir, "gopher.toml")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected gopher.toml to be created: %v", err)
	}
	if !strings.Contains(string(body), "[gateway]") {
		t.Fatalf("default config missing [gateway] block")
	}
}

func TestEnsureGatewayRunConfigExistsUsesLocalWithoutCreatingPrimary(t *testing.T) {
	dir := t.TempDir()
	local := filepath.Join(dir, "gopher.local.toml")
	if err := os.WriteFile(local, []byte("[gateway]\n"), 0o644); err != nil {
		t.Fatalf("write local config: %v", err)
	}

	if err := ensureGatewayRunConfigExists(dir, ""); err != nil {
		t.Fatalf("ensureGatewayRunConfigExists() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "gopher.toml")); !os.IsNotExist(err) {
		t.Fatalf("expected primary config to remain absent, stat err=%v", err)
	}
}

func TestEnsureGatewayRunConfigExistsCreatesExplicitPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join("configs", "custom.toml")
	if err := ensureGatewayRunConfigExists(dir, target); err != nil {
		t.Fatalf("ensureGatewayRunConfigExists() error: %v", err)
	}

	absTarget := filepath.Join(dir, target)
	if _, err := os.Stat(absTarget); err != nil {
		t.Fatalf("expected explicit config path to be created: %v", err)
	}
}
