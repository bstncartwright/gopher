package agentcore

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	osExec "os/exec"
	"strings"
	"time"
)

const (
	defaultGopherUpdateTimeoutSeconds = 900
	maxGopherUpdateTimeoutSeconds     = 1800
)

type gopherUpdateCommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

var (
	gopherUpdateExecutablePath = os.Executable
	gopherUpdateRunCommand     = runGopherUpdateCommand
)

type gopherUpdateTool struct{}

func (t *gopherUpdateTool) Name() string {
	return "gopher_update"
}

func (t *gopherUpdateTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Update the current gopher binary via its built-in update command using the running executable path (no PATH lookup required).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"check_only": map[string]any{
					"type":        "boolean",
					"description": "If true, only check for updates (equivalent to `gopher update --check`).",
				},
				"github_token": map[string]any{
					"type":        "string",
					"description": "Optional GitHub token override for this run. When provided, it is passed via GOPHER_GITHUB_UPDATE_TOKEN.",
				},
				"timeout_seconds": map[string]any{
					"type":        "integer",
					"description": "Optional command timeout in seconds (default 900, max 1800).",
				},
			},
		},
	}
}

func (t *gopherUpdateTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	executablePath, err := gopherUpdateExecutablePath()
	if err != nil {
		slog.Error("gopher_update_tool: resolve executable path failed", "error", err)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": fmt.Sprintf("resolve executable path: %v", err)}}, err
	}
	executablePath = strings.TrimSpace(executablePath)
	if executablePath == "" {
		err := fmt.Errorf("resolve executable path: empty path")
		slog.Error("gopher_update_tool: executable path is empty")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	checkOnly := false
	if raw, exists := input.Args["check_only"]; exists {
		value, ok := raw.(bool)
		if !ok {
			err := fmt.Errorf("check_only must be a boolean")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		checkOnly = value
	}

	timeoutSeconds := defaultGopherUpdateTimeoutSeconds
	if raw, exists := input.Args["timeout_seconds"]; exists {
		value, ok := toInt(raw)
		if !ok {
			err := fmt.Errorf("timeout_seconds must be an integer")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		timeoutSeconds = value
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultGopherUpdateTimeoutSeconds
	}
	if timeoutSeconds > maxGopherUpdateTimeoutSeconds {
		timeoutSeconds = maxGopherUpdateTimeoutSeconds
	}

	args := []string{"update"}
	if checkOnly {
		args = append(args, "--check")
	}

	envOverrides := map[string]string{}
	if rawToken, exists := input.Args["github_token"]; exists {
		token, ok := rawToken.(string)
		if !ok {
			err := fmt.Errorf("github_token must be a string")
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		if strings.TrimSpace(token) != "" {
			envOverrides["GOPHER_GITHUB_UPDATE_TOKEN"] = strings.TrimSpace(token)
		}
	}

	slog.Info("gopher_update_tool: running update command",
		"executable_path", executablePath,
		"args", args,
		"check_only", checkOnly,
		"timeout_seconds", timeoutSeconds,
		"has_token_override", envOverrides["GOPHER_GITHUB_UPDATE_TOKEN"] != "",
	)

	result, err := gopherUpdateRunCommand(ctx, executablePath, args, envOverrides, time.Duration(timeoutSeconds)*time.Second)
	output := map[string]any{
		"executable_path": executablePath,
		"args":            args,
		"check_only":      checkOnly,
		"exit_code":       result.ExitCode,
		"stdout":          result.Stdout,
		"stderr":          result.Stderr,
	}
	if err != nil {
		output["error"] = err.Error()
		slog.Error("gopher_update_tool: update command failed",
			"executable_path", executablePath,
			"exit_code", result.ExitCode,
			"error", err,
		)
		return ToolOutput{Status: ToolStatusError, Result: output}, err
	}
	return ToolOutput{Status: ToolStatusOK, Result: output}, nil
}

func runGopherUpdateCommand(ctx context.Context, executablePath string, args []string, env map[string]string, timeout time.Duration) (gopherUpdateCommandResult, error) {
	runCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := osExec.CommandContext(runCtx, executablePath, args...)
	if len(env) > 0 {
		cmd.Env = cmd.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}

	const maxOutputBytes = 1 << 20
	stdout := &limitedBuffer{max: maxOutputBytes}
	stderr := &limitedBuffer{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	result := gopherUpdateCommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: 0,
	}
	if runErr != nil {
		if exitErr, ok := runErr.(*osExec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
		return result, fmt.Errorf("gopher update command failed: %w", runErr)
	}

	return result, nil
}
