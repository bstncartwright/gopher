package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupProcessLoggingWritesToFileAndStderr(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer

	logger, cleanup, err := setupProcessLogging(tmp, "gateway", &stderr)
	if err != nil {
		t.Fatalf("setupProcessLogging() error: %v", err)
	}
	defer cleanup()

	logger.Printf("stdlog-line")
	slog.Info("slog-line")

	logPath := filepath.Join(tmp, "logs", "gateway.log")
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "stdlog-line") {
		t.Fatalf("expected stdlog line in file, got %q", text)
	}
	if !strings.Contains(text, "slog-line") {
		t.Fatalf("expected slog line in file, got %q", text)
	}
	if !strings.Contains(stderr.String(), "stdlog-line") {
		t.Fatalf("expected stdlog line on stderr, got %q", stderr.String())
	}
}

func TestSetupProcessLoggingRequiresInputs(t *testing.T) {
	if _, cleanup, err := setupProcessLogging("", "gateway", nil); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatalf("expected error for empty working dir")
	}
	if _, cleanup, err := setupProcessLogging(t.TempDir(), "", nil); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatalf("expected error for empty component")
	}
}
