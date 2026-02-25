package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/config"
)

func TestSetGatewayTelegramEnabled(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "gopher.toml")
	if err := os.WriteFile(path, []byte(config.DefaultGatewayTOML()), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	changed, err := setGatewayTelegramEnabled(path, true)
	if err != nil {
		t.Fatalf("setGatewayTelegramEnabled() error: %v", err)
	}
	if !changed {
		t.Fatalf("expected config mutation to report changed")
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(blob)
	if !strings.Contains(text, "[gateway.telegram]") || !strings.Contains(text, "enabled = true") {
		t.Fatalf("expected telegram enabled in config, got: %s", text)
	}

	changed, err = setGatewayTelegramEnabled(path, true)
	if err != nil {
		t.Fatalf("setGatewayTelegramEnabled(second) error: %v", err)
	}
	if changed {
		t.Fatalf("expected no-op when target value already set")
	}
}
