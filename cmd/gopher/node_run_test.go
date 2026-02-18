package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestRunNodeSubcommandHelp(t *testing.T) {
	var out bytes.Buffer
	if err := runNodeSubcommand([]string{"run", "--help"}, &out, io.Discard); err != nil {
		t.Fatalf("runNodeSubcommand(run --help) error: %v", err)
	}
	if got := out.String(); got == "" || !strings.Contains(got, "gopher node run") {
		t.Fatalf("help output missing expected usage text: %q", got)
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
