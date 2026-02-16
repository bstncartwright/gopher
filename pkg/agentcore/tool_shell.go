package agentcore

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

type shellExecTool struct{}

func (t *shellExecTool) Name() string {
	return "shell.exec"
}

func (t *shellExecTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Execute a shell command with structured args.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":         map[string]any{"type": "string"},
				"args":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"cwd":             map[string]any{"type": "string"},
				"timeout_seconds": map[string]any{"type": "integer"},
			},
			"required": []any{"command"},
		},
	}
}

func (t *shellExecTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	command, err := requiredStringArg(input.Args, "command")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}
	cwd, err := requiredStringArg(input.Args, "cwd")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}
	args, err := parseStringSlice(input.Args["args"])
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}

	timeoutSeconds, ok := toInt(input.Args["timeout_seconds"])
	if !ok || timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}

	runCtx := ctx
	cancel := func() {}
	if timeoutSeconds > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, command, args...)
	cmd.Dir = cwd

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	result := map[string]any{
		"command":   command,
		"args":      args,
		"cwd":       cwd,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	}

	if runErr != nil {
		result["error"] = runErr.Error()
		return ToolOutput{Status: ToolStatusError, Result: result}, fmt.Errorf("shell.exec failed: %w", runErr)
	}

	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}
