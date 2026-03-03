package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

func resolveRuntimeWorkspace(workingDir, primaryConfigPath, localConfigPath string) (string, error) {
	for _, configPath := range []string{primaryConfigPath, localConfigPath} {
		candidate := strings.TrimSpace(configPath)
		if candidate == "" {
			continue
		}
		dir := filepath.Dir(candidate)
		abs, err := filepath.Abs(dir)
		if err != nil {
			return "", fmt.Errorf("resolve workspace directory from config %q: %w", candidate, err)
		}
		workspace := filepath.Clean(abs)
		slog.Debug("runtime_workspace: resolved from config file location", "config_path", candidate, "workspace", workspace)
		return workspace, nil
	}

	base := strings.TrimSpace(workingDir)
	if base == "" {
		return "", fmt.Errorf("resolve workspace directory: working directory is required")
	}
	abs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve workspace directory: %w", err)
	}
	workspace := filepath.Clean(abs)
	slog.Debug("runtime_workspace: resolved from working directory fallback", "working_dir", base, "workspace", workspace)
	return workspace, nil
}
