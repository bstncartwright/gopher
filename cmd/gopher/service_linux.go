//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
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
	workingDir := resolveServiceWorkingDir()
	unit, err := service.RenderGatewayUnit(service.GatewayUnitConfig{
		ExecStart:  fmt.Sprintf("%s gateway run --config %s", opts.BinaryPath, opts.ConfigPath),
		WorkingDir: workingDir,
		EnvFile:    opts.EnvPath,
	})
	if err != nil {
		return err
	}

	if err := os.MkdirAll("/etc/gopher", 0o755); err != nil {
		return fmt.Errorf("create /etc/gopher: %w", err)
	}
	if err := os.MkdirAll(workingDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", workingDir, err)
	}
	if err := os.WriteFile("/etc/systemd/system/gopher-gateway.service", []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write systemd unit: %w", err)
	}
	updatesEnabled, err := r.installUpdaterUnits(opts)
	if err != nil {
		return err
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
	if err := runner.Run(ctx, "systemctl", "enable", "--now", "gopher-gateway.service"); err != nil {
		return err
	}
	if updatesEnabled {
		if err := runner.Run(ctx, "systemctl", "enable", "--now", "gopher-gateway-update.timer"); err != nil {
			return err
		}
	}
	fmt.Fprintln(r.stdout, "installed and started gopher-gateway.service")
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
	cmd := exec.CommandContext(ctx, "systemctl", "status", "gopher-gateway.service", "--no-pager")
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	return cmd.Run()
}

func (r *linuxServiceRuntime) Start(ctx context.Context) error {
	return systemctlRunner{}.Run(ctx, "systemctl", "start", "gopher-gateway.service")
}

func (r *linuxServiceRuntime) Stop(ctx context.Context) error {
	return systemctlRunner{}.Run(ctx, "systemctl", "stop", "gopher-gateway.service")
}

func (r *linuxServiceRuntime) Restart(ctx context.Context) error {
	return systemctlRunner{}.Run(ctx, "systemctl", "restart", "gopher-gateway.service")
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
