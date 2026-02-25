package main

import (
	"path/filepath"
	"testing"
)

func TestResolveRuntimeWorkspacePrefersPrimaryConfigDir(t *testing.T) {
	workingDir := t.TempDir()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "gopher.toml")

	got, err := resolveRuntimeWorkspace(workingDir, configPath, "")
	if err != nil {
		t.Fatalf("resolveRuntimeWorkspace() error: %v", err)
	}
	if got != configDir {
		t.Fatalf("workspace = %q, want %q", got, configDir)
	}
}

func TestResolveRuntimeWorkspaceFallsBackToLocalConfigDir(t *testing.T) {
	workingDir := t.TempDir()
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "gopher.local.toml")

	got, err := resolveRuntimeWorkspace(workingDir, "", configPath)
	if err != nil {
		t.Fatalf("resolveRuntimeWorkspace() error: %v", err)
	}
	if got != configDir {
		t.Fatalf("workspace = %q, want %q", got, configDir)
	}
}

func TestResolveRuntimeWorkspaceFallsBackToWorkingDir(t *testing.T) {
	workingDir := t.TempDir()
	got, err := resolveRuntimeWorkspace(workingDir, "", "")
	if err != nil {
		t.Fatalf("resolveRuntimeWorkspace() error: %v", err)
	}
	if got != workingDir {
		t.Fatalf("workspace = %q, want %q", got, workingDir)
	}
}

func TestResolveRuntimeWorkspaceRejectsEmptyWorkingDirWhenNoConfig(t *testing.T) {
	if _, err := resolveRuntimeWorkspace("", "", ""); err == nil {
		t.Fatalf("expected error for empty working directory")
	}
}
