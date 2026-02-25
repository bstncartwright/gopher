package service

import (
	"strings"
	"testing"
)

func TestRenderGatewayUnit(t *testing.T) {
	unit, err := RenderGatewayUnit(GatewayUnitConfig{
		ExecStart: "/usr/local/bin/gopher gateway run --config /etc/gopher/gopher.toml",
		EnvFile:   "/etc/gopher/gopher.env",
	})
	if err != nil {
		t.Fatalf("RenderGatewayUnit() error: %v", err)
	}
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/gopher gateway run --config /etc/gopher/gopher.toml") {
		t.Fatalf("missing execstart in unit: %s", unit)
	}
	if !strings.Contains(unit, "EnvironmentFile=-/etc/gopher/gopher.env") {
		t.Fatalf("missing env file in unit: %s", unit)
	}
	if !strings.Contains(unit, "WorkingDirectory=/root/.gopher") {
		t.Fatalf("missing default working directory in unit: %s", unit)
	}
	if strings.Contains(unit, "\nUser=") || strings.Contains(unit, "\nGroup=") {
		t.Fatalf("default unit should not force user/group: %s", unit)
	}
}

func TestRenderUpdateTimerUnit(t *testing.T) {
	unit, err := RenderUpdateTimerUnit(UpdateTimerConfig{OnCalendar: "hourly"})
	if err != nil {
		t.Fatalf("RenderUpdateTimerUnit() error: %v", err)
	}
	if !strings.Contains(unit, "OnCalendar=hourly") {
		t.Fatalf("missing OnCalendar in timer unit: %s", unit)
	}
}

func TestRenderNodeUnit(t *testing.T) {
	unit, err := RenderNodeUnit(NodeUnitConfig{
		ExecStart: "/usr/local/bin/gopher node run --config /etc/gopher/node.toml",
		EnvFile:   "/etc/gopher/gopher.env",
	})
	if err != nil {
		t.Fatalf("RenderNodeUnit() error: %v", err)
	}
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/gopher node run --config /etc/gopher/node.toml") {
		t.Fatalf("missing execstart in node unit: %s", unit)
	}
	if !strings.Contains(unit, "Description=gopher node service") {
		t.Fatalf("missing node description in unit: %s", unit)
	}
}

func TestRenderGatewayUnitIncludesConfiguredIdentity(t *testing.T) {
	unit, err := RenderGatewayUnit(GatewayUnitConfig{
		ExecStart:  "/usr/local/bin/gopher gateway run --config /tmp/gopher.toml",
		User:       "exedev",
		WorkingDir: "/home/exedev/.gopher",
		EnvFile:    "/home/exedev/.gopher/gopher.env",
	})
	if err != nil {
		t.Fatalf("RenderGatewayUnit() error: %v", err)
	}
	if !strings.Contains(unit, "User=exedev") {
		t.Fatalf("missing configured user in unit: %s", unit)
	}
	if !strings.Contains(unit, "Group=exedev") {
		t.Fatalf("missing configured group default in unit: %s", unit)
	}
}
