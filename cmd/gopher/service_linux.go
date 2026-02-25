//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/scheduler"
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

var readUnitStatusForManagedUnit = func(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
	return readUnitStatus(ctx, scope, unit)
}
var loadGatewayConfigForStatus = func() (config.GatewayConfig, error) {
	cfg, _, err := config.LoadGatewayConfig(config.GatewayLoadOptions{ConfigPath: defaultServiceGatewayConfigPath()})
	return cfg, err
}
var runSystemctlForService = func(ctx context.Context, args ...string) error {
	return systemctlRunner{}.Run(ctx, "systemctl", args...)
}
var removeFileForService = os.Remove
var runJournalctlForService = func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
var runTailForService = func(ctx context.Context, path string, lines int, follow bool, stdout, stderr io.Writer) error {
	if lines <= 0 {
		lines = 200
	}
	args := []string{"-n", fmt.Sprintf("%d", lines), path}
	if follow {
		args = append([]string{"-f"}, args...)
	}
	cmd := exec.CommandContext(ctx, "tail", args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
var serviceGetEUIDForLinux = os.Geteuid
var serviceUserHomeDir = os.UserHomeDir
var releaseVersionPattern = regexp.MustCompile(`\bv\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?\b`)

type serviceSystemdScope struct {
	user bool
}

func resolveServiceSystemdScope() serviceSystemdScope {
	return serviceSystemdScope{user: serviceGetEUIDForLinux() != 0}
}

func (s serviceSystemdScope) systemctlArgs(args ...string) []string {
	if !s.user {
		return args
	}
	return append([]string{"--user"}, args...)
}

func (s serviceSystemdScope) journalctlArgs(args ...string) []string {
	if !s.user {
		return args
	}
	return append([]string{"--user"}, args...)
}

func (s serviceSystemdScope) unitDirectory() (string, error) {
	if !s.user {
		return "/etc/systemd/system", nil
	}
	home, err := serviceUserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home for systemd user units: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("resolve user home for systemd user units: home is empty")
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

func (s serviceSystemdScope) label() string {
	if s.user {
		return "user"
	}
	return "system"
}

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
	scope := resolveServiceSystemdScope()
	unitDir, err := scope.unitDirectory()
	if err != nil {
		return err
	}
	workingDir := resolveServiceWorkingDir()
	var (
		unit     string
		unitName string
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

	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", unitDir, err)
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", workingDir, err)
	}
	if strings.TrimSpace(opts.EnvPath) != "" {
		if err := ensureEnvFile(opts.EnvPath); err != nil {
			return err
		}
	}
	if role == "gateway" {
		if err := ensureGatewayConfigFile(opts.ConfigPath); err != nil {
			return err
		}
		token, err := resolveTelegramTokenForAutoEnable(opts.EnvPath)
		if err != nil {
			return fmt.Errorf("resolve telegram token for auto-enable: %w", err)
		}
		if strings.TrimSpace(token) != "" {
			enabled, err := setGatewayTelegramEnabled(opts.ConfigPath, true)
			if err != nil {
				return fmt.Errorf("auto-enable gateway telegram in %s: %w", opts.ConfigPath, err)
			}
			if enabled {
				fmt.Fprintf(r.stdout, "enabled gateway telegram in %s because %s is set\n", opts.ConfigPath, telegramBotTokenEnvKey)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(unitDir, unitName), []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	updatesEnabled := false
	if role == "gateway" {
		updatesEnabled, err = r.installUpdaterUnits(opts, unitDir)
		if err != nil {
			return err
		}
	}
	if err := runSystemctlForService(ctx, scope.systemctlArgs("daemon-reload")...); err != nil {
		return err
	}
	if err := runSystemctlForService(ctx, scope.systemctlArgs("enable", "--now", unitName)...); err != nil {
		return err
	}
	if updatesEnabled {
		if err := runSystemctlForService(ctx, scope.systemctlArgs("enable", "--now", "gopher-gateway-update.timer")...); err != nil {
			return err
		}
	}
	fmt.Fprintf(r.stdout, "installed and started %s (%s scope)\n", unitName, scope.label())
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
	scope := resolveServiceSystemdScope()
	unitDir, err := scope.unitDirectory()
	if err != nil {
		return err
	}
	var errs []error
	run := func(action string, err error) {
		if err == nil {
			return
		}
		if shouldIgnoreUninstallError(err) {
			return
		}
		errs = append(errs, fmt.Errorf("%s: %w", action, err))
	}

	run("disable timer", runSystemctlForService(ctx, scope.systemctlArgs("disable", "--now", "gopher-gateway-update.timer")...))
	run("disable update service", runSystemctlForService(ctx, scope.systemctlArgs("disable", "--now", "gopher-gateway-update.service")...))
	run("disable gateway service", runSystemctlForService(ctx, scope.systemctlArgs("disable", "--now", "gopher-gateway.service")...))
	run("disable node service", runSystemctlForService(ctx, scope.systemctlArgs("disable", "--now", "gopher-node.service")...))
	run("remove timer unit", removeFileForService(filepath.Join(unitDir, "gopher-gateway-update.timer")))
	run("remove update service unit", removeFileForService(filepath.Join(unitDir, "gopher-gateway-update.service")))
	run("remove gateway service unit", removeFileForService(filepath.Join(unitDir, "gopher-gateway.service")))
	run("remove node service unit", removeFileForService(filepath.Join(unitDir, "gopher-node.service")))
	run("reload systemd daemon", runSystemctlForService(ctx, scope.systemctlArgs("daemon-reload")...))
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	fmt.Fprintln(r.stdout, "uninstalled gopher-gateway.service")
	return nil
}

func shouldIgnoreUninstallError(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	ignorePatterns := []string{
		"unit gopher-gateway-update.timer not loaded",
		"unit gopher-gateway-update.service not loaded",
		"unit gopher-gateway.service not loaded",
		"unit gopher-node.service not loaded",
		"does not exist",
		"not found",
	}
	for _, pattern := range ignorePatterns {
		if strings.Contains(msg, pattern) {
			return true
		}
	}
	return false
}

func (r *linuxServiceRuntime) Status(ctx context.Context, opts serviceStatusOptions) error {
	scope := resolveServiceSystemdScope()
	unit, err := resolveManagedServiceUnit(ctx, scope, opts.Target)
	if err != nil {
		return err
	}
	selected, err := readUnitStatus(ctx, scope, unit)
	if err != nil {
		return err
	}
	nats, _ := readUnitStatus(ctx, serviceSystemdScope{}, "nats-server.service")
	gatewayCfg, gatewayCfgErr := config.GatewayConfig{}, fmt.Errorf("gateway config unavailable")
	if unit == gopherGatewayUnitName {
		gatewayCfg, gatewayCfgErr = loadGatewayConfigForStatus()
	}

	gopherPath, gopherVersion, gopherSHA := readBinaryDetails("gopher")
	natsPath, natsVersion, _ := readBinaryDetails("nats-server")

	fmt.Fprintln(r.stdout, "gopher status")
	fmt.Fprintln(r.stdout, "=============")
	fmt.Fprintln(r.stdout, "")
	printStatusSectionHeader(r.stdout, "service health")
	printStatusStateLine(r.stdout, fmt.Sprintf("%s service", describeManagedServiceUnit(unit)), formatUnitStatus(selected))
	printStatusStateLine(r.stdout, "nats service", formatUnitStatus(nats))
	if unit == gopherGatewayUnitName {
		updater, _ := readUnitStatus(ctx, scope, "gopher-gateway-update.timer")
		printStatusStateLine(r.stdout, "update timer", formatUnitStatus(updater))
	}
	fmt.Fprintln(r.stdout, "")
	printStatusSectionHeader(r.stdout, "binary details")
	printStatusValueLine(r.stdout, "gopher binary", valueOrUnknown(gopherPath))
	printStatusValueLine(r.stdout, "gopher version", valueOrUnknown(gopherVersion))
	printStatusValueLine(r.stdout, "gopher sha256", valueOrUnknown(gopherSHA))
	printStatusValueLine(r.stdout, "nats binary", valueOrUnknown(natsPath))
	printStatusValueLine(r.stdout, "nats version", valueOrUnknown(natsVersion))

	if unit == gopherGatewayUnitName {
		telegramDataDir := resolveGatewayDataDir(resolveServiceWorkingDir())
		telegramLine, telegramWarning := readTelegramStatusLine(ctx, telegramDataDir, gatewayCfg, gatewayCfgErr)
		nodeLines, nodeWarning := readGatewayNodeStatusLines(ctx, gatewayCfg, gatewayCfgErr)
		if telegramLine != "" || telegramWarning != "" || len(nodeLines) > 0 || nodeWarning != "" {
			fmt.Fprintln(r.stdout, "")
			printStatusSectionHeader(r.stdout, "gateway runtime")
			if telegramLine != "" {
				printStatusExternalLine(r.stdout, telegramLine, "INFO")
			}
			if telegramWarning != "" {
				printStatusExternalLine(r.stdout, telegramWarning, "WARN")
			}
			for _, line := range nodeLines {
				printStatusExternalLine(r.stdout, line, "INFO")
			}
			if nodeWarning != "" {
				printStatusExternalLine(r.stdout, nodeWarning, "WARN")
			}
		}
	}

	if selected.LoadState == "not-found" {
		return fmt.Errorf("%s is not installed", unit)
	}
	if selected.ActiveState != "active" {
		return fmt.Errorf("%s is %s", unit, valueOrUnknown(selected.ActiveState))
	}
	return nil
}

func printStatusSectionHeader(out io.Writer, title string) {
	fmt.Fprintln(out, title)
	fmt.Fprintln(out, strings.Repeat("-", len(title)))
}

func printStatusStateLine(out io.Writer, label string, detail string) {
	printStatusRow(out, label, statusBadge(detail), detail)
}

func printStatusValueLine(out io.Writer, label string, value string) {
	printStatusRow(out, label, "INFO", value)
}

func printStatusExternalLine(out io.Writer, raw string, fallbackBadge string) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return
	}
	if strings.HasPrefix(trimmed, "- ") {
		printStatusRow(out, "node", fallbackBadge, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		return
	}
	parts := strings.SplitN(trimmed, ":", 2)
	if len(parts) == 2 {
		label := strings.TrimSpace(parts[0])
		detail := strings.TrimSpace(parts[1])
		printStatusRow(out, label, statusBadgeWithFallback(detail, fallbackBadge), detail)
		return
	}
	printStatusRow(out, "info", statusBadgeWithFallback(trimmed, fallbackBadge), trimmed)
}

func printStatusRow(out io.Writer, label string, badge string, detail string) {
	fmt.Fprintf(out, "  %-16s [%-4s] %s\n", valueOrUnknown(label), valueOrUnknown(badge), valueOrUnknown(detail))
}

func statusBadge(detail string) string {
	return statusBadgeWithFallback(detail, "INFO")
}

func statusBadgeWithFallback(detail string, fallback string) string {
	text := strings.ToLower(strings.TrimSpace(detail))
	switch {
	case text == "":
		return fallback
	case strings.Contains(text, "not installed"), strings.Contains(text, "failed"), strings.Contains(text, "error"):
		return "FAIL"
	case strings.Contains(text, "degraded"), strings.Contains(text, "unknown"), strings.Contains(text, "warning"), strings.Contains(text, "inactive"):
		return "WARN"
	case strings.Contains(text, "healthy"), strings.Contains(text, "active"), strings.Contains(text, "enabled"):
		return "OK"
	default:
		return fallback
	}
}

func readTelegramStatusLine(_ context.Context, dataDir string, cfg config.GatewayConfig, cfgErr error) (line string, warning string) {
	if cfgErr != nil || !cfg.Telegram.Enabled {
		return "", ""
	}
	pairedChatID := strings.TrimSpace(cfg.Telegram.AllowedChatID)
	if dataDir != "" {
		if state, err := readTelegramPairingState(dataDir); err == nil && strings.TrimSpace(state.PairedChatID) != "" {
			pairedChatID = strings.TrimSpace(state.PairedChatID)
		}
	}
	if pairedChatID == "" {
		return "telegram bridge: waiting for pairing", "telegram warning: allowed_chat_id is empty; approve a pending pair with gopher pair approve"
	}
	line = fmt.Sprintf(
		"telegram bridge: healthy (poll_interval=%s poll_timeout=%s allowed_user_id=%s allowed_chat_id=%s)",
		cfg.Telegram.PollInterval,
		cfg.Telegram.PollTimeout,
		cfg.Telegram.AllowedUserID,
		pairedChatID,
	)
	if strings.TrimSpace(cfg.Telegram.BotToken) == "" {
		warning = "telegram warning: bot_token is empty"
	}
	return line, warning
}

func readGatewayNodeStatusLines(ctx context.Context, cfg config.GatewayConfig, cfgErr error) ([]string, string) {
	if cfgErr != nil {
		return nil, ""
	}
	addr := strings.TrimSpace(cfg.Panel.ListenAddr)
	if addr == "" {
		addr = "127.0.0.1:29329"
	}
	url := fmt.Sprintf("http://%s/_gopher/panel/nodes", addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return []string{"known nodes:    unknown"}, fmt.Sprintf("nodes warning: invalid panel request (%v)", err)
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return []string{"known nodes:    unknown"}, fmt.Sprintf("nodes warning: panel endpoint is not reachable at %s", addr)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return []string{"known nodes:    unknown"}, fmt.Sprintf("nodes warning: panel nodes endpoint returned %s", resp.Status)
	}
	var payload struct {
		Nodes []scheduler.NodeInfo `json:"nodes"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return []string{"known nodes:    unknown"}, fmt.Sprintf("nodes warning: unable to decode panel nodes (%v)", err)
	}
	if len(payload.Nodes) == 0 {
		return []string{"known nodes:    none"}, ""
	}
	sort.Slice(payload.Nodes, func(i, j int) bool {
		return strings.TrimSpace(payload.Nodes[i].NodeID) < strings.TrimSpace(payload.Nodes[j].NodeID)
	})
	lines := []string{fmt.Sprintf("known nodes:    %d", len(payload.Nodes))}
	for _, node := range payload.Nodes {
		nodeID := strings.TrimSpace(node.NodeID)
		if nodeID == "" {
			nodeID = "unknown"
		}
		heartbeat := "-"
		if !node.LastHeartbeat.IsZero() {
			heartbeat = node.LastHeartbeat.UTC().Format(time.RFC3339)
		}
		lines = append(lines, fmt.Sprintf(
			"  - %s (%s) heartbeat=%s capabilities=%s",
			nodeID,
			schedulerNodeRole(node.IsGateway),
			heartbeat,
			formatSchedulerCapabilities(node.Capabilities),
		))
	}
	return lines, ""
}

func formatSchedulerCapabilities(capabilities []scheduler.Capability) string {
	if len(capabilities) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		name := strings.TrimSpace(capability.Name)
		if name == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%s", schedulerCapabilityKind(capability.Kind), name))
	}
	if len(parts) == 0 {
		return "-"
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func schedulerCapabilityKind(kind scheduler.CapabilityKind) string {
	switch kind {
	case scheduler.CapabilityAgent:
		return "agent"
	case scheduler.CapabilityTool:
		return "tool"
	case scheduler.CapabilitySystem:
		return "system"
	default:
		return "unknown"
	}
}

func schedulerNodeRole(isGateway bool) string {
	if isGateway {
		return "gateway"
	}
	return "node"
}

func (r *linuxServiceRuntime) Start(ctx context.Context) error {
	scope := resolveServiceSystemdScope()
	unit, err := resolveManagedServiceUnit(ctx, scope, serviceTargetAuto)
	if err != nil {
		return err
	}
	return runSystemctlForService(ctx, scope.systemctlArgs("start", unit)...)
}

func (r *linuxServiceRuntime) Stop(ctx context.Context) error {
	scope := resolveServiceSystemdScope()
	unit, err := resolveManagedServiceUnit(ctx, scope, serviceTargetAuto)
	if err != nil {
		return err
	}
	return runSystemctlForService(ctx, scope.systemctlArgs("stop", unit)...)
}

func (r *linuxServiceRuntime) Restart(ctx context.Context, opts serviceTargetOptions) error {
	scope := resolveServiceSystemdScope()
	unit, err := resolveManagedServiceUnit(ctx, scope, opts.Target)
	if err != nil {
		return err
	}
	return runSystemctlForService(ctx, scope.systemctlArgs("restart", unit)...)
}

func (r *linuxServiceRuntime) Logs(ctx context.Context, opts serviceLogsOptions) error {
	scope := resolveServiceSystemdScope()
	lines := opts.Lines
	if lines <= 0 {
		lines = 200
	}
	unit := strings.TrimSpace(opts.Unit)
	if unit == "" {
		resolvedUnit, err := resolveManagedServiceUnit(ctx, scope, opts.Target)
		if err != nil {
			if scope.user {
				logPath, ok := resolveFallbackLogPathWithoutUnit()
				if ok {
					if r.stderr != nil {
						fmt.Fprintf(r.stderr, "service state unavailable, falling back to log file: %s\n", logPath)
					}
					return runTailForService(ctx, logPath, lines, opts.Follow, r.stdout, r.stderr)
				}
			}
			return err
		}
		unit = resolvedUnit
	}
	if scope.user {
		logPath, ok := resolveServiceLogPath(unit)
		if ok {
			if _, statErr := os.Stat(logPath); statErr == nil {
				if r.stderr != nil {
					fmt.Fprintf(r.stderr, "using runtime log file: %s\n", logPath)
				}
				return runTailForService(ctx, logPath, lines, opts.Follow, r.stdout, r.stderr)
			}
		}
	}
	args := []string{"-u", unit, "--no-pager", "-n", fmt.Sprintf("%d", lines)}
	if opts.Follow {
		args = append(args, "-f")
	}
	err := runJournalctlForService(ctx, scope.journalctlArgs(args...), r.stdout, r.stderr)
	if err == nil || !scope.user {
		return err
	}
	logPath, ok := resolveServiceLogPath(unit)
	if !ok {
		return err
	}
	if _, statErr := os.Stat(logPath); statErr != nil {
		return err
	}
	if r.stderr != nil {
		fmt.Fprintf(r.stderr, "journalctl unavailable, falling back to log file: %s\n", logPath)
	}
	return runTailForService(ctx, logPath, lines, opts.Follow, r.stdout, r.stderr)
}

func resolveFallbackLogPathWithoutUnit() (string, bool) {
	workingDir := strings.TrimSpace(resolveServiceWorkingDir())
	if workingDir == "" {
		return "", false
	}
	logDir := filepath.Join(workingDir, "logs")
	candidates := []string{
		filepath.Join(logDir, "gateway.log"),
		filepath.Join(logDir, "node.log"),
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, true
		}
	}
	return "", false
}

func resolveServiceLogPath(unit string) (string, bool) {
	workingDir := strings.TrimSpace(resolveServiceWorkingDir())
	if workingDir == "" {
		return "", false
	}
	logDir := filepath.Join(workingDir, "logs")
	switch strings.TrimSpace(unit) {
	case gopherGatewayUnitName:
		return filepath.Join(logDir, "gateway.log"), true
	case gopherNodeUnitName:
		return filepath.Join(logDir, "node.log"), true
	default:
		return "", false
	}
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

func ensureGatewayConfigFile(path string) error {
	target := strings.TrimSpace(path)
	if target == "" {
		return fmt.Errorf("gateway config path is required")
	}
	info, err := os.Stat(target)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("gateway config path %s is a directory", target)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat gateway config %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create gateway config directory: %w", err)
	}
	if err := os.WriteFile(target, []byte(config.DefaultGatewayTOML()), 0o644); err != nil {
		return fmt.Errorf("write default gateway config %s: %w", target, err)
	}
	return nil
}

func resolveTelegramTokenForAutoEnable(envPath string) (string, error) {
	if value := strings.TrimSpace(os.Getenv(telegramBotTokenEnvKey)); value != "" {
		return value, nil
	}
	path := strings.TrimSpace(envPath)
	if path == "" {
		return "", nil
	}
	values, err := readEnvFileMap(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(values[telegramBotTokenEnvKey]), nil
}

func (r *linuxServiceRuntime) installUpdaterUnits(opts serviceInstallOptions, unitDir string) (bool, error) {
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
	if err := os.WriteFile(filepath.Join(unitDir, "gopher-gateway-update.service"), []byte(updateService), 0o644); err != nil {
		return false, fmt.Errorf("write update service unit: %w", err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, "gopher-gateway-update.timer"), []byte(updateTimer), 0o644); err != nil {
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

func resolveManagedServiceUnit(ctx context.Context, scope serviceSystemdScope, target serviceTarget) (string, error) {
	gateway, err := readUnitStatusForManagedUnit(ctx, scope, gopherGatewayUnitName)
	if err != nil {
		return "", err
	}
	node, err := readUnitStatusForManagedUnit(ctx, scope, gopherNodeUnitName)
	if err != nil {
		return "", err
	}

	gatewayInstalled := gateway.LoadState != "not-found"
	nodeInstalled := node.LoadState != "not-found"
	switch target {
	case "", serviceTargetAuto:
		// Auto mode below.
	case serviceTargetGateway:
		if !gatewayInstalled {
			return "", fmt.Errorf("%s is not installed", gopherGatewayUnitName)
		}
		return gopherGatewayUnitName, nil
	case serviceTargetNode:
		if !nodeInstalled {
			return "", fmt.Errorf("%s is not installed", gopherNodeUnitName)
		}
		return gopherNodeUnitName, nil
	default:
		return "", fmt.Errorf("invalid service role %q", target)
	}

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

func describeManagedServiceUnit(unit string) string {
	switch unit {
	case gopherGatewayUnitName:
		return "gateway"
	case gopherNodeUnitName:
		return "node"
	default:
		return unit
	}
}

type unitStatus struct {
	LoadState     string
	ActiveState   string
	SubState      string
	UnitFileState string
}

func readUnitStatus(ctx context.Context, scope serviceSystemdScope, unit string) (unitStatus, error) {
	args := scope.systemctlArgs(
		"show",
		unit,
		"--no-pager",
		"--property=LoadState",
		"--property=ActiveState",
		"--property=SubState",
		"--property=UnitFileState",
	)
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		lowerText := strings.ToLower(text)
		if strings.Contains(lowerText, "could not be found") || strings.Contains(lowerText, "not found") {
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
	versionText := ""
	versionCmd := exec.Command(path, "--version")
	versionOutput, versionErr := versionCmd.CombinedOutput()
	if versionErr == nil {
		versionText = strings.TrimSpace(string(versionOutput))
	}

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
		if moduleLine != "" {
			return formatBinaryVersionWithRelease(moduleLine, revision, extractReleaseVersion(versionText))
		}
	}

	if versionText != "" {
		return versionText
	}
	name := filepath.Base(path)
	return name
}

func formatBinaryVersionWithRelease(moduleLine string, revision string, release string) string {
	base := strings.TrimSpace(moduleLine)
	if strings.TrimSpace(revision) != "" {
		base = fmt.Sprintf("%s @ %s", base, strings.TrimSpace(revision))
	}
	release = strings.TrimSpace(release)
	if release == "" || strings.Contains(base, release) {
		return base
	}
	return fmt.Sprintf("%s (release %s)", base, release)
}

func extractReleaseVersion(versionText string) string {
	match := releaseVersionPattern.FindString(strings.TrimSpace(versionText))
	return strings.TrimSpace(match)
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
