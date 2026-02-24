package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
)

func TestParseNodeRunFlagsDefaults(t *testing.T) {
	inputs, err := parseNodeRunFlags(nil)
	if err != nil {
		t.Fatalf("parseNodeRunFlags() error: %v", err)
	}
	if inputs.ConfigPath != "" {
		t.Fatalf("config path = %q, want empty", inputs.ConfigPath)
	}
	if inputs.Overrides.NodeID != nil ||
		inputs.Overrides.NATSURL != nil ||
		inputs.Overrides.HeartbeatInterval != nil ||
		inputs.Overrides.Capabilities != nil {
		t.Fatalf("expected no overrides, got %#v", inputs.Overrides)
	}
}

func TestParseNodeRunFlagsCapabilities(t *testing.T) {
	inputs, err := parseNodeRunFlags([]string{
		"--node-id", "node-1",
		"--config", "node-custom.toml",
		"--capability", "agent:planner",
		"--capability", "tool:gpu",
	})
	if err != nil {
		t.Fatalf("parseNodeRunFlags() error: %v", err)
	}
	if inputs.ConfigPath != "node-custom.toml" {
		t.Fatalf("config path = %q, want node-custom.toml", inputs.ConfigPath)
	}
	if inputs.Overrides.NodeID == nil || *inputs.Overrides.NodeID != "node-1" {
		t.Fatalf("node override = %#v", inputs.Overrides.NodeID)
	}
	if inputs.Overrides.Capabilities == nil || len(*inputs.Overrides.Capabilities) != 2 {
		t.Fatalf("capability override = %#v", inputs.Overrides.Capabilities)
	}
	if (*inputs.Overrides.Capabilities)[0] != (scheduler.Capability{Kind: scheduler.CapabilityAgent, Name: "planner"}) {
		t.Fatalf("capability[0] = %+v", (*inputs.Overrides.Capabilities)[0])
	}
	if (*inputs.Overrides.Capabilities)[1] != (scheduler.Capability{Kind: scheduler.CapabilityTool, Name: "gpu"}) {
		t.Fatalf("capability[1] = %+v", (*inputs.Overrides.Capabilities)[1])
	}
}

func TestParseNodeRunFlagsRejectsBadCapability(t *testing.T) {
	if _, err := parseNodeRunFlags([]string{"--capability", "badformat"}); err == nil {
		t.Fatalf("expected parse error for invalid capability")
	}
}

func TestParseNodeConfigureFlags(t *testing.T) {
	inputs, err := parseNodeConfigureFlags([]string{
		"--target-node", "node-remote",
		"--gateway-config", "/etc/gopher/gopher.toml",
		"--node-id", "node-new",
		"--node-nats-url", "nats://10.0.0.2:4222",
		"--node-connect-timeout", "9s",
		"--node-reconnect-wait", "4s",
		"--node-heartbeat-interval", "5s",
		"--capability", "tool:gpu",
		"--capability", "agent:planner",
		"--restart=true",
		"--restart-timeout", "10s",
	})
	if err != nil {
		t.Fatalf("parseNodeConfigureFlags() error: %v", err)
	}
	if inputs.TargetNode != "node-remote" {
		t.Fatalf("target node = %q, want node-remote", inputs.TargetNode)
	}
	if !inputs.Restart {
		t.Fatalf("expected restart=true")
	}
	if inputs.RestartTimeout != 10*time.Second {
		t.Fatalf("restart timeout = %s, want 10s", inputs.RestartTimeout)
	}
	if inputs.Request.NodeID == nil || *inputs.Request.NodeID != "node-new" {
		t.Fatalf("node id request = %#v", inputs.Request.NodeID)
	}
	if inputs.Request.NATS == nil || inputs.Request.NATS.URL == nil || *inputs.Request.NATS.URL != "nats://10.0.0.2:4222" {
		t.Fatalf("nats request = %#v", inputs.Request.NATS)
	}
	if inputs.Request.Runtime == nil || inputs.Request.Runtime.HeartbeatInterval == nil || *inputs.Request.Runtime.HeartbeatInterval != "5s" {
		t.Fatalf("runtime request = %#v", inputs.Request.Runtime)
	}
	if inputs.Request.Capabilities == nil || len(*inputs.Request.Capabilities) != 2 {
		t.Fatalf("capability request = %#v", inputs.Request.Capabilities)
	}
}

func TestParseNodeRestartFlags(t *testing.T) {
	inputs, err := parseNodeRestartFlags([]string{
		"--target-node", "node-remote",
		"--gateway-nats-url", "nats://127.0.0.1:4222",
		"--timeout", "3s",
	})
	if err != nil {
		t.Fatalf("parseNodeRestartFlags() error: %v", err)
	}
	if inputs.TargetNode != "node-remote" {
		t.Fatalf("target node = %q, want node-remote", inputs.TargetNode)
	}
	if inputs.GatewayNATSURL != "nats://127.0.0.1:4222" {
		t.Fatalf("gateway nats url = %q", inputs.GatewayNATSURL)
	}
	if inputs.VerifyTimeout != 3*time.Second {
		t.Fatalf("verify timeout = %s, want 3s", inputs.VerifyTimeout)
	}
}

func TestRunNodeSubcommandHelp(t *testing.T) {
	var out bytes.Buffer
	if err := runNodeSubcommand([]string{"run", "--help"}, &out, io.Discard); err != nil {
		t.Fatalf("runNodeSubcommand(run --help) error: %v", err)
	}
	if got := out.String(); got == "" || !strings.Contains(got, "gopher node run") {
		t.Fatalf("help output missing expected usage text: %q", got)
	}
	if !strings.Contains(out.String(), "gopher node configure") {
		t.Fatalf("help output missing configure usage: %q", out.String())
	}
	if !strings.Contains(out.String(), "gopher node restart") {
		t.Fatalf("help output missing restart usage: %q", out.String())
	}
}

func TestRunNodeConfigSubcommandInit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "node.toml")
	var out bytes.Buffer
	if err := runNodeConfigSubcommand([]string{"init", "--path", target}, &out, io.Discard); err != nil {
		t.Fatalf("runNodeConfigSubcommand(init) error: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read generated config error: %v", err)
	}
	if got := string(body); !strings.Contains(got, "[node]") {
		t.Fatalf("generated config missing node section: %q", got)
	}
}

func TestRunNodeConfigSubcommandInitForceRollsBackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "node.toml")
	original := []byte("[node]\nnode_id = \"original\"\n")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatalf("seed config error: %v", err)
	}

	prevWrite := configFileWrite
	configFileWrite = func(path string, data []byte, perm os.FileMode) error {
		if path == target {
			return fmt.Errorf("forced write failure")
		}
		return prevWrite(path, data, perm)
	}
	defer func() {
		configFileWrite = prevWrite
	}()

	var out bytes.Buffer
	err := runNodeConfigSubcommand([]string{"init", "--path", target, "--force"}, &out, io.Discard)
	if err == nil {
		t.Fatalf("expected write failure")
	}

	body, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read restored config error: %v", readErr)
	}
	if string(body) != string(original) {
		t.Fatalf("config rollback failed: got %q want %q", string(body), string(original))
	}
}

func TestRunNodeConfigureSubcommandSendsConfigurePayload(t *testing.T) {
	bus := fabricts.NewInMemoryBus()
	requests := make(chan node.AdminRequest, 1)
	_, err := bus.Subscribe(fabricts.NodeAdminSubject("node-remote"), func(ctx context.Context, message fabricts.Message) {
		var request node.AdminRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests <- request
		respondAdminRequest(ctx, t, bus, message.Reply, node.AdminResponse{OK: true, PersistedPath: "/tmp/node.toml"})
	})
	if err != nil {
		t.Fatalf("subscribe error: %v", err)
	}

	prevClient := newNodeAdminClient
	defer func() { newNodeAdminClient = prevClient }()
	newNodeAdminClient = func(opts fabricts.ClientOptions) (fabricts.Fabric, func() error, error) {
		_ = opts
		return bus, func() error { return nil }, nil
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	err = runNodeConfigureSubcommand([]string{
		"--target-node", "node-remote",
		"--gateway-nats-url", "nats://127.0.0.1:4222",
		"--node-id", "node-new",
		"--node-heartbeat-interval", "4s",
		"--capability", "tool:gpu",
		"--restart=false",
	}, &out, &stderr)
	if err != nil {
		t.Fatalf("runNodeConfigureSubcommand() error: %v", err)
	}

	select {
	case request := <-requests:
		if request.Action != node.AdminActionConfigure {
			t.Fatalf("action = %q, want configure", request.Action)
		}
		if request.Configure == nil || request.Configure.NodeID == nil || *request.Configure.NodeID != "node-new" {
			t.Fatalf("configure payload = %#v", request.Configure)
		}
		if request.Configure.Runtime == nil || request.Configure.Runtime.HeartbeatInterval == nil || *request.Configure.Runtime.HeartbeatInterval != "4s" {
			t.Fatalf("runtime payload = %#v", request.Configure.Runtime)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for configure request")
	}
	if !strings.Contains(out.String(), "configured node node-remote") {
		t.Fatalf("stdout missing configure line: %q", out.String())
	}
	if !strings.Contains(out.String(), "persisted_path=/tmp/node.toml") {
		t.Fatalf("stdout missing persisted path: %q", out.String())
	}
}

func TestRunNodeConfigureSubcommandChainsRestart(t *testing.T) {
	bus := fabricts.NewInMemoryBus()
	_, err := bus.Subscribe(fabricts.NodeAdminSubject("node-remote"), func(ctx context.Context, message fabricts.Message) {
		var request node.AdminRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		switch request.Action {
		case node.AdminActionConfigure:
			respondAdminRequest(ctx, t, bus, message.Reply, node.AdminResponse{OK: true, PersistedPath: "/tmp/node.toml"})
		case node.AdminActionRestart:
			respondAdminRequest(ctx, t, bus, message.Reply, node.AdminResponse{OK: true, RestartRequested: true})
			go func() {
				time.Sleep(10 * time.Millisecond)
				_ = bus.Publish(context.Background(), fabricts.Message{
					Subject: fabricts.NodeStatusSubject("node-remote"),
					Data:    []byte(`{"ok":true}`),
				})
			}()
		default:
			respondAdminRequest(ctx, t, bus, message.Reply, node.AdminResponse{OK: false, Error: "unexpected action"})
		}
	})
	if err != nil {
		t.Fatalf("subscribe error: %v", err)
	}

	prevClient := newNodeAdminClient
	defer func() { newNodeAdminClient = prevClient }()
	newNodeAdminClient = func(opts fabricts.ClientOptions) (fabricts.Fabric, func() error, error) {
		_ = opts
		return bus, func() error { return nil }, nil
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	err = runNodeConfigureSubcommand([]string{
		"--target-node", "node-remote",
		"--gateway-nats-url", "nats://127.0.0.1:4222",
		"--node-heartbeat-interval", "4s",
		"--capability", "tool:gpu",
		"--restart-timeout", "200ms",
	}, &out, &stderr)
	if err != nil {
		t.Fatalf("runNodeConfigureSubcommand() error: %v", err)
	}
	if !strings.Contains(out.String(), "restart requested for node node-remote") {
		t.Fatalf("stdout missing restart request line: %q", out.String())
	}
	if !strings.Contains(out.String(), "node node-remote re-registered") {
		t.Fatalf("stdout missing re-register line: %q", out.String())
	}
}

func TestRunNodeRestartSubcommandWarnsWhenNodeDoesNotRejoin(t *testing.T) {
	bus := fabricts.NewInMemoryBus()
	_, err := bus.Subscribe(fabricts.NodeAdminSubject("node-remote"), func(ctx context.Context, message fabricts.Message) {
		respondAdminRequest(ctx, t, bus, message.Reply, node.AdminResponse{OK: true, RestartRequested: true})
	})
	if err != nil {
		t.Fatalf("subscribe error: %v", err)
	}

	prevClient := newNodeAdminClient
	defer func() { newNodeAdminClient = prevClient }()
	newNodeAdminClient = func(opts fabricts.ClientOptions) (fabricts.Fabric, func() error, error) {
		_ = opts
		return bus, func() error { return nil }, nil
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	err = runNodeRestartSubcommand([]string{
		"--target-node", "node-remote",
		"--gateway-nats-url", "nats://127.0.0.1:4222",
		"--timeout", "60ms",
	}, &out, &stderr)
	if err != nil {
		t.Fatalf("runNodeRestartSubcommand() error: %v", err)
	}
	if !strings.Contains(stderr.String(), "warning: node node-remote did not re-register") {
		t.Fatalf("stderr missing warning line: %q", stderr.String())
	}
}

func respondAdminRequest(ctx context.Context, t *testing.T, bus *fabricts.InMemoryBus, reply string, response node.AdminResponse) {
	t.Helper()
	blob, err := json.Marshal(response)
	if err != nil {
		t.Errorf("marshal response: %v", err)
		return
	}
	if err := bus.Publish(ctx, fabricts.Message{Subject: reply, Data: blob}); err != nil {
		t.Errorf("publish response: %v", err)
	}
}
