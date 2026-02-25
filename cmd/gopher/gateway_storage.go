package main

import (
	"os"
	"path/filepath"
	"strings"
)

func resolveGatewayDataDir(workspace string) string {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspace == "" {
		return ".gopher"
	}
	if filepath.Base(workspace) != ".gopher" {
		return filepath.Join(workspace, ".gopher")
	}
	canonical := workspace
	legacy := filepath.Join(workspace, ".gopher")
	if hasGatewayData(canonical) {
		return canonical
	}
	if hasGatewayData(legacy) {
		return legacy
	}
	return canonical
}

func hasGatewayData(dataDir string) bool {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return false
	}
	sessionsPath := filepath.Join(dataDir, "sessions", "conversation_bindings.json")
	if _, err := os.Stat(sessionsPath); err == nil {
		return true
	}
	cronPath := filepath.Join(dataDir, "cron", "jobs.json")
	if _, err := os.Stat(cronPath); err == nil {
		return true
	}
	return false
}
