package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

type fakeServiceRuntime struct {
	installCalled   bool
	installOpts     serviceInstallOptions
	installErr      error
	uninstallCalled bool
	uninstallErr    error
	statusCalled    bool
	statusOpts      serviceStatusOptions
	statusErr       error
	startCalled     bool
	startErr        error
	stopCalled      bool
	stopErr         error
	restartCalled   bool
	restartOpts     serviceTargetOptions
	restartErr      error
	logsCalled      bool
	logsOpts        serviceLogsOptions
	logsErr         error
}

func (f *fakeServiceRuntime) Install(ctx context.Context, opts serviceInstallOptions) error {
	_ = ctx
	f.installCalled = true
	f.installOpts = opts
	return f.installErr
}

func (f *fakeServiceRuntime) Uninstall(ctx context.Context) error {
	_ = ctx
	f.uninstallCalled = true
	return f.uninstallErr
}

func (f *fakeServiceRuntime) Status(ctx context.Context, opts serviceStatusOptions) error {
	_ = ctx
	f.statusCalled = true
	f.statusOpts = opts
	return f.statusErr
}

func (f *fakeServiceRuntime) Start(ctx context.Context) error {
	_ = ctx
	f.startCalled = true
	return f.startErr
}

func (f *fakeServiceRuntime) Stop(ctx context.Context) error {
	_ = ctx
	f.stopCalled = true
	return f.stopErr
}

func (f *fakeServiceRuntime) Restart(ctx context.Context, opts serviceTargetOptions) error {
	_ = ctx
	f.restartCalled = true
	f.restartOpts = opts
	return f.restartErr
}

func (f *fakeServiceRuntime) Logs(ctx context.Context, opts serviceLogsOptions) error {
	_ = ctx
	f.logsCalled = true
	f.logsOpts = opts
	return f.logsErr
}

func TestRunServiceSubcommandRoutesInstall(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"install"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(install) error: %v", err)
	}
	if !fake.installCalled {
		t.Fatalf("expected install to be called")
	}
	if fake.installOpts.Role != "gateway" {
		t.Fatalf("install role = %q, want gateway", fake.installOpts.Role)
	}
	if fake.installOpts.ConfigPath != defaultServiceGatewayConfigPath() {
		t.Fatalf("install config path = %q, want %q", fake.installOpts.ConfigPath, defaultServiceGatewayConfigPath())
	}
}

func TestRunServiceSubcommandRoutesRestart(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"restart"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(restart) error: %v", err)
	}
	if !fake.restartCalled {
		t.Fatalf("expected restart to be called")
	}
	if fake.restartOpts.Target != serviceTargetAuto {
		t.Fatalf("restart target = %q, want %q", fake.restartOpts.Target, serviceTargetAuto)
	}
}

func TestRunServiceSubcommandRoutesLogs(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"logs", "--lines", "50"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(logs) error: %v", err)
	}
	if !fake.logsCalled {
		t.Fatalf("expected logs to be called")
	}
	if fake.logsOpts.Target != serviceTargetAuto {
		t.Fatalf("logs target = %q, want %q", fake.logsOpts.Target, serviceTargetAuto)
	}
}

func TestRunServiceSubcommandRoutesInstallNodeRole(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"install", "--role", "node"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(install --role node) error: %v", err)
	}
	if !fake.installCalled {
		t.Fatalf("expected install to be called")
	}
	if fake.installOpts.Role != "node" {
		t.Fatalf("install role = %q, want node", fake.installOpts.Role)
	}
}

func TestRunServiceSubcommandRejectsInvalidInstallRole(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"install", "--role", "invalid"}, &out, &out); err == nil {
		t.Fatalf("expected invalid role error")
	}
	if fake.installCalled {
		t.Fatalf("install should not be called on invalid role")
	}
}

func TestRunServiceSubcommandPermissionErrorRetriesWithSudo(t *testing.T) {
	prevRuntime := newServiceRuntime
	prevShouldPrompt := shouldPromptSudoForService
	prevRetry := retryWithSudoForService
	prevEnvLookup := envLookupForService
	defer func() {
		newServiceRuntime = prevRuntime
		shouldPromptSudoForService = prevShouldPrompt
		retryWithSudoForService = prevRetry
		envLookupForService = prevEnvLookup
	}()

	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	shouldPromptSudoForService = func() bool { return true }
	envLookupForService = func(key string) string {
		_ = key
		return ""
	}
	retried := false
	retryWithSudoForService = func(ctx context.Context, serviceArgs []string, stdout, stderr io.Writer) error {
		_ = ctx
		_ = stdout
		_ = stderr
		retried = true
		if len(serviceArgs) != 1 || serviceArgs[0] != "restart" {
			t.Fatalf("unexpected service sudo retry args: %#v", serviceArgs)
		}
		return nil
	}
	// ensure the retry path is exercised by a permission-like error.
	fake.restartErr = os.ErrPermission

	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"restart"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(restart) error: %v", err)
	}
	if !retried {
		t.Fatalf("expected sudo retry")
	}
	if !strings.Contains(out.String(), "retrying with sudo") {
		t.Fatalf("expected retry notice, got: %q", out.String())
	}
}

func TestRunServiceSubcommandPermissionErrorIncludesSudoHint(t *testing.T) {
	prevRuntime := newServiceRuntime
	prevShouldPrompt := shouldPromptSudoForService
	prevEnvLookup := envLookupForService
	defer func() {
		newServiceRuntime = prevRuntime
		shouldPromptSudoForService = prevShouldPrompt
		envLookupForService = prevEnvLookup
	}()

	fake := &fakeServiceRuntime{restartErr: os.ErrPermission}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	shouldPromptSudoForService = func() bool { return false }
	envLookupForService = func(key string) string {
		_ = key
		return ""
	}

	var out bytes.Buffer
	err := runServiceSubcommand([]string{"restart"}, &out, &out)
	if err == nil {
		t.Fatalf("expected restart permission error")
	}
	if !strings.Contains(err.Error(), "service commands may require elevated permissions") {
		t.Fatalf("expected elevated permissions hint, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo -E") {
		t.Fatalf("expected sudo hint, got: %v", err)
	}
}

func TestRunServiceSubcommandRoutesStatusNodeRole(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"status", "--role", "node"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(status --role node) error: %v", err)
	}
	if !fake.statusCalled {
		t.Fatalf("expected status to be called")
	}
	if fake.statusOpts.Target != serviceTargetNode {
		t.Fatalf("status target = %q, want %q", fake.statusOpts.Target, serviceTargetNode)
	}
}

func TestRunServiceSubcommandRoutesRestartNodeRole(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"restart", "--role", "node"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(restart --role node) error: %v", err)
	}
	if !fake.restartCalled {
		t.Fatalf("expected restart to be called")
	}
	if fake.restartOpts.Target != serviceTargetNode {
		t.Fatalf("restart target = %q, want %q", fake.restartOpts.Target, serviceTargetNode)
	}
}

func TestRunServiceSubcommandRoutesLogsNodeRole(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"logs", "--role", "node"}, &out, &out); err != nil {
		t.Fatalf("runServiceSubcommand(logs --role node) error: %v", err)
	}
	if !fake.logsCalled {
		t.Fatalf("expected logs to be called")
	}
	if fake.logsOpts.Target != serviceTargetNode {
		t.Fatalf("logs target = %q, want %q", fake.logsOpts.Target, serviceTargetNode)
	}
}

func TestRunServiceSubcommandRejectsInvalidStatusRole(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()
	fake := &fakeServiceRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}
	var out bytes.Buffer
	if err := runServiceSubcommand([]string{"status", "--role", "bad"}, &out, &out); err == nil {
		t.Fatalf("expected invalid role error")
	}
	if fake.statusCalled {
		t.Fatalf("status should not be called on invalid role")
	}
}
