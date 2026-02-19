package main

import (
	"bytes"
	"context"
	"io"
	"testing"
)

type fakeServiceRuntime struct {
	installCalled   bool
	installOpts     serviceInstallOptions
	uninstallCalled bool
	statusCalled    bool
	startCalled     bool
	stopCalled      bool
	restartCalled   bool
	logsCalled      bool
}

func (f *fakeServiceRuntime) Install(ctx context.Context, opts serviceInstallOptions) error {
	_ = ctx
	f.installCalled = true
	f.installOpts = opts
	return nil
}

func (f *fakeServiceRuntime) Uninstall(ctx context.Context) error {
	_ = ctx
	f.uninstallCalled = true
	return nil
}

func (f *fakeServiceRuntime) Status(ctx context.Context) error {
	_ = ctx
	f.statusCalled = true
	return nil
}

func (f *fakeServiceRuntime) Start(ctx context.Context) error {
	_ = ctx
	f.startCalled = true
	return nil
}

func (f *fakeServiceRuntime) Stop(ctx context.Context) error {
	_ = ctx
	f.stopCalled = true
	return nil
}

func (f *fakeServiceRuntime) Restart(ctx context.Context) error {
	_ = ctx
	f.restartCalled = true
	return nil
}

func (f *fakeServiceRuntime) Logs(ctx context.Context, opts serviceLogsOptions) error {
	_ = ctx
	_ = opts
	f.logsCalled = true
	return nil
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
