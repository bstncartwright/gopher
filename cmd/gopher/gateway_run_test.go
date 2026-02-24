package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/config"
	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/panel"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
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
		"--matrix-progress-updates-enabled", "true",
		"--matrix-rich-text-enabled", "false",
		"--matrix-presence-enabled", "true",
		"--matrix-presence-interval", "45s",
		"--matrix-presence-status-msg", "online",
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
	if inputs.Overrides.MatrixProgressUpdates == nil || !*inputs.Overrides.MatrixProgressUpdates {
		t.Fatalf("matrix progress updates override missing or incorrect")
	}
	if inputs.Overrides.MatrixRichTextEnabled == nil || *inputs.Overrides.MatrixRichTextEnabled {
		t.Fatalf("matrix rich text override missing or incorrect")
	}
	if inputs.Overrides.MatrixPresenceEnabled == nil || !*inputs.Overrides.MatrixPresenceEnabled {
		t.Fatalf("matrix presence enabled override missing or incorrect")
	}
	if inputs.Overrides.MatrixPresenceInterval == nil || *inputs.Overrides.MatrixPresenceInterval != 45*time.Second {
		t.Fatalf("matrix presence interval override missing or incorrect")
	}
	if inputs.Overrides.MatrixPresenceStatusMsg == nil || *inputs.Overrides.MatrixPresenceStatusMsg != "online" {
		t.Fatalf("matrix presence status override missing or incorrect")
	}
}

func TestParseGatewayRunFlagsPanelOverrides(t *testing.T) {
	inputs, err := parseGatewayRunFlags([]string{
		"--panel-listen-addr", "127.0.0.1:4010",
		"--panel-capture-thinking", "true",
	})
	if err != nil {
		t.Fatalf("parseGatewayRunFlags() error: %v", err)
	}
	if inputs.Overrides.PanelListenAddr == nil || *inputs.Overrides.PanelListenAddr != "127.0.0.1:4010" {
		t.Fatalf("panel listen addr override = %#v", inputs.Overrides.PanelListenAddr)
	}
	if inputs.Overrides.PanelCaptureThinking == nil || !*inputs.Overrides.PanelCaptureThinking {
		t.Fatalf("panel capture thinking override = %#v", inputs.Overrides.PanelCaptureThinking)
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
	if !strings.Contains(out.String(), "--matrix-presence-enabled") {
		t.Fatalf("help output missing matrix presence flags: %q", out.String())
	}
	if !strings.Contains(out.String(), "--panel-listen-addr") {
		t.Fatalf("help output missing panel flags: %q", out.String())
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

func TestRunGatewayConfigSubcommandInitForceRollsBackOnWriteFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "gopher.toml")
	original := []byte("[gateway]\nnode_id = \"original\"\n")
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
	err := runGatewayConfigSubcommand([]string{"init", "--path", target, "--force"}, &out, io.Discard)
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

func TestBuildRequiredCapabilityResolver(t *testing.T) {
	runtime := &gatewayAgentRuntime{
		DefaultActorID: "planner",
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"planner": {
				Config: agentcore.AgentConfig{
					Execution: agentcore.ExecutionConfig{
						RequiredCapabilities: []string{"tool:gpu", "system:matrix"},
					},
				},
			},
			"writer": {
				Config: agentcore.AgentConfig{},
			},
		},
	}
	resolver, err := buildRequiredCapabilityResolver(runtime)
	if err != nil {
		t.Fatalf("buildRequiredCapabilityResolver() error: %v", err)
	}
	required := resolver(sessionrt.AgentInput{ActorID: "planner"})
	if len(required) != 2 {
		t.Fatalf("required len = %d, want 2", len(required))
	}
	aliased := resolver(sessionrt.AgentInput{ActorID: "agent:planner"})
	if len(aliased) != 2 {
		t.Fatalf("aliased required len = %d, want 2", len(aliased))
	}
	if got := resolver(sessionrt.AgentInput{ActorID: "writer"}); len(got) != 0 {
		t.Fatalf("writer required = %#v, want none", got)
	}
}

func TestBuildRequiredCapabilityResolverRejectsInvalidCapability(t *testing.T) {
	runtime := &gatewayAgentRuntime{
		DefaultActorID: "planner",
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"planner": {
				Config: agentcore.AgentConfig{
					Execution: agentcore.ExecutionConfig{
						RequiredCapabilities: []string{"badformat"},
					},
				},
			},
		},
	}
	if _, err := buildRequiredCapabilityResolver(runtime); err == nil {
		t.Fatalf("expected invalid required capability error")
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

type fakePanelRuntime struct {
	calls atomic.Int64
}

func (f *fakePanelRuntime) RunWithRetry(ctx context.Context) error {
	f.calls.Add(1)
	<-ctx.Done()
	return nil
}

func TestStartGatewayPanelInvokedInLimitedMode(t *testing.T) {
	prev := newGatewayPanel
	defer func() { newGatewayPanel = prev }()

	fakeRuntime := &fakePanelRuntime{}
	var gotStore panel.SessionStore
	var gotMetadata panel.SessionMetadataResolver
	newGatewayPanel = func(opts panel.ServerOptions) (panelRuntime, error) {
		gotStore = opts.Store
		gotMetadata = opts.SessionMetadata
		return fakeRuntime, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	process := &gatewayProcess{registry: scheduler.NewRegistry(0)}
	cfg := config.GatewayConfig{
		Panel: config.PanelConfig{ListenAddr: "127.0.0.1:29329"},
	}

	if err := startGatewayPanel(ctx, cfg, process, nil, nil, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("startGatewayPanel() error: %v", err)
	}

	deadline := time.After(500 * time.Millisecond)
	for fakeRuntime.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatalf("expected panel runtime RunWithRetry to be called")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	if gotStore != nil {
		t.Fatalf("expected nil session store in limited mode")
	}
	if gotMetadata != nil {
		t.Fatalf("expected nil session metadata resolver in limited mode")
	}
}
