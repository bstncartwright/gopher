//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/config"
)

func TestResolveManagedServiceUnitPrefersInstalledNodeWhenGatewayMissing(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		_ = scope
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceSystemdScope{}, serviceTargetAuto)
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
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		_ = scope
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceSystemdScope{}, serviceTargetAuto)
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
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		_ = scope
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceSystemdScope{}, serviceTargetAuto)
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
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		_ = scope
		switch unit {
		case gopherGatewayUnitName, gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	if _, err := resolveManagedServiceUnit(context.Background(), serviceSystemdScope{}, serviceTargetAuto); err == nil {
		t.Fatalf("expected error when neither service is installed")
	}
}

func TestResolveManagedServiceUnitExplicitNodeRequiresInstall(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		_ = scope
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "not-found", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	if _, err := resolveManagedServiceUnit(context.Background(), serviceSystemdScope{}, serviceTargetNode); err == nil {
		t.Fatalf("expected error when node service is not installed")
	}
}

func TestResolveManagedServiceUnitExplicitNodeWhenInstalled(t *testing.T) {
	prev := readUnitStatusForManagedUnit
	defer func() { readUnitStatusForManagedUnit = prev }()
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		_ = scope
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	unit, err := resolveManagedServiceUnit(context.Background(), serviceSystemdScope{}, serviceTargetNode)
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
		if args[len(args)-1] == "daemon-reload" {
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

func TestResolveServiceSystemdScopeUsesUserScopeWhenNotRoot(t *testing.T) {
	prevGetEUID := serviceGetEUIDForLinux
	defer func() { serviceGetEUIDForLinux = prevGetEUID }()
	serviceGetEUIDForLinux = func() int { return 1000 }

	scope := resolveServiceSystemdScope()
	if !scope.user {
		t.Fatalf("expected user scope for non-root user")
	}
	args := scope.systemctlArgs("status", "gopher-gateway.service")
	if len(args) == 0 || args[0] != "--user" {
		t.Fatalf("expected --user prefixed args, got %#v", args)
	}
}

func TestResolveServiceSystemdScopeUsesSystemScopeWhenRoot(t *testing.T) {
	prevGetEUID := serviceGetEUIDForLinux
	defer func() { serviceGetEUIDForLinux = prevGetEUID }()
	serviceGetEUIDForLinux = func() int { return 0 }

	scope := resolveServiceSystemdScope()
	if scope.user {
		t.Fatalf("expected system scope for root user")
	}
	args := scope.systemctlArgs("status", "gopher-gateway.service")
	if len(args) == 0 || args[0] == "--user" {
		t.Fatalf("expected system-scope args without --user, got %#v", args)
	}
}

func TestExtractReleaseVersion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "gopher output", in: "gopher v0.1.75", want: "v0.1.75"},
		{name: "nats output", in: "nats-server: v2.10.7", want: "v2.10.7"},
		{name: "no version", in: "unknown", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractReleaseVersion(tt.in); got != tt.want {
				t.Fatalf("extractReleaseVersion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatBinaryVersionWithRelease(t *testing.T) {
	got := formatBinaryVersionWithRelease(
		"github.com/bstncartwright/gopher\tv0.0.0-20260225173712-8ea24608f92d",
		"8ea24608f92d4a56ffebb8a68ca4b79c60e01f75",
		"v0.1.75",
	)
	if !strings.Contains(got, "release v0.1.75") {
		t.Fatalf("expected release tag in formatted version, got %q", got)
	}

	got = formatBinaryVersionWithRelease("github.com/bstncartwright/gopher\tv0.1.75", "", "v0.1.75")
	if strings.Count(got, "v0.1.75") != 1 {
		t.Fatalf("expected release tag not duplicated, got %q", got)
	}
}

func TestLinuxServiceLogsUsesUserScopeAndNodeRole(t *testing.T) {
	prevGetEUID := serviceGetEUIDForLinux
	prevReadUnitStatus := readUnitStatusForManagedUnit
	prevRunJournalctl := runJournalctlForService
	prevRunTail := runTailForService
	defer func() {
		serviceGetEUIDForLinux = prevGetEUID
		readUnitStatusForManagedUnit = prevReadUnitStatus
		runJournalctlForService = prevRunJournalctl
		runTailForService = prevRunTail
	}()

	serviceGetEUIDForLinux = func() int { return 1000 }
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		if !scope.user {
			t.Fatalf("expected user scope for non-root logs")
		}
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", UnitFileState: "enabled"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active", SubState: "running", UnitFileState: "enabled"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	var capturedArgs []string
	runJournalctlForService = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		_ = ctx
		_ = stdout
		_ = stderr
		capturedArgs = append([]string{}, args...)
		return nil
	}
	runTailForService = func(ctx context.Context, path string, lines int, follow bool, stdout, stderr io.Writer) error {
		_ = ctx
		_ = path
		_ = lines
		_ = follow
		_ = stdout
		_ = stderr
		t.Fatalf("did not expect fallback tail when journalctl succeeds")
		return nil
	}

	runtime := &linuxServiceRuntime{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	if err := runtime.Logs(context.Background(), serviceLogsOptions{
		Target: serviceTargetNode,
		Lines:  25,
	}); err != nil {
		t.Fatalf("Logs() error: %v", err)
	}
	if len(capturedArgs) == 0 {
		t.Fatalf("expected journalctl args to be captured")
	}
	if capturedArgs[0] != "--user" {
		t.Fatalf("expected --user journalctl scope, got %#v", capturedArgs)
	}
	joined := strings.Join(capturedArgs, " ")
	if !strings.Contains(joined, "-u gopher-node.service") {
		t.Fatalf("expected node unit logs, args=%#v", capturedArgs)
	}
}

func TestLinuxServiceLogsFallsBackToFileWhenUserJournalUnavailable(t *testing.T) {
	prevGetEUID := serviceGetEUIDForLinux
	prevReadUnitStatus := readUnitStatusForManagedUnit
	prevRunJournalctl := runJournalctlForService
	prevRunTail := runTailForService
	defer func() {
		serviceGetEUIDForLinux = prevGetEUID
		readUnitStatusForManagedUnit = prevReadUnitStatus
		runJournalctlForService = prevRunJournalctl
		runTailForService = prevRunTail
	}()

	serviceGetEUIDForLinux = func() int { return 1000 }
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		switch unit {
		case gopherGatewayUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "active", SubState: "running", UnitFileState: "enabled"}, nil
		case gopherNodeUnitName:
			return unitStatus{LoadState: "loaded", ActiveState: "inactive", SubState: "dead", UnitFileState: "enabled"}, nil
		default:
			return unitStatus{}, fmt.Errorf("unexpected unit %q", unit)
		}
	}

	runJournalctlForService = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		_ = ctx
		_ = args
		_ = stdout
		_ = stderr
		t.Fatalf("did not expect journalctl when runtime log file exists")
		return nil
	}

	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".gopher", "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	logPath := filepath.Join(tmp, ".gopher", "logs", "gateway.log")
	if err := os.WriteFile(logPath, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	t.Setenv("HOME", tmp)

	var tailPath string
	runTailForService = func(ctx context.Context, path string, lines int, follow bool, stdout, stderr io.Writer) error {
		_ = ctx
		_ = stdout
		_ = stderr
		tailPath = path
		if lines != 10 {
			t.Fatalf("lines = %d, want 10", lines)
		}
		if follow {
			t.Fatalf("expected follow=false")
		}
		return nil
	}

	var stderr bytes.Buffer
	runtime := &linuxServiceRuntime{stdout: &bytes.Buffer{}, stderr: &stderr}
	if err := runtime.Logs(context.Background(), serviceLogsOptions{
		Target: serviceTargetGateway,
		Lines:  10,
	}); err != nil {
		t.Fatalf("Logs() error: %v", err)
	}
	if tailPath != logPath {
		t.Fatalf("tail fallback path = %q, want %q", tailPath, logPath)
	}
	if !strings.Contains(stderr.String(), "using runtime log file") {
		t.Fatalf("expected runtime log file notice in stderr, got %q", stderr.String())
	}
}

func TestResolveServiceLogPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path, ok := resolveServiceLogPath(gopherGatewayUnitName)
	if !ok || path != filepath.Join(tmp, ".gopher", "logs", "gateway.log") {
		t.Fatalf("gateway log path = %q ok=%v", path, ok)
	}
	path, ok = resolveServiceLogPath(gopherNodeUnitName)
	if !ok || path != filepath.Join(tmp, ".gopher", "logs", "node.log") {
		t.Fatalf("node log path = %q ok=%v", path, ok)
	}
	if _, ok := resolveServiceLogPath("custom.service"); ok {
		t.Fatalf("expected unknown unit to not resolve log path")
	}
}

func TestLinuxServiceLogsFallsBackWithoutUnitWhenStateUnavailable(t *testing.T) {
	prevGetEUID := serviceGetEUIDForLinux
	prevReadUnitStatus := readUnitStatusForManagedUnit
	prevRunJournalctl := runJournalctlForService
	prevRunTail := runTailForService
	defer func() {
		serviceGetEUIDForLinux = prevGetEUID
		readUnitStatusForManagedUnit = prevReadUnitStatus
		runJournalctlForService = prevRunJournalctl
		runTailForService = prevRunTail
	}()

	serviceGetEUIDForLinux = func() int { return 1000 }
	readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
		_ = ctx
		_ = scope
		_ = unit
		return unitStatus{}, errors.New("Failed to connect to bus: No medium found")
	}
	runJournalctlForService = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		_ = ctx
		_ = args
		_ = stdout
		_ = stderr
		t.Fatalf("journalctl should not be called when unit state resolution fails")
		return nil
	}

	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".gopher", "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	logPath := filepath.Join(tmp, ".gopher", "logs", "gateway.log")
	if err := os.WriteFile(logPath, []byte("line1\n"), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	t.Setenv("HOME", tmp)

	var tailPath string
	runTailForService = func(ctx context.Context, path string, lines int, follow bool, stdout, stderr io.Writer) error {
		_ = ctx
		_ = follow
		_ = stdout
		_ = stderr
		tailPath = path
		if lines != 5 {
			t.Fatalf("lines = %d, want 5", lines)
		}
		return nil
	}

	var stderr bytes.Buffer
	runtime := &linuxServiceRuntime{stdout: &bytes.Buffer{}, stderr: &stderr}
	if err := runtime.Logs(context.Background(), serviceLogsOptions{Lines: 5}); err != nil {
		t.Fatalf("Logs() error: %v", err)
	}
	if tailPath != logPath {
		t.Fatalf("tail fallback path = %q, want %q", tailPath, logPath)
	}
	if !strings.Contains(stderr.String(), "service state unavailable, falling back to log file") {
		t.Fatalf("expected state-unavailable fallback notice, got %q", stderr.String())
	}
}

func TestLinuxServiceUninstallRemovesNodeUnitInUserScope(t *testing.T) {
	prevGetEUID := serviceGetEUIDForLinux
	prevHome := serviceUserHomeDir
	prevRunSystemctl := runSystemctlForService
	prevRemoveFile := removeFileForService
	defer func() {
		serviceGetEUIDForLinux = prevGetEUID
		serviceUserHomeDir = prevHome
		runSystemctlForService = prevRunSystemctl
		removeFileForService = prevRemoveFile
	}()

	serviceGetEUIDForLinux = func() int { return 1000 }
	serviceUserHomeDir = func() (string, error) { return "/tmp/gopher-user", nil }

	var systemctlCalls [][]string
	runSystemctlForService = func(ctx context.Context, args ...string) error {
		_ = ctx
		systemctlCalls = append(systemctlCalls, append([]string{}, args...))
		return nil
	}
	var removed []string
	removeFileForService = func(path string) error {
		removed = append(removed, path)
		return nil
	}

	runtime := &linuxServiceRuntime{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	if err := runtime.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() error: %v", err)
	}
	foundDisableNode := false
	for _, call := range systemctlCalls {
		if strings.Join(call, " ") == "--user disable --now gopher-node.service" {
			foundDisableNode = true
			break
		}
	}
	if !foundDisableNode {
		t.Fatalf("expected gopher-node.service to be disabled, calls=%#v", systemctlCalls)
	}
	foundRemoveNode := false
	for _, path := range removed {
		if path == "/tmp/gopher-user/.config/systemd/user/gopher-node.service" {
			foundRemoveNode = true
			break
		}
	}
	if !foundRemoveNode {
		t.Fatalf("expected node unit removal path, removed=%#v", removed)
	}
}
