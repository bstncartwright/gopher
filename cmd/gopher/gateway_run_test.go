package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/config"
	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
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
		inputs.Overrides.HeartbeatInterval != nil ||
		inputs.Overrides.PruneInterval != nil ||
		inputs.Overrides.Capabilities != nil {
		t.Fatalf("expected no overrides, got %#v", inputs.Overrides)
	}
}

func TestParseGatewayRunFlagsCapabilities(t *testing.T) {
	inputs, err := parseGatewayRunFlags([]string{
		"--node-id", "gw-1",
		"--config", "custom.toml",
		"--capability", "agent:planner",
		"--capability", "tool:gpu",
		"--capability", "system:matrix",
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
	if inputs.Overrides.Capabilities == nil || len(*inputs.Overrides.Capabilities) != 3 {
		t.Fatalf("capability override = %#v", inputs.Overrides.Capabilities)
	}
	if (*inputs.Overrides.Capabilities)[0] != (scheduler.Capability{Kind: scheduler.CapabilityAgent, Name: "planner"}) {
		t.Fatalf("capability[0] = %+v", (*inputs.Overrides.Capabilities)[0])
	}
	if (*inputs.Overrides.Capabilities)[1] != (scheduler.Capability{Kind: scheduler.CapabilityTool, Name: "gpu"}) {
		t.Fatalf("capability[1] = %+v", (*inputs.Overrides.Capabilities)[1])
	}
	if (*inputs.Overrides.Capabilities)[2] != (scheduler.Capability{Kind: scheduler.CapabilitySystem, Name: "matrix"}) {
		t.Fatalf("capability[2] = %+v", (*inputs.Overrides.Capabilities)[2])
	}
}

func TestParseGatewayRunFlagsMatrixOverrides(t *testing.T) {
	inputs, err := parseGatewayRunFlags([]string{
		"--matrix-enabled", "true",
		"--matrix-homeserver-url", "http://localhost:8008",
		"--matrix-appservice-id", "gopher",
		"--matrix-as-token", "as-token",
		"--matrix-hs-token", "hs-token",
		"--matrix-listen-addr", "127.0.0.1:29328",
		"--matrix-bot-user-id", "@gopher:local",
	})
	if err != nil {
		t.Fatalf("parseGatewayRunFlags() error: %v", err)
	}
	if inputs.Overrides.MatrixEnabled == nil || !*inputs.Overrides.MatrixEnabled {
		t.Fatalf("matrix enabled override missing")
	}
	if inputs.Overrides.MatrixHomeserver == nil || *inputs.Overrides.MatrixHomeserver != "http://localhost:8008" {
		t.Fatalf("matrix homeserver override = %#v", inputs.Overrides.MatrixHomeserver)
	}
	if inputs.Overrides.MatrixASToken == nil || *inputs.Overrides.MatrixASToken != "as-token" {
		t.Fatalf("matrix as token override missing")
	}
	if inputs.Overrides.MatrixHSToken == nil || *inputs.Overrides.MatrixHSToken != "hs-token" {
		t.Fatalf("matrix hs token override missing")
	}
}

func TestParseGatewayRunFlagsRejectsBadCapability(t *testing.T) {
	if _, err := parseGatewayRunFlags([]string{"--capability", "badformat"}); err == nil {
		t.Fatalf("expected parse error for invalid capability")
	}
}

func TestStartGatewayProcessLifecycleInMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := config.GatewayConfig{
		NodeID:            "gateway-test",
		GatewayNodeID:     "gateway-test",
		HeartbeatInterval: 20 * time.Millisecond,
		PruneInterval:     20 * time.Millisecond,
		Capabilities: []scheduler.Capability{
			{Kind: scheduler.CapabilityAgent, Name: "agent"},
			{Kind: scheduler.CapabilityTool, Name: "gpu"},
		},
	}
	bus := fabricts.NewInMemoryBus()
	process, err := startGatewayProcess(ctx, cfg, bus, newTempExecutor(), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("startGatewayProcess() error: %v", err)
	}
	defer process.Stop()

	if _, ok := process.registry.Get("gateway-test"); !ok {
		t.Fatalf("gateway node not present in registry")
	}
	if selection, err := process.scheduler.Select(scheduler.SelectionRequest{}); err != nil {
		t.Fatalf("scheduler.Select() error: %v", err)
	} else if selection.NodeID != "gateway-test" {
		t.Fatalf("selected node = %q, want gateway-test", selection.NodeID)
	}

	announcement := node.CapabilityAnnouncement{
		NodeID:       "node-remote",
		IsGateway:    false,
		Capabilities: []scheduler.Capability{{Kind: scheduler.CapabilityTool, Name: "gpu"}},
		Timestamp:    time.Now().UTC(),
	}
	blob, err := json.Marshal(announcement)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	if err := bus.Publish(ctx, fabricts.Message{
		Subject: fabricts.NodeCapabilitiesSubject("node-remote"),
		Data:    blob,
	}); err != nil {
		t.Fatalf("bus.Publish() error: %v", err)
	}
	if err := waitForRegistryNode(ctx, process.registry, "node-remote"); err != nil {
		t.Fatalf("waitForRegistryNode(node-remote) error: %v", err)
	}

	process.Stop()
	process.Stop()
}

func TestRunGatewaySubcommandHelp(t *testing.T) {
	var out bytes.Buffer
	if err := runGatewaySubcommand([]string{"run", "--help"}, &out, io.Discard); err != nil {
		t.Fatalf("runGatewaySubcommand(run --help) error: %v", err)
	}
	if got := out.String(); got == "" || !strings.Contains(got, "gopher gateway run") {
		t.Fatalf("help output missing expected usage text: %q", got)
	}
}

func TestRunGatewayConfigSubcommandInit(t *testing.T) {
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
	if got := string(body); !strings.Contains(got, "[gateway]") {
		t.Fatalf("generated config missing gateway section: %q", got)
	}
}

func waitForRegistryNode(ctx context.Context, registry *scheduler.Registry, nodeID string) error {
	for {
		if _, ok := registry.Get(nodeID); ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
