package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
)

func TestParseGatewayNodesListFlagsDefaults(t *testing.T) {
	inputs, err := parseGatewayNodesListFlags(nil)
	if err != nil {
		t.Fatalf("parseGatewayNodesListFlags() error: %v", err)
	}
	if inputs.ConfigPath != "" {
		t.Fatalf("config path = %q, want empty", inputs.ConfigPath)
	}
	if inputs.Wait != defaultGatewayNodesListWait {
		t.Fatalf("wait = %s, want %s", inputs.Wait, defaultGatewayNodesListWait)
	}
	if inputs.Overrides.NATSURL != nil {
		t.Fatalf("expected nil nats override, got %#v", inputs.Overrides.NATSURL)
	}
}

func TestParseGatewayNodesListFlagsOverrides(t *testing.T) {
	inputs, err := parseGatewayNodesListFlags([]string{
		"--config", "custom.toml",
		"--nats-url", "nats://10.0.0.5:4222",
		"--wait", "7s",
	})
	if err != nil {
		t.Fatalf("parseGatewayNodesListFlags() error: %v", err)
	}
	if inputs.ConfigPath != "custom.toml" {
		t.Fatalf("config path = %q, want custom.toml", inputs.ConfigPath)
	}
	if inputs.Overrides.NATSURL == nil || *inputs.Overrides.NATSURL != "nats://10.0.0.5:4222" {
		t.Fatalf("nats override = %#v", inputs.Overrides.NATSURL)
	}
	if inputs.Wait != 7*time.Second {
		t.Fatalf("wait = %s, want 7s", inputs.Wait)
	}
}

func TestParseGatewayNodesListFlagsRejectsNonPositiveWait(t *testing.T) {
	if _, err := parseGatewayNodesListFlags([]string{"--wait", "0s"}); err == nil {
		t.Fatalf("expected parse error for non-positive wait")
	}
}

func TestObserveGatewayNodesCollectsNodes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	bus := fabricts.NewInMemoryBus()

	gatewayRuntime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            "gw-a",
		IsGateway:         true,
		Capabilities:      []scheduler.Capability{{Kind: scheduler.CapabilityAgent, Name: "agent"}},
		Fabric:            bus,
		Executor:          newTempExecutor(),
		HeartbeatInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("node.NewRuntime(gateway) error: %v", err)
	}
	if err := gatewayRuntime.Start(ctx); err != nil {
		t.Fatalf("gatewayRuntime.Start() error: %v", err)
	}
	defer gatewayRuntime.Stop()

	workerRuntime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            "worker-b",
		Capabilities:      []scheduler.Capability{{Kind: scheduler.CapabilityTool, Name: "gpu"}},
		Fabric:            bus,
		Executor:          newTempExecutor(),
		HeartbeatInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("node.NewRuntime(worker) error: %v", err)
	}
	if err := workerRuntime.Start(ctx); err != nil {
		t.Fatalf("workerRuntime.Start() error: %v", err)
	}
	defer workerRuntime.Stop()

	nodes, err := observeGatewayNodes(ctx, bus, 50*time.Millisecond, 120*time.Millisecond)
	if err != nil {
		t.Fatalf("observeGatewayNodes() error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes length = %d, want 2 (%+v)", len(nodes), nodes)
	}

	seen := make(map[string]scheduler.NodeInfo, len(nodes))
	for _, n := range nodes {
		seen[n.NodeID] = n
	}
	if _, ok := seen["gw-a"]; !ok {
		t.Fatalf("gateway node not found in snapshot")
	}
	if _, ok := seen["worker-b"]; !ok {
		t.Fatalf("worker node not found in snapshot")
	}
}

func TestRunGatewayNodesSubcommandHelp(t *testing.T) {
	var out bytes.Buffer
	if err := runGatewayNodesSubcommand([]string{"list", "--help"}, &out, io.Discard); err != nil {
		t.Fatalf("runGatewayNodesSubcommand(list --help) error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "gopher gateway nodes list") {
		t.Fatalf("help output missing command usage: %q", got)
	}
}
