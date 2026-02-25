//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/config"
)

func TestResolveManagedServiceUnitPrefersInstalledNodeWhenGatewayMissing(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit() error: %v", err)
	}
	if unit != gopherNodeUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherNodeUnitName)
	}
}

func TestResolveManagedServiceUnitPrefersGatewayWhenNodeMissing(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit() error: %v", err)
	}
	if unit != gopherGatewayUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherGatewayUnitName)
	}
}

func TestResolveManagedServiceUnitPrefersActiveUnitWhenBothInstalled(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit() error: %v", err)
	}
	if unit != gopherNodeUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherNodeUnitName)
	}
}

func TestResolveManagedServiceUnitErrorsWhenNoServiceInstalled(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName, gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	if _, err := resolveManagedServiceUnit(context.Background(), serviceTargetAuto); err == nil {
		t.Fatalf("expected error when neither service is installed")
	}
}

func TestResolveManagedServiceUnitExplicitNodeRequiresInstall(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	if _, err := resolveManagedServiceUnit(context.Background(), serviceTargetNode); err == nil {
		t.Fatalf("expected error when node service is not installed")
	}
}

func TestResolveManagedServiceUnitExplicitNodeWhenInstalled(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceTargetNode)
	if err != nil {
		t.Fatalf("resolveManagedServiceUnit(node) error: %v", err)
	}
	if unit != gopherNodeUnitName {
		t.Fatalf("unit = %q, want %q", unit, gopherNodeUnitName)
	}
}

func TestLinuxServiceUninstallReturnsPermissionErrors(t *testing.T) {
	prevRunSystemctl := runSystemctlForService
	prevRemoveFile := removeFileForService
	defer func() {
		runSystemctlForService = prevRunSystemctl
		removeFileForService = prevRemoveFile
	}()

	runSystemctlForService = func(ctx context.Context, args ...string) error {
		_ = ctx
		_ = args
		return errors.New("systemctl disable failed: permission denied")
	}
	removeFileForService = func(path string) error {
		_ = path
		return nil
	}

	runtime := &linuxServiceRuntime{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	err := runtime.Uninstall(context.Background())
	if err == nil {
		t.Fatalf("expected uninstall error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "permission denied") {
		t.Fatalf("expected permission-denied error, got: %v", err)
	}
}

func TestLinuxServiceUninstallIgnoresMissingUnitsAndFiles(t *testing.T) {
	prevRunSystemctl := runSystemctlForService
	prevRemoveFile := removeFileForService
	defer func() {
		runSystemctlForService = prevRunSystemctl
		removeFileForService = prevRemoveFile
	}()

	runSystemctlForService = func(ctx context.Context, args ...string) error {
		_ = ctx
		if len(args) == 0 {
			return nil
		}
		if args[0] == "daemon-reload" {
			return nil
		}
		return errors.New("Unit gopher-gateway.service not loaded.")
	}
	removeFileForService = func(path string) error {
		_ = path
		return os.ErrNotExist
	}

	var out bytes.Buffer
	runtime := &linuxServiceRuntime{stdout: &out, stderr: &bytes.Buffer{}}
	if err := runtime.Uninstall(context.Background()); err != nil {
		t.Fatalf("expected uninstall success, got: %v", err)
	}
	if !strings.Contains(out.String(), "uninstalled gopher-gateway.service") {
		t.Fatalf("expected uninstall success output, got %q", out.String())
	}
}

func TestEnsureGatewayConfigFileCreatesDefaultWhenMissing(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "etc", "gopher", "gopher.toml")

	if err := ensureGatewayConfigFile(target); err != nil {
		t.Fatalf("ensureGatewayConfigFile() error: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read created config: %v", err)
	}
	defaultContent := config.DefaultGatewayTOML()
	if string(content) != defaultContent {
		t.Fatalf("created config did not match default template")
	}
}

func TestEnsureGatewayConfigFilePreservesExistingFile(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "gopher.toml")
	initial := []byte("[gateway]\nnode_id = \"custom\"\n")
	if err := os.WriteFile(target, initial, 0o644); err != nil {
		t.Fatalf("write initial config: %v", err)
	}

	if err := ensureGatewayConfigFile(target); err != nil {
		t.Fatalf("ensureGatewayConfigFile() error: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(content) != string(initial) {
		t.Fatalf("expected existing config preserved, got %q", string(content))
	}
}
