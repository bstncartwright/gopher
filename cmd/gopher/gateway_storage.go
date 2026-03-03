package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

func resolveGatewayDataDir(workspace string) string {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspace == "" {
		slog.Debug("gateway_storage: workspace empty, using default relative data directory")
		return ".gopher"
	}
	if filepath.Base(workspace) != ".gopher" {
		dataDir := filepath.Join(workspace, ".gopher")
		slog.Debug("gateway_storage: resolved data directory from workspace", "workspace", workspace, "data_dir", dataDir)
		return dataDir
	}
	canonical := workspace
	legacy := filepath.Join(workspace, ".gopher")
	if hasGatewayData(canonical) {
		slog.Debug("gateway_storage: using canonical .gopher path with existing data", "data_dir", canonical)
		return canonical
	}
	if hasGatewayData(legacy) {
		slog.Debug("gateway_storage: using legacy nested .gopher path with existing data", "data_dir", legacy)
		return legacy
	}
	slog.Debug("gateway_storage: no existing data found, using canonical .gopher path", "data_dir", canonical)
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
