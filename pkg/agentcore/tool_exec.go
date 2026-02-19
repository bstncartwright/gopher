package agentcore

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (lb *limitedBuffer) Write(p []byte) (int, error) {
	if lb.buf.Len()+len(p) > lb.max {
		remaining := lb.max - lb.buf.Len()
		if remaining > 0 {
			lb.buf.Write(p[:remaining])
		}
		lb.truncated = true
		return len(p), nil
	}
	return lb.buf.Write(p)
}

func (lb *limitedBuffer) String() string {
	if lb.truncated {
		return lb.buf.String() + "\n[output truncated]"
	}
	return lb.buf.String()
}

type execTool struct{}

func (t *execTool) Name() string {
	return "exec"
}

func (t *execTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Run a shell command in the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":    map[string]any{"type": "string", "description": "The shell command to run."},
				"timeout":    map[string]any{"type": "integer", "description": "Timeout in seconds (default 30, max 1800)."},
				"background": map[string]any{"type": "boolean", "description": "If true, start in background and return session ID."},
				"workdir":    map[string]any{"type": "string", "description": "Working directory for the command."},
				"env":        map[string]any{"type": "object", "description": "Environment variable overrides."},
			},
			"required": []any{"command"},
		},
	}
}

func (t *execTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	command, err := requiredStringArg(input.Args, "command")
	if err != nil {
		slog.Error("exec_tool: command arg required")
		return ToolOutput{Status: ToolStatusError}, err
	}

	timeoutSeconds := 30
	if raw, exists := input.Args["timeout"]; exists {
		if v, ok := toInt(raw); ok {
			timeoutSeconds = v
		}
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	if timeoutSeconds > 1800 {
		timeoutSeconds = 1800
	}
	timeoutDuration := time.Duration(timeoutSeconds) * time.Second

	workdir, _ := optionalStringArg(input.Args, "workdir")

	envMap, err := parseEnvMap(input.Args["env"])
	if err != nil {
		slog.Error("exec_tool: failed to parse env map", "error", err)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	background := false
	if raw, exists := input.Args["background"]; exists {
		if b, ok := raw.(bool); ok {
			background = b
		}
	}

	slog.Debug("exec_tool: preparing execution",
		"command", command,
		"workdir", workdir,
		"timeout_seconds", timeoutSeconds,
		"background", background,
		"env_count", len(envMap),
		"session_id", input.Session.ID,
	)

	if background {
		return t.runBackground(ctx, input, command, workdir, envMap, timeoutDuration)
	}
	return t.runForeground(ctx, command, workdir, envMap, timeoutDuration)
}

func (t *execTool) runForeground(ctx context.Context, command string, workdir string, envMap map[string]string, timeout time.Duration) (ToolOutput, error) {
	startTime := time.Now()
	slog.Debug("exec_tool: running foreground command", "command", command, "workdir", workdir, "timeout", timeout)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-c", command)
	if workdir != "" {
		cmd.Dir = workdir
	}
	if len(envMap) > 0 {
		cmd.Env = cmd.Environ()
		for k, v := range envMap {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	const maxOutputBytes = 1 << 20
	stdout := &limitedBuffer{max: maxOutputBytes}
	stderr := &limitedBuffer{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	duration := time.Since(startTime)
	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	slog.Info("exec_tool: foreground command complete",
		"command", command,
		"exit_code", exitCode,
		"duration_ms", duration.Milliseconds(),
		"stdout_length", len(stdout.String()),
		"stderr_length", len(stderr.String()),
		"success", exitCode == 0,
	)

	result := map[string]any{
		"command":   command,
		"stdout":    stdout.String(),
		"stderr":    stderr.String(),
		"exit_code": exitCode,
	}

	if exitCode != 0 {
		result["error"] = runErr.Error()
		return ToolOutput{Status: ToolStatusError, Result: result}, fmt.Errorf("exec failed: %w", runErr)
	}

	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}

func (t *execTool) runBackground(ctx context.Context, input ToolInput, command string, workdir string, envMap map[string]string, timeout time.Duration) (ToolOutput, error) {
	slog.Debug("exec_tool: starting background command", "command", command, "workdir", workdir, "timeout", timeout)
	session, err := input.Agent.Processes.Start(ctx, command, workdir, envMap, timeout)
	if err != nil {
		slog.Error("exec_tool: failed to start background process", "command", command, "error", err)
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"error": err.Error()},
		}, fmt.Errorf("exec background start: %w", err)
	}

	slog.Info("exec_tool: background process started",
		"command", command,
		"session_id", session.ID,
		"pid", session.PID,
	)

	time.Sleep(100 * time.Millisecond)

	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{
			"status":     "running",
			"session_id": session.ID,
			"pid":        session.PID,
			"command":    command,
		},
	}, nil
}

func parseEnvMap(v any) (map[string]string, error) {
	if v == nil {
		return nil, nil
	}
	switch typed := v.(type) {
	case map[string]string:
		return typed, nil
	case map[string]any:
		out := make(map[string]string, len(typed))
		for k, val := range typed {
			out[k] = fmt.Sprintf("%v", val)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("env must be an object mapping strings to strings")
	}
}
