package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

type statusOnlyFakeRuntime struct {
	statusCalled  bool
	restartCalled bool
	logsCalled    bool
	logsFollow    bool
}

func (f *statusOnlyFakeRuntime) Install(ctx context.Context, opts serviceInstallOptions) error {
	_ = ctx
	_ = opts
	return nil
}

func (f *statusOnlyFakeRuntime) Uninstall(ctx context.Context) error {
	_ = ctx
	return nil
}

func (f *statusOnlyFakeRuntime) Status(ctx context.Context) error {
	_ = ctx
	f.statusCalled = true
	return nil
}

func (f *statusOnlyFakeRuntime) Start(ctx context.Context) error {
	_ = ctx
	return nil
}

func (f *statusOnlyFakeRuntime) Stop(ctx context.Context) error {
	_ = ctx
	return nil
}

func (f *statusOnlyFakeRuntime) Restart(ctx context.Context) error {
	_ = ctx
	f.restartCalled = true
	return nil
}

func (f *statusOnlyFakeRuntime) Logs(ctx context.Context, opts serviceLogsOptions) error {
	_ = ctx
	f.logsCalled = true
	f.logsFollow = opts.Follow
	return nil
}

func TestRunStatusRoutesToServiceStatus(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()

	fake := &statusOnlyFakeRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}

	var out bytes.Buffer
	if err := run([]string{"status"}, &out, &out); err != nil {
		t.Fatalf("run(status) error: %v", err)
	}
	if !fake.statusCalled {
		t.Fatalf("expected service status to be called")
	}
}

func TestRunRestartRoutesToServiceRestart(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()

	fake := &statusOnlyFakeRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}

	var out bytes.Buffer
	if err := run([]string{"restart"}, &out, &out); err != nil {
		t.Fatalf("run(restart) error: %v", err)
	}
	if !fake.restartCalled {
		t.Fatalf("expected service restart to be called")
	}
}

func TestRunLogsRoutesToServiceLogs(t *testing.T) {
	prev := newServiceRuntime
	defer func() { newServiceRuntime = prev }()

	fake := &statusOnlyFakeRuntime{}
	newServiceRuntime = func(stdout, stderr io.Writer) serviceRuntime {
		_ = stdout
		_ = stderr
		return fake
	}

	var out bytes.Buffer
	if err := run([]string{"logs", "--follow"}, &out, &out); err != nil {
		t.Fatalf("run(logs) error: %v", err)
	}
	if !fake.logsCalled {
		t.Fatalf("expected service logs to be called")
	}
	if !fake.logsFollow {
		t.Fatalf("expected --follow to be propagated")
	}
}

func TestRunVersionPrintsBinaryVersion(t *testing.T) {
	prev := binaryVersion
	binaryVersion = "v9.9.9"
	defer func() { binaryVersion = prev }()

	var out bytes.Buffer
	if err := run([]string{"version"}, &out, io.Discard); err != nil {
		t.Fatalf("run(version) error: %v", err)
	}
	if !strings.Contains(out.String(), "gopher v9.9.9") {
		t.Fatalf("unexpected version output: %q", out.String())
	}
}

func TestRunNodeHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"node", "help"}, &out, io.Discard); err != nil {
		t.Fatalf("run(node help) error: %v", err)
	}
	if !strings.Contains(out.String(), "gopher node run") {
		t.Fatalf("unexpected node help output: %q", out.String())
	}
}
