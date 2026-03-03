package agentcore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGopherUpdateToolRunCheckOnlyUsesCurrentExecutable(t *testing.T) {
	prevExecPath := gopherUpdateExecutablePath
	prevRunCommand := gopherUpdateRunCommand
	defer func() {
		gopherUpdateExecutablePath = prevExecPath
		gopherUpdateRunCommand = prevRunCommand
	}()

	gopherUpdateExecutablePath = func() (string, error) {
		return "/home/exedev/.local/bin/gopher", nil
	}

	var (
		gotExecPath string
		gotArgs     []string
		gotEnv      map[string]string
		gotTimeout  time.Duration
	)
	gopherUpdateRunCommand = func(ctx context.Context, executablePath string, args []string, env map[string]string, timeout time.Duration) (gopherUpdateCommandResult, error) {
		_ = ctx
		gotExecPath = executablePath
		gotArgs = append([]string(nil), args...)
		gotEnv = env
		gotTimeout = timeout
		return gopherUpdateCommandResult{
			Stdout:   "current version: v1.2.3\nlatest version:  v1.2.4\nupdate available\n",
			Stderr:   "",
			ExitCode: 0,
		}, nil
	}

	tool := &gopherUpdateTool{}
	output, err := tool.Run(context.Background(), ToolInput{
		Args: map[string]any{
			"check_only":      true,
			"github_token":    "ghp_example_token",
			"timeout_seconds": 120,
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want %q", output.Status, ToolStatusOK)
	}
	if gotExecPath != "/home/exedev/.local/bin/gopher" {
		t.Fatalf("executable path = %q, want /home/exedev/.local/bin/gopher", gotExecPath)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "update" || gotArgs[1] != "--check" {
		t.Fatalf("args = %#v, want [\"update\", \"--check\"]", gotArgs)
	}
	if gotEnv["GOPHER_GITHUB_UPDATE_TOKEN"] != "ghp_example_token" {
		t.Fatalf("GOPHER_GITHUB_UPDATE_TOKEN = %q, want ghp_example_token", gotEnv["GOPHER_GITHUB_UPDATE_TOKEN"])
	}
	if gotTimeout != 120*time.Second {
		t.Fatalf("timeout = %v, want %v", gotTimeout, 120*time.Second)
	}
}

func TestGopherUpdateToolRunReturnsErrorOnNonZeroExit(t *testing.T) {
	prevExecPath := gopherUpdateExecutablePath
	prevRunCommand := gopherUpdateRunCommand
	defer func() {
		gopherUpdateExecutablePath = prevExecPath
		gopherUpdateRunCommand = prevRunCommand
	}()

	gopherUpdateExecutablePath = func() (string, error) {
		return "/opt/gopher", nil
	}
	gopherUpdateRunCommand = func(ctx context.Context, executablePath string, args []string, env map[string]string, timeout time.Duration) (gopherUpdateCommandResult, error) {
		_ = ctx
		_ = executablePath
		_ = args
		_ = env
		_ = timeout
		return gopherUpdateCommandResult{
			Stdout:   "",
			Stderr:   "permission denied",
			ExitCode: 1,
		}, errors.New("gopher update command failed: exit status 1")
	}

	tool := &gopherUpdateTool{}
	output, err := tool.Run(context.Background(), ToolInput{
		Args: map[string]any{},
	})
	if err == nil {
		t.Fatalf("expected error for non-zero exit")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want %q", output.Status, ToolStatusError)
	}
	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", output.Result)
	}
	if got := result["exit_code"]; got != 1 {
		t.Fatalf("result.exit_code = %v, want 1", got)
	}
}

func TestGopherUpdateToolRunFailsWhenExecutableCannotBeResolved(t *testing.T) {
	prevExecPath := gopherUpdateExecutablePath
	defer func() {
		gopherUpdateExecutablePath = prevExecPath
	}()
	gopherUpdateExecutablePath = func() (string, error) {
		return "", errors.New("cannot resolve executable")
	}

	tool := &gopherUpdateTool{}
	output, err := tool.Run(context.Background(), ToolInput{Args: map[string]any{}})
	if err == nil {
		t.Fatalf("expected error when executable path resolution fails")
	}
	if output.Status != ToolStatusError {
		t.Fatalf("status = %q, want %q", output.Status, ToolStatusError)
	}
}
