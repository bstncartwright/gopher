package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type systemctlRunner struct{}

func (r systemctlRunner) Run(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s failed: %w: %s", command, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
