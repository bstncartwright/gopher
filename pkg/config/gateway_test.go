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
	if cfg.Telegram.Mode != "polling" {
		t.Fatalf("telegram mode = %q, want polling", cfg.Telegram.Mode)
	}
	if cfg.Telegram.Webhook.ListenAddr != "127.0.0.1:29330" {
		t.Fatalf("telegram webhook listen addr = %q", cfg.Telegram.Webhook.ListenAddr)
	}
	if cfg.Telegram.Webhook.Path != "/_gopher/telegram/webhook" {
		t.Fatalf("telegram webhook path = %q", cfg.Telegram.Webhook.Path)
	}
	if !cfg.Cron.Enabled {
		t.Fatalf("cron enabled = false, want true")
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
mode = "polling"
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
	overrideMode := "webhook"
	overrideWebhookListen := "127.0.0.1:29440"
	overrideWebhookPath := "/telegram/hook"
	overrideWebhookURL := "https://example.ts.net/telegram/hook"
	overrideWebhookSecret := "secret-1"
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
			TelegramMode:          &overrideMode,
			TelegramWebhookListen: &overrideWebhookListen,
			TelegramWebhookPath:   &overrideWebhookPath,
			TelegramWebhookURL:    &overrideWebhookURL,
			TelegramWebhookSecret: &overrideWebhookSecret,
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
	if cfg.Telegram.Mode != overrideMode {
		t.Fatalf("telegram mode = %q, want %q", cfg.Telegram.Mode, overrideMode)
	}
	if cfg.Telegram.Webhook.ListenAddr != overrideWebhookListen {
		t.Fatalf("telegram webhook listen addr = %q, want %q", cfg.Telegram.Webhook.ListenAddr, overrideWebhookListen)
	}
	if cfg.Telegram.Webhook.Path != overrideWebhookPath {
		t.Fatalf("telegram webhook path = %q, want %q", cfg.Telegram.Webhook.Path, overrideWebhookPath)
	}
	if cfg.Telegram.Webhook.URL != overrideWebhookURL {
		t.Fatalf("telegram webhook url = %q, want %q", cfg.Telegram.Webhook.URL, overrideWebhookURL)
	}
	if cfg.Telegram.Webhook.Secret != overrideWebhookSecret {
		t.Fatalf("telegram webhook secret mismatch")
	}
}

func TestLoadGatewayConfigRejectsMissingTelegramSecretsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
mode = "polling"
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
mode = "polling"
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
mode = "polling"
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

func TestLoadGatewayConfigParsesA2ARemotes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("A2A_TOKEN", "secret-token")
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.a2a]
enabled = true
discovery_timeout = "6s"
request_timeout = "20s"
task_poll_interval = "3s"
stream_idle_timeout = "15s"
card_refresh_interval = "2m"
resume_scan_interval = "8s"
compat_legacy_well_known_path = true

[[gateway.a2a.remotes]]
id = "research"
display_name = "Research"
base_url = "https://example.com/a2a"
enabled = true
request_timeout = "12s"
tags = ["research"]

[gateway.a2a.remotes.headers]
Authorization = "Bearer ${A2A_TOKEN}"
`)
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"A2A_TOKEN": "secret-token",
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if !cfg.A2A.Enabled {
		t.Fatalf("a2a.enabled = false, want true")
	}
	if len(cfg.A2A.Remotes) != 1 {
		t.Fatalf("a2a.remotes len = %d, want 1", len(cfg.A2A.Remotes))
	}
	if cfg.A2A.Remotes[0].Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("authorization header = %q", cfg.A2A.Remotes[0].Headers["Authorization"])
	}
}

func TestLoadGatewayConfigRejectsDuplicateA2ARemotes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.a2a]
enabled = true

[[gateway.a2a.remotes]]
id = "research"
base_url = "https://example.com/a"

[[gateway.a2a.remotes]]
id = "research"
base_url = "https://example.com/b"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected duplicate a2a remotes validation error")
	}
}

func TestLoadGatewayConfigRejectsMissingA2AHeaderEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.a2a]
enabled = true

[[gateway.a2a.remotes]]
id = "research"
base_url = "https://example.com/a"

[gateway.a2a.remotes.headers]
Authorization = "Bearer ${MISSING_A2A_TOKEN}"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected missing env validation error")
	}
}

func TestLoadGatewayConfigAppliesTelegramWebhookEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
mode = "polling"
bot_token = "token"
poll_interval = "2s"
poll_timeout = "30s"
`)
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"GOPHER_GATEWAY_TELEGRAM_MODE":                "webhook",
			"GOPHER_GATEWAY_TELEGRAM_WEBHOOK_LISTEN_ADDR": "127.0.0.1:29440",
			"GOPHER_GATEWAY_TELEGRAM_WEBHOOK_PATH":        "/tg/webhook",
			"GOPHER_GATEWAY_TELEGRAM_WEBHOOK_URL":         "https://example.ts.net/tg/webhook",
			"GOPHER_GATEWAY_TELEGRAM_WEBHOOK_SECRET":      "secret",
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if cfg.Telegram.Mode != "webhook" {
		t.Fatalf("telegram mode = %q, want webhook", cfg.Telegram.Mode)
	}
	if cfg.Telegram.Webhook.ListenAddr != "127.0.0.1:29440" {
		t.Fatalf("telegram webhook listen addr = %q", cfg.Telegram.Webhook.ListenAddr)
	}
	if cfg.Telegram.Webhook.Path != "/tg/webhook" {
		t.Fatalf("telegram webhook path = %q", cfg.Telegram.Webhook.Path)
	}
	if cfg.Telegram.Webhook.URL != "https://example.ts.net/tg/webhook" {
		t.Fatalf("telegram webhook url = %q", cfg.Telegram.Webhook.URL)
	}
	if cfg.Telegram.Webhook.Secret != "secret" {
		t.Fatalf("telegram webhook secret mismatch")
	}
}

func TestLoadGatewayConfigWebhookModeRequiresWebhookFields(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
mode = "webhook"
bot_token = "token"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected telegram webhook validation error")
	}
}

func TestLoadGatewayConfigWebhookModeValidation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
mode = "webhook"
bot_token = "token"
allowed_user_id = "1001"
allowed_chat_id = "2002"

[gateway.telegram.webhook]
listen_addr = "127.0.0.1:29330"
path = "/_gopher/telegram/webhook"
url = "https://example.ts.net/_gopher/telegram/webhook"
secret = "s3cret"
`)
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if cfg.Telegram.Mode != "webhook" {
		t.Fatalf("telegram mode = %q, want webhook", cfg.Telegram.Mode)
	}
}

func TestLoadGatewayConfigRejectsInvalidTelegramMode(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
mode = "streaming"
bot_token = "token"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected telegram mode validation error")
	}
}

func TestLoadGatewayConfigRejectsInvalidTelegramWebhookPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
mode = "webhook"
bot_token = "token"

[gateway.telegram.webhook]
listen_addr = "127.0.0.1:29330"
path = "telegram/webhook"
url = "https://example.ts.net/telegram/webhook"
secret = "secret"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected telegram webhook path validation error")
	}
}

func TestLoadGatewayConfigRejectsNonLoopbackTelegramWebhookListenAddr(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.telegram]
enabled = true
mode = "webhook"
bot_token = "token"

[gateway.telegram.webhook]
listen_addr = "0.0.0.0:29330"
path = "/telegram/webhook"
url = "https://example.ts.net/telegram/webhook"
secret = "secret"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected telegram webhook listen addr validation error")
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
