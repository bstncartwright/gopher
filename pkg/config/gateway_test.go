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
	if cfg.HeartbeatInterval != DefaultHeartbeatInterval {
		t.Fatalf("heartbeat interval = %s, want %s", cfg.HeartbeatInterval, DefaultHeartbeatInterval)
	}
	if len(cfg.Capabilities) != 1 {
		t.Fatalf("capabilities len = %d, want 1", len(cfg.Capabilities))
	}
	if len(sources) != 1 || sources[0] != "defaults" {
		t.Fatalf("sources = %#v, want defaults only", sources)
	}
	if cfg.Cron.Enabled {
		t.Fatalf("cron enabled = true, want false")
	}
	if cfg.Cron.PollInterval != DefaultCronPollInterval {
		t.Fatalf("cron poll interval = %s, want %s", cfg.Cron.PollInterval, DefaultCronPollInterval)
	}
	if cfg.Cron.DefaultTimezone != "UTC" {
		t.Fatalf("cron timezone = %q, want UTC", cfg.Cron.DefaultTimezone)
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
	writeFile(t, filepath.Join(dir, "gopher.local.toml"), `
[gateway]
node_id = "local-gw"
`)

	overrideNodeID := "flag-gw"
	overrideHeartbeat := 11 * time.Second
	overrideCaps := []scheduler.Capability{{Kind: scheduler.CapabilitySystem, Name: "matrix"}}
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
	if cfg.ConnectTimeout != 7*time.Second || cfg.ReconnectWait != 4*time.Second {
		t.Fatalf("nats durations = %s/%s, want 7s/4s", cfg.ConnectTimeout, cfg.ReconnectWait)
	}
	if len(cfg.Capabilities) != 1 || cfg.Capabilities[0].Name != "matrix" {
		t.Fatalf("capabilities = %#v, want system:matrix override", cfg.Capabilities)
	}
}

func TestLoadGatewayConfigRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"
bad_field = "nope"
`)

	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected unknown field error")
	}
}

func TestLoadGatewayConfigRejectsInvalidDuration(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway.runtime]
heartbeat_interval = "bad"
`)

	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected invalid duration error")
	}
}

func TestLoadGatewayConfigRejectsInvalidCapabilityKind(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[[gateway.capabilities]]
kind = "wrong"
name = "agent"
`)

	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected invalid capability kind error")
	}
}

func TestLoadGatewayConfigMatrixValidationAndOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.matrix]
enabled = true
homeserver_url = "http://localhost:8008"
appservice_id = "gopher"
as_token = "as-file"
hs_token = "hs-file"
listen_addr = "127.0.0.1:29328"
bot_user_id = "@gopher:local"
`)

	overrideEnabled := true
	overrideHS := "http://example.test:8008"
	overrideAS := "override-as"
	overrideHSSecret := "override-hs"
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Overrides: GatewayOverrides{
			MatrixEnabled:    &overrideEnabled,
			MatrixHomeserver: &overrideHS,
			MatrixASToken:    &overrideAS,
			MatrixHSToken:    &overrideHSSecret,
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if !cfg.Matrix.Enabled {
		t.Fatalf("matrix.enabled = false, want true")
	}
	if cfg.Matrix.HomeserverURL != overrideHS {
		t.Fatalf("matrix homeserver = %q, want %q", cfg.Matrix.HomeserverURL, overrideHS)
	}
	if cfg.Matrix.ASToken != overrideAS || cfg.Matrix.HSToken != overrideHSSecret {
		t.Fatalf("matrix tokens not overridden as expected")
	}
}

func TestLoadGatewayConfigCronFileEnvAndOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway.cron]
enabled = true
poll_interval = "3s"
default_timezone = "America/New_York"
`)

	overrideEnabled := true
	overridePoll := 7 * time.Second
	overrideTZ := "UTC"
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"GOPHER_GATEWAY_CRON_ENABLED":          "false",
			"GOPHER_GATEWAY_CRON_POLL_INTERVAL":    "5s",
			"GOPHER_GATEWAY_CRON_DEFAULT_TIMEZONE": "Asia/Tokyo",
		},
		Overrides: GatewayOverrides{
			CronEnabled:      &overrideEnabled,
			CronPollInterval: &overridePoll,
			CronTimezone:     &overrideTZ,
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if !cfg.Cron.Enabled {
		t.Fatalf("cron enabled = false, want true")
	}
	if cfg.Cron.PollInterval != 7*time.Second {
		t.Fatalf("cron poll interval = %s, want 7s", cfg.Cron.PollInterval)
	}
	if cfg.Cron.DefaultTimezone != "UTC" {
		t.Fatalf("cron timezone = %q, want UTC", cfg.Cron.DefaultTimezone)
	}
}

func TestLoadGatewayConfigRejectsMissingMatrixSecretsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.matrix]
enabled = true
homeserver_url = "http://localhost:8008"
appservice_id = "gopher"
`)

	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected matrix validation error")
	}
}

func TestLoadGatewayConfigAppliesGatewayNodeFallbackAndCronTimezoneDefault(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw-fallback"
gateway_id = ""

[gateway.cron]
enabled = false
poll_interval = "1s"
default_timezone = ""
`)

	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if cfg.GatewayNodeID != "gw-fallback" {
		t.Fatalf("gateway node id = %q, want gw-fallback", cfg.GatewayNodeID)
	}
	if cfg.Cron.DefaultTimezone != "UTC" {
		t.Fatalf("cron default timezone = %q, want UTC", cfg.Cron.DefaultTimezone)
	}
}

func TestLoadGatewayConfigUpdateValidation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"

[gateway.update]
enabled = true
repo_owner = ""
repo_name = "repo"
channel = "stable"
check_interval = "1h"
binary_asset_pattern = "linux"
`)
	_, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected update validation error for missing repo_owner")
	}
}

func TestLoadGatewayConfigUpdateEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gopher.toml"), `
[gateway]
node_id = "gw"
`)
	cfg, _, err := LoadGatewayConfig(GatewayLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"GOPHER_GATEWAY_UPDATE_ENABLED":              "true",
			"GOPHER_GATEWAY_UPDATE_REPO_OWNER":           "acme",
			"GOPHER_GATEWAY_UPDATE_REPO_NAME":            "gopher",
			"GOPHER_GATEWAY_UPDATE_CHECK_INTERVAL":       "2h",
			"GOPHER_GATEWAY_UPDATE_BINARY_ASSET_PATTERN": "linux-amd64",
		},
	})
	if err != nil {
		t.Fatalf("LoadGatewayConfig() error: %v", err)
	}
	if !cfg.Update.Enabled || cfg.Update.RepoOwner != "acme" || cfg.Update.RepoName != "gopher" {
		t.Fatalf("update env overrides not applied: %+v", cfg.Update)
	}
}

func writeFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}
