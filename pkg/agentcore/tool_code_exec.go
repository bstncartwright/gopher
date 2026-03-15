package agentcore

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultCodeExecTimeoutSeconds = 60
	maxCodeExecTimeoutSeconds     = 600
	maxCodeExecOutputBytes        = 1 << 20
	maxCodeExecArtifactBytes      = 256 << 10
)

type codeExecTool struct{}

func (t *codeExecTool) Name() string {
	return "code_exec"
}

func (t *codeExecTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Run a short local program for multi-step analysis or transformation. Prefer it when a small script is simpler than many exec/read rounds. Programs run with the same machine access as gopher.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runtime": map[string]any{
					"type":        "string",
					"description": "Program runtime.",
					"enum":        []any{"bash", "sh", "python", "node"},
				},
				"source": map[string]any{
					"type":        "string",
					"description": "Program source code.",
				},
				"args": map[string]any{
					"type":        "array",
					"description": "Optional positional arguments passed to the program.",
					"items":       map[string]any{"type": "string"},
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Timeout in seconds (default 60, max 600).",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Working directory for the program. Defaults to the agent workspace.",
				},
				"env": map[string]any{
					"type":        "object",
					"description": "Environment variable overrides.",
				},
				"capture_paths": map[string]any{
					"type":        "array",
					"description": "Workspace file paths to read back after the program exits.",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []any{"runtime", "source"},
		},
	}
}

func (t *codeExecTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	runtimeName, err := requiredStringArg(input.Args, "runtime")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}
	spec, err := resolveCodeExecRuntime(runtimeName)
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	source, err := requiredStringArg(input.Args, "source")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}

	timeoutSeconds := defaultCodeExecTimeoutSeconds
	if raw, exists := input.Args["timeout"]; exists {
		if v, ok := toInt(raw); ok {
			timeoutSeconds = v
		}
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultCodeExecTimeoutSeconds
	}
	if timeoutSeconds > maxCodeExecTimeoutSeconds {
		timeoutSeconds = maxCodeExecTimeoutSeconds
	}
	timeoutDuration := time.Duration(timeoutSeconds) * time.Second

	workdir := ""
	if value, ok := optionalStringArg(input.Args, "workdir"); ok && strings.TrimSpace(value) != "" {
		workdir = value
	} else if input.Agent != nil {
		workdir = input.Agent.Workspace
	}

	envMap, err := parseEnvMap(input.Args["env"])
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	args, err := codeExecStringListArg(input.Args, "args")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	capturePaths, err := codeExecStringListArg(input.Args, "capture_paths")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	baseDir := os.TempDir()
	if input.Agent != nil && strings.TrimSpace(input.Agent.Workspace) != "" {
		baseDir = filepath.Join(input.Agent.Workspace, ".gopher", "code_exec")
	}
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, fmt.Errorf("create code_exec dir: %w", err)
	}

	runDir, err := os.MkdirTemp(baseDir, "run-")
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, fmt.Errorf("create temp run dir: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(runDir); removeErr != nil {
			slog.Warn("code_exec: failed to clean run dir", "path", runDir, "error", removeErr)
		}
	}()

	scriptPath := filepath.Join(runDir, "main"+spec.Extension)
	if err := os.WriteFile(scriptPath, []byte(source), 0o755); err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, fmt.Errorf("write script: %w", err)
	}

	commandArgs := append([]string{scriptPath}, args...)
	startTime := time.Now()
	runCtx, cancel := context.WithTimeout(ctx, timeoutDuration)
	defer cancel()

	cmd := exec.CommandContext(runCtx, spec.Command, commandArgs...)
	if strings.TrimSpace(workdir) != "" {
		cmd.Dir = workdir
	}
	if len(envMap) > 0 {
		cmd.Env = cmd.Environ()
		for k, v := range envMap {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	stdout := &limitedBuffer{max: maxCodeExecOutputBytes}
	stderr := &limitedBuffer{max: maxCodeExecOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	slog.Debug("code_exec: running program",
		"runtime", spec.Runtime,
		"command", spec.Command,
		"workdir", cmd.Dir,
		"args_count", len(args),
		"capture_paths", len(capturePaths),
		"timeout_seconds", timeoutSeconds,
		"session_id", sessionIDOrUnknown(input.Session),
	)

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

	artifacts := make([]map[string]any, 0, len(capturePaths))
	for _, capturePath := range capturePaths {
		artifact, artifactErr := readCodeExecArtifact(capturePath)
		if artifactErr != nil {
			artifact = map[string]any{
				"path":   capturePath,
				"exists": false,
				"error":  artifactErr.Error(),
			}
		}
		artifacts = append(artifacts, artifact)
	}

	result := map[string]any{
		"runtime":     spec.Runtime,
		"command":     spec.Command,
		"workdir":     cmd.Dir,
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"exit_code":   exitCode,
		"duration_ms": duration.Milliseconds(),
	}
	if len(artifacts) > 0 {
		result["artifacts"] = artifacts
	}

	slog.Info("code_exec: program complete",
		"runtime", spec.Runtime,
		"exit_code", exitCode,
		"duration_ms", duration.Milliseconds(),
		"stdout_length", len(stdout.String()),
		"stderr_length", len(stderr.String()),
	)

	if exitCode != 0 {
		result["error"] = runErr.Error()
		return ToolOutput{Status: ToolStatusError, Result: result}, fmt.Errorf("code_exec failed: %w", runErr)
	}
	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}

type codeExecRuntimeSpec struct {
	Runtime   string
	Command   string
	Extension string
}

func resolveCodeExecRuntime(raw string) (codeExecRuntimeSpec, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "bash":
		return codeExecRuntimeSpec{Runtime: "bash", Command: "bash", Extension: ".sh"}, nil
	case "sh":
		return codeExecRuntimeSpec{Runtime: "sh", Command: "sh", Extension: ".sh"}, nil
	case "python", "python3":
		return codeExecRuntimeSpec{Runtime: "python", Command: "python3", Extension: ".py"}, nil
	case "node", "javascript", "js":
		return codeExecRuntimeSpec{Runtime: "node", Command: "node", Extension: ".mjs"}, nil
	default:
		return codeExecRuntimeSpec{}, fmt.Errorf("unsupported runtime %q", raw)
	}
}

func codeExecStringListArg(args map[string]any, key string) ([]string, error) {
	raw, exists := args[key]
	if !exists || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	out := make([]string, 0, len(items))
	for idx, item := range items {
		value, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", key, idx)
		}
		out = append(out, value)
	}
	return out, nil
}

func readCodeExecArtifact(path string) (map[string]any, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("artifact path %q is a directory", path)
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	truncated := false
	if len(blob) > maxCodeExecArtifactBytes {
		blob = blob[:maxCodeExecArtifactBytes]
		truncated = true
	}

	artifact := map[string]any{
		"path":        path,
		"exists":      true,
		"bytes":       info.Size(),
		"truncated":   truncated,
		"is_utf8":     utf8.Valid(blob),
		"modified_at": info.ModTime().UTC().Format(time.RFC3339),
	}
	if utf8.Valid(blob) {
		artifact["content"] = string(blob)
		return artifact, nil
	}
	artifact["encoding"] = "base64"
	artifact["content_base64"] = base64.StdEncoding.EncodeToString(blob)
	return artifact, nil
}

func sessionIDOrUnknown(session *Session) string {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return "unknown"
	}
	return session.ID
}
