//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/service"
)

type linuxServiceRuntime struct {
	stdout io.Writer
	stderr io.Writer
}

const (
	gopherGatewayUnitName = "gopher-gateway.service"
	gopherNodeUnitName    = "gopher-node.service"
)

var readUnitStatusForManagedUnit = readUnitStatus

func defaultServiceRuntime(stdout, stderr io.Writer) serviceRuntime {
	return &linuxServiceRuntime{stdout: stdout, stderr: stderr}
}

func (r *linuxServiceRuntime) Install(ctx context.Context, opts serviceInstallOptions) error {
	if strings.TrimSpace(opts.BinaryPath) == "" {
		return fmt.Errorf("binary path is required")
	}
	if strings.TrimSpace(opts.ConfigPath) == "" {
		return fmt.Errorf("config path is required")
	}
	role := strings.ToLower(strings.TrimSpace(opts.Role))
	if role == "" {
		role = "gateway"
	}
	if role != "gateway" && role != "node" {
		return fmt.Errorf("invalid service role %q", opts.Role)
	}
	workingDir := resolveServiceWorkingDir()
	var (
		unit     string
		unitName string
		err      error
	)
	switch role {
	case "gateway":
		unitName = "gopher-gateway.service"
		unit, err = service.RenderGatewayUnit(service.GatewayUnitConfig{
			ExecStart:  fmt.Sprintf("%s gateway run --config %s", opts.BinaryPath, opts.ConfigPath),
			WorkingDir: workingDir,
			EnvFile:    opts.EnvPath,
		})
	case "node":
		unitName = "gopher-node.service"
		unit, err = service.RenderNodeUnit(service.NodeUnitConfig{
			ExecStart:  fmt.Sprintf("%s node run --config %s", opts.BinaryPath, opts.ConfigPath),
			WorkingDir: workingDir,
			EnvFile:    opts.EnvPath,
		})
	}
	if err != nil {
		return err
	}

	if err := os.MkdirAll("/etc/gopher", 0o755); err != nil {
		return fmt.Errorf("create /etc/gopher: %w", err)
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", workingDir, err)
	}
	if err := os.WriteFile(filepath.Join("/etc/systemd/system", unitName), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	updatesEnabled := false
	if role == "gateway" {
		updatesEnabled, err = r.installUpdaterUnits(opts)
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(opts.EnvPath) != "" {
		if err := ensureEnvFile(opts.EnvPath); err != nil {
			return err
		}
	}
	runner := systemctlRunner{}
	if err := runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if err := runner.Run(ctx, "systemctl", "enable", "--now", unitName); err != nil {
		return err
	}
	if updatesEnabled {
		if err := runner.Run(ctx, "systemctl", "enable", "--now", "gopher-gateway-update.timer"); err != nil {
			return err
		}
	}
	fmt.Fprintf(r.stdout, "installed and started %s\n", unitName)
	return nil
}

func resolveServiceWorkingDir() string {
	sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER"))
	if sudoUser != "" {
		if u, err := user.Lookup(sudoUser); err == nil {
			home := strings.TrimSpace(u.HomeDir)
			if home != "" {
				return filepath.Join(home, ".gopher")
			}
		}
	}

	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".gopher")
	}

	return "/root/.gopher"
}

func (r *linuxServiceRuntime) Uninstall(ctx context.Context) error {
	runner := systemctlRunner{}
	_ = runner.Run(ctx, "systemctl", "disable", "--now", "gopher-gateway-update.timer")
	_ = runner.Run(ctx, "systemctl", "disable", "--now", "gopher-gateway-update.service")
	_ = runner.Run(ctx, "systemctl", "disable", "--now", "gopher-gateway.service")
	_ = os.Remove("/etc/systemd/system/gopher-gateway-update.timer")
	_ = os.Remove("/etc/systemd/system/gopher-gateway-update.service")
	_ = os.Remove("/etc/systemd/system/gopher-gateway.service")
	_ = runner.Run(ctx, "systemctl", "daemon-reload")
	fmt.Fprintln(r.stdout, "uninstalled gopher-gateway.service")
	return nil
}

func (r *linuxServiceRuntime) Status(ctx context.Context) error {
	gateway, err := readUnitStatus(ctx, "gopher-gateway.service")
	if err != nil {
		return err
	}
	nats, _ := readUnitStatus(ctx, "nats-server.service")
	updater, _ := readUnitStatus(ctx, "gopher-gateway-update.timer")

	gopherPath, gopherVersion, gopherSHA := readBinaryDetails("gopher")
	natsPath, natsVersion, _ := readBinaryDetails("nats-server")

	fmt.Fprintln(r.stdout, "gopher status")
	fmt.Fprintln(r.stdout, "")
	fmt.Fprintf(r.stdout, "gateway service: %s\n", formatUnitStatus(gateway))
	fmt.Fprintf(r.stdout, "nats service:    %s\n", formatUnitStatus(nats))
	fmt.Fprintf(r.stdout, "update timer:    %s\n", formatUnitStatus(updater))
	fmt.Fprintln(r.stdout, "")
	fmt.Fprintf(r.stdout, "gopher binary:   %s\n", valueOrUnknown(gopherPath))
	fmt.Fprintf(r.stdout, "gopher version:  %s\n", valueOrUnknown(gopherVersion))
	fmt.Fprintf(r.stdout, "gopher sha256:   %s\n", valueOrUnknown(gopherSHA))
	fmt.Fprintf(r.stdout, "nats binary:     %s\n", valueOrUnknown(natsPath))
	fmt.Fprintf(r.stdout, "nats version:    %s\n", valueOrUnknown(natsVersion))

	matrixLine, matrixWarning := readMatrixStatusLine(ctx)
	if matrixLine != "" || matrixWarning != "" {
		fmt.Fprintln(r.stdout, "")
		if matrixLine != "" {
			fmt.Fprintln(r.stdout, matrixLine)
		}
		if matrixWarning != "" {
			fmt.Fprintln(r.stdout, matrixWarning)
		}
	}

	if gateway.LoadState == "not-found" {
		return fmt.Errorf("gopher-gateway.service is not installed")
	}
	if gateway.ActiveState != "active" {
		return fmt.Errorf("gopher-gateway.service is %s", valueOrUnknown(gateway.ActiveState))
	}
	return nil
}

type matrixRuntimeMetrics struct {
	LastInboundTxnAt       string `json:"last_inbound_txn_at"`
	LastOutboundSuccessAt  string `json:"last_outbound_success_at"`
	PresenceEnabled        bool   `json:"presence_enabled"`
	PresenceState          string `json:"presence_state"`
	PresenceLastSuccessAt  string `json:"presence_last_success_at"`
	PresenceFailures       uint64 `json:"presence_failures"`
	PresenceLastError      string `json:"presence_last_error"`
	QueueDepth             int    `json:"queue_depth"`
	OutboundRetries        uint64 `json:"outbound_retries"`
	OutboundDropped        uint64 `json:"outbound_dropped"`
	OutboundReplayPending  int    `json:"outbound_replay_pending"`
	OutboundTransientErrs  uint64 `json:"outbound_transient_errors"`
	OutboundPermanentErrs  uint64 `json:"outbound_permanent_errors"`
	DuplicateTxnSeen       uint64 `json:"duplicate_txn_seen"`
	DuplicateEventsSkipped uint64 `json:"duplicate_events_skipped"`
	ReplayEventsProcessed  uint64 `json:"replay_events_processed"`
	TraceRoomsCreated      uint64 `json:"trace_rooms_created_total"`
	TracePublishSuccess    uint64 `json:"trace_publish_success_total"`
	TracePublishFailure    uint64 `json:"trace_publish_failure_total"`
	TraceInboundIgnored    uint64 `json:"trace_events_ignored_inbound_total"`
	InboundFailures        uint64 `json:"inbound_failures"`
}

func readMatrixStatusLine(ctx context.Context) (line string, warning string) {
	cfg, _, err := config.LoadGatewayConfig(config.GatewayLoadOptions{ConfigPath: "/etc/gopher/gopher.toml"})
	if err != nil || !cfg.Matrix.Enabled {
		return "", ""
	}
	addr := strings.TrimSpace(cfg.Matrix.ListenAddr)
	if addr == "" {
		addr = "127.0.0.1:29328"
	}
	url := fmt.Sprintf("http://%s/_gopher/matrix/metrics", addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Sprintf("matrix bridge: warning (invalid probe request: %v)", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "matrix bridge: degraded", fmt.Sprintf("matrix warning: configured but listener not reachable at %s", addr)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "matrix bridge: degraded", fmt.Sprintf("matrix warning: metrics endpoint returned %s", resp.Status)
	}
	var metrics matrixRuntimeMetrics
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		return "matrix bridge: degraded", fmt.Sprintf("matrix warning: unable to decode metrics (%v)", err)
	}
	line = fmt.Sprintf(
		"matrix bridge:   healthy (queue=%d retries=%d dropped=%d replay_pending=%d inbound_failures=%d presence=%s)",
		metrics.QueueDepth,
		metrics.OutboundRetries,
		metrics.OutboundDropped,
		metrics.OutboundReplayPending,
		metrics.InboundFailures,
		matrixPresenceSummary(metrics),
	)
	if strings.TrimSpace(metrics.LastInboundTxnAt) != "" || strings.TrimSpace(metrics.LastOutboundSuccessAt) != "" {
		warning = fmt.Sprintf(
			"matrix activity: inbound=%s outbound=%s",
			valueOrUnknown(metrics.LastInboundTxnAt),
			valueOrUnknown(metrics.LastOutboundSuccessAt),
		)
	}
	return line, warning
}

func matrixPresenceSummary(metrics matrixRuntimeMetrics) string {
	if !metrics.PresenceEnabled {
		return "disabled"
	}
	state := strings.TrimSpace(metrics.PresenceState)
	if state == "" {
		state = "unknown"
	}
	if strings.TrimSpace(metrics.PresenceLastError) != "" {
		return fmt.Sprintf("%s (failures=%d)", state, metrics.PresenceFailures)
	}
	return state
}

func (r *linuxServiceRuntime) Start(ctx context.Context) error {
	unit, err := resolveManagedServiceUnit(ctx)
	if err != nil {
		return err
	}
	return systemctlRunner{}.Run(ctx, "systemctl", "start", unit)
}

func (r *linuxServiceRuntime) Stop(ctx context.Context) error {
	unit, err := resolveManagedServiceUnit(ctx)
	if err != nil {
		return err
	}
	return systemctlRunner{}.Run(ctx, "systemctl", "stop", unit)
}

func (r *linuxServiceRuntime) Restart(ctx context.Context) error {
	unit, err := resolveManagedServiceUnit(ctx)
	if err != nil {
		return err
	}
	return systemctlRunner{}.Run(ctx, "systemctl", "restart", unit)
}

func (r *linuxServiceRuntime) Logs(ctx context.Context, opts serviceLogsOptions) error {
	unit := strings.TrimSpace(opts.Unit)
	if unit == "" {
		unit = "gopher-gateway.service"
	}
	lines := opts.Lines
	if lines <= 0 {
		lines = 200
	}
	args := []string{"-u", unit, "--no-pager", "-n", fmt.Sprintf("%d", lines)}
	if opts.Follow {
		args = append(args, "-f")
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	return cmd.Run()
}

func ensureEnvFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create env file directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	initial := "GOPHER_GITHUB_TOKEN=\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}
	return nil
}

func (r *linuxServiceRuntime) installUpdaterUnits(opts serviceInstallOptions) (bool, error) {
	cfg, _, err := config.LoadGatewayConfig(config.GatewayLoadOptions{
		ConfigPath: opts.ConfigPath,
	})
	if err != nil {
		return false, fmt.Errorf("load gateway config for updater units: %w", err)
	}
	if !cfg.Update.Enabled {
		return false, nil
	}
	updateService, err := service.RenderUpdateServiceUnit(service.UpdateUnitConfig{
		ExecStart: fmt.Sprintf("%s service update apply --config %s", opts.BinaryPath, opts.ConfigPath),
		EnvFile:   opts.EnvPath,
	})
	if err != nil {
		return false, err
	}
	updateTimer, err := service.RenderUpdateTimerUnit(service.UpdateTimerConfig{
		OnCalendar: durationToOnCalendar(cfg.Update.CheckInterval),
	})
	if err != nil {
		return false, err
	}
	if err := os.WriteFile("/etc/systemd/system/gopher-gateway-update.service", []byte(updateService), 0o644); err != nil {
		return false, fmt.Errorf("write update service unit: %w", err)
	}
	if err := os.WriteFile("/etc/systemd/system/gopher-gateway-update.timer", []byte(updateTimer), 0o644); err != nil {
		return false, fmt.Errorf("write update timer unit: %w", err)
	}
	return true, nil
}

func durationToOnCalendar(d time.Duration) string {
	switch {
	case d <= time.Hour:
		return "hourly"
	case d <= 24*time.Hour:
		return "daily"
	default:
		return "weekly"
	}
}

func resolveManagedServiceUnit(ctx context.Context) (string, error) {
	gateway, err := readUnitStatusForManagedUnit(ctx, gopherGatewayUnitName)
	if err != nil {
		return "", err
	}
	node, err := readUnitStatusForManagedUnit(ctx, gopherNodeUnitName)
	if err != nil {
		return "", err
	}

	gatewayInstalled := gateway.LoadState != "not-found"
	nodeInstalled := node.LoadState != "not-found"

	switch {
	case gatewayInstalled && !nodeInstalled:
		return gopherGatewayUnitName, nil
	case nodeInstalled && !gatewayInstalled:
		return gopherNodeUnitName, nil
	case gatewayInstalled && nodeInstalled:
		if strings.EqualFold(gateway.ActiveState, "active") && !strings.EqualFold(node.ActiveState, "active") {
			return gopherGatewayUnitName, nil
		}
		if strings.EqualFold(node.ActiveState, "active") && !strings.EqualFold(gateway.ActiveState, "active") {
			return gopherNodeUnitName, nil
		}
		return gopherGatewayUnitName, nil
	default:
		return "", fmt.Errorf("no gopher service installed (checked %s and %s)", gopherGatewayUnitName, gopherNodeUnitName)
	}
}

type unitStatus struct {
	LoadState     string
	ActiveState   string
	SubState      string
	UnitFileState string
}

func readUnitStatus(ctx context.Context, unit string) (unitStatus, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "show", unit, "--no-pager",
		"--property=LoadState", "--property=ActiveState", "--property=SubState", "--property=UnitFileState")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if strings.Contains(text, "could not be found") {
			return unitStatus{LoadState: "not-found", ActiveState: "inactive", SubState: "dead", UnitFileState: "not-found"}, nil
		}
		return unitStatus{}, fmt.Errorf("read %s status: %w: %s", unit, err, text)
	}
	status := unitStatus{}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "LoadState":
			status.LoadState = value
		case "ActiveState":
			status.ActiveState = value
		case "SubState":
			status.SubState = value
		case "UnitFileState":
			status.UnitFileState = value
		}
	}
	if status.LoadState == "" {
		status.LoadState = "unknown"
	}
	if status.ActiveState == "" {
		status.ActiveState = "unknown"
	}
	if status.SubState == "" {
		status.SubState = "unknown"
	}
	if status.UnitFileState == "" {
		status.UnitFileState = "unknown"
	}
	return status, nil
}

func formatUnitStatus(status unitStatus) string {
	if status.LoadState == "not-found" {
		return "not installed"
	}
	return fmt.Sprintf("%s/%s (%s)", valueOrUnknown(status.ActiveState), valueOrUnknown(status.SubState), valueOrUnknown(status.UnitFileState))
}

func valueOrUnknown(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "unknown"
	}
	return trimmed
}

func readBinaryDetails(binaryName string) (path string, version string, sha string) {
	path, err := exec.LookPath(binaryName)
	if err != nil {
		return "", "", ""
	}
	version = readBinaryVersion(path)
	sha = readFileSHA256(path)
	return path, version, sha
}

func readBinaryVersion(path string) string {
	cmd := exec.Command("go", "version", "-m", path)
	output, err := cmd.CombinedOutput()
	if err == nil {
		moduleLine := ""
		revision := ""
		for _, raw := range strings.Split(string(output), "\n") {
			line := strings.TrimSpace(raw)
			if strings.HasPrefix(line, "mod\t") && moduleLine == "" {
				moduleLine = strings.TrimPrefix(line, "mod\t")
			}
			if strings.Contains(line, "vcs.revision=") {
				revision = strings.TrimPrefix(line, "build\tvcs.revision=")
			}
		}
		if revision != "" && moduleLine != "" {
			return fmt.Sprintf("%s @ %s", moduleLine, revision)
		}
		if moduleLine != "" {
			return moduleLine
		}
	}

	name := filepath.Base(path)
	versionCmd := exec.Command(path, "--version")
	output, err = versionCmd.CombinedOutput()
	if err == nil {
		text := strings.TrimSpace(string(output))
		if text != "" {
			return text
		}
	}
	return name
}

func readFileSHA256(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return ""
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
