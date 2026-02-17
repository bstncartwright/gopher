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
