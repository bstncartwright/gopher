package main

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

type systemctlRunner struct{}

func (r systemctlRunner) Run(ctx context.Context, command string, args ...string) error {
	slog.Debug("systemctl_runner: executing command", "command", command, "args", strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		slog.Error("systemctl_runner: command failed", "command", command, "args", strings.Join(args, " "), "error", err, "output", strings.TrimSpace(string(output)))
		return fmt.Errorf("%s %s failed: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	slog.Debug("systemctl_runner: command completed", "command", command, "args", strings.Join(args, " "), "output_bytes", len(strings.TrimSpace(string(output))))
	return nil
}
