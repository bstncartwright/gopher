package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
)

func TestLoadNodeConfigDefaults(t *testing.T) {
	cfg, sources, err := LoadNodeConfig(NodeLoadOptions{
		WorkingDir: t.TempDir(),
		Env:        map[string]string{},
	})
	if err != nil {
		t.Fatalf("LoadNodeConfig() error: %v", err)
	}
	if cfg.NodeID != DefaultNodeNodeID {
		t.Fatalf("node id = %q, want %q", cfg.NodeID, DefaultNodeNodeID)
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
}

func TestLoadNodeConfigTOMLEnvAndOverrides(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "node.toml"), `
[node]
node_id = "file-node"

[node.nats]
url = "nats://localhost:4222"
connect_timeout = "7s"
reconnect_wait = "4s"

[node.runtime]
heartbeat_interval = "6s"

[[node.capabilities]]
kind = "tool"
name = "gpu"
`)

	overrideNodeID := "flag-node"
	overrideHeartbeat := 11 * time.Second
	overrideCaps := []scheduler.Capability{{Kind: scheduler.CapabilitySystem, Name: "matrix"}}
	cfg, _, err := LoadNodeConfig(NodeLoadOptions{
		WorkingDir: dir,
		Env: map[string]string{
			"GOPHER_NODE_NATS_URL": "nats://env:4222",
		},
		Overrides: NodeOverrides{
			NodeID:            &overrideNodeID,
			HeartbeatInterval: &overrideHeartbeat,
			Capabilities:      &overrideCaps,
		},
	})
	if err != nil {
		t.Fatalf("LoadNodeConfig() error: %v", err)
	}

	if cfg.NodeID != "flag-node" {
		t.Fatalf("node id = %q, want flag-node", cfg.NodeID)
	}
	if cfg.NATSURL != "nats://env:4222" {
		t.Fatalf("nats url = %q, want env override", cfg.NATSURL)
	}
	if cfg.HeartbeatInterval != 11*time.Second {
		t.Fatalf("heartbeat interval = %s, want 11s", cfg.HeartbeatInterval)
	}
	if cfg.ConnectTimeout != 7*time.Second || cfg.ReconnectWait != 4*time.Second {
		t.Fatalf("nats durations = %s/%s, want 7s/4s", cfg.ConnectTimeout, cfg.ReconnectWait)
	}
	if len(cfg.Capabilities) != 1 || cfg.Capabilities[0].Name != "matrix" {
		t.Fatalf("capabilities = %#v, want system:matrix override", cfg.Capabilities)
	}
}

func TestLoadNodeConfigRejectsInvalidCapability(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "node.toml"), `
[[node.capabilities]]
kind = "wrong"
name = "agent"
`)

	_, _, err := LoadNodeConfig(NodeLoadOptions{
		WorkingDir: dir,
		Env:        map[string]string{},
	})
	if err == nil {
		t.Fatalf("expected invalid capability kind error")
	}
}
