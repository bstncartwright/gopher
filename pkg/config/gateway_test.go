package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
)

func TestLoadGatewayConfigDefaults(t *testing.T) {
	cfg, sources, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: t.TempDir(),
		Env:        map[string]string{},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if cfg.NodeID != DefaultGatewayNodeID {
		t.Fatalf("node id = %q, want %q", cfg.NodeID, DefaultGatewayNodeID)
	}
	if cfg.GatewayNodeID != DefaultGatewayNodeID {
		t.Fatalf("gateway id = %q, want %q", cfg.GatewayNodeID, DefaultGatewayNodeID)
	}
	if cfg.Telegram.Enabled {
		t.Fatalf("telegram enabled = true, want false")
	}
	if cfg.Telegram.PollInterval <= 0 {
		t.Fatalf("telegram poll interval must be > 0")
	}
	if cfg.Telegram.PollTimeout <= 0 {
		t.Fatalf("telegram poll timeout must be > 0")
	}
	if len(sources) != 1 || sources[0] != "defaults" {
		t.Fatalf("sources = %#v, want defaults only", sources)
	}
}

func TestLoadGatewayConfigTOMLAndOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "file-gw"
gateway_id = "file-router"

[gateway.nats]
url = "nats://localhost:4222"
connect_timeout = "7s"
reconnect_wait = "4s"

[gateway.runtime]
heartbeat_interval = "6s"
prune_interval = "5s"

[[gateway.capabilities]]
kind = "tool"
name = "gpu"
`)
	overrideNodeID := "flag-gw"
	overrideHeartbeat := 11 * time.Second
	overrideCaps := []scheduler.Capability{{Kind: scheduler.CapabilitySystem, Name: "telegram"}}
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"GOPHER_GATEWAY_PRUNE_INTERVAL": "9s",
		},
		Overrides: GatewayOverrides{
			NodeID:            &overrideNodeID,
			HeartbeatInterval: &overrideHeartbeat,
			Capabilities:      &overrideCaps,
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if cfg.NodeID != "flag-gw" {
		t.Fatalf("node id = %q, want flag-gw", cfg.NodeID)
	}
	if cfg.GatewayNodeID != "file-router" {
		t.Fatalf("gateway id = %q, want file-router", cfg.GatewayNodeID)
	}
	if cfg.HeartbeatInterval != 11*time.Second {
		t.Fatalf("heartbeat interval = %s, want 11s", cfg.HeartbeatInterval)
	}
	if cfg.PruneInterval != 9*time.Second {
		t.Fatalf("prune interval = %s, want 9s", cfg.PruneInterval)
	}
	if len(cfg.Capabilities) != 1 || cfg.Capabilities[0].Name != "telegram" {
		t.Fatalf("capabilities override mismatch: %#v", cfg.Capabilities)
	}
}

func TestLoadGatewayConfigTelegramValidationAndOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
bot_token = "file-token"
poll_interval = "4s"
poll_timeout = "40s"
allowed_user_id = "1001"
allowed_chat_id = "2002"
`)

	overrideEnabled := true
	overrideToken := "override-token"
	overridePollInterval := 8 * time.Second
	overridePollTimeout := 50 * time.Second
	overrideUserID := "user-1"
	overrideChatID := "chat-1"
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"GOPHER_GATEWAY_TELEGRAM_POLL_INTERVAL": "6s",
			"GOPHER_GATEWAY_TELEGRAM_POLL_TIMEOUT":  "45s",
		},
		Overrides: GatewayOverrides{
			TelegramEnabled:       &overrideEnabled,
			TelegramBotToken:      &overrideToken,
			TelegramPollInterval:  &overridePollInterval,
			TelegramPollTimeout:   &overridePollTimeout,
			TelegramAllowedUserID: &overrideUserID,
			TelegramAllowedChatID: &overrideChatID,
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if !cfg.Telegram.Enabled {
		t.Fatalf("telegram.enabled = false, want true")
	}
	if cfg.Telegram.BotToken != overrideToken {
		t.Fatalf("telegram token = %q, want %q", cfg.Telegram.BotToken, overrideToken)
	}
	if cfg.Telegram.PollInterval != overridePollInterval {
		t.Fatalf("telegram poll interval = %s, want %s", cfg.Telegram.PollInterval, overridePollInterval)
	}
	if cfg.Telegram.PollTimeout != overridePollTimeout {
		t.Fatalf("telegram poll timeout = %s, want %s", cfg.Telegram.PollTimeout, overridePollTimeout)
	}
	if cfg.Telegram.AllowedUserID != overrideUserID || cfg.Telegram.AllowedChatID != overrideChatID {
		t.Fatalf("telegram binding mismatch: %+v", cfg.Telegram)
	}
}

func TestLoadGatewayConfigRejectsMissingTelegramSecretsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
bot_token = ""
poll_interval = "2s"
poll_timeout = "30s"
allowed_user_id = "1001"
allowed_chat_id = "2002"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected telegram validation error")
	}
}

func TestLoadGatewayConfigAcceptsLegacyTelegramTokenEnvKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
bot_token = ""
poll_interval = "2s"
poll_timeout = "30s"
`)
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"GOPHER_TELEGRAM_BOT_TOKEN": "legacy-token",
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if cfg.Telegram.BotToken != "legacy-token" {
		t.Fatalf("telegram token = %q, want legacy-token", cfg.Telegram.BotToken)
	}
}

func TestLoadGatewayConfigRejectsInvalidTelegramPollTimeout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
bot_token = "token"
poll_interval = "2s"
poll_timeout = "0s"
allowed_user_id = "1001"
allowed_chat_id = "2002"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected telegram poll timeout validation error")
	}
}

func TestLoadGatewayConfigRejectsNonLoopbackPanelListenAddr(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.panel]
listen_addr = "0.0.0.0:29329"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected panel listen addr validation error")
	}
}

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}
