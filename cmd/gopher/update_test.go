package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/update"
)

func TestRunUpdateSubcommandAlreadyUpToDate(t *testing.T) {
	restore := stubUpdateDependencies(t)
	defer restore()

	binaryVersion = "v1.2.3"
	latestReleaseForUpdate = func(ctx context.Context, owner, repo, token string) (update.Release, error) {
		_ = ctx
		_ = owner
		_ = repo
		_ = token
		return update.Release{TagName: "v1.2.3"}, nil
	}

	applyCalled := false
	applyReleaseForUpdate = func(ctx context.Context, opts update.ApplyOptions) error {
		_ = ctx
		_ = opts
		applyCalled = true
		return nil
	}

	var out bytes.Buffer
	if err := runUpdateSubcommand([]string{"--check", "--github-token", "token"}, &out, io.Discard); err != nil {
		t.Fatalf("runUpdateSubcommand() error: %v", err)
	}
	if applyCalled {
		t.Fatalf("did not expect update to be applied when already up to date")
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Fatalf("expected output to mention up-to-date status, got: %q", out.String())
	}
}

func TestRunUpdateSubcommandApplyWhenNewerReleaseExists(t *testing.T) {
	restore := stubUpdateDependencies(t)
	defer restore()

	binaryVersion = "v1.2.3"
	latestReleaseForUpdate = func(ctx context.Context, owner, repo, token string) (update.Release, error) {
		_ = ctx
		_ = owner
		_ = repo
		_ = token
		return update.Release{
			TagName: "v1.2.4",
			Assets: []update.ReleaseAsset{
				{Name: "gopher-" + runtime.GOOS + "-" + runtime.GOARCH, URL: "https://example.test/asset"},
				{Name: "checksums.txt", URL: "https://example.test/checksums"},
			},
		}, nil
	}
	executablePathForUpdate = func() (string, error) {
		return "/tmp/gopher", nil
	}

	applyCalled := false
	applyReleaseForUpdate = func(ctx context.Context, opts update.ApplyOptions) error {
		_ = ctx
		applyCalled = true
		if opts.BinaryPath != "/tmp/gopher" {
			t.Fatalf("binary path = %q, want /tmp/gopher", opts.BinaryPath)
		}
		if opts.AssetURL == "" {
			t.Fatalf("expected asset url to be set")
		}
		if opts.Token != "token" {
			t.Fatalf("token = %q, want token", opts.Token)
		}
		return nil
	}

	var out bytes.Buffer
	if err := runUpdateSubcommand([]string{"--github-token", "token"}, &out, io.Discard); err != nil {
		t.Fatalf("runUpdateSubcommand() error: %v", err)
	}
	if !applyCalled {
		t.Fatalf("expected update to be applied")
	}
	if !strings.Contains(out.String(), "updated binary to v1.2.4") {
		t.Fatalf("expected output to mention applied update, got: %q", out.String())
	}
}

func TestRunUpdateSubcommandRejectsUnknownBinaryVersion(t *testing.T) {
	restore := stubUpdateDependencies(t)
	defer restore()

	binaryVersion = "dev"
	var out bytes.Buffer
	err := runUpdateSubcommand([]string{"--check", "--github-token", "token"}, &out, io.Discard)
	if err == nil {
		t.Fatalf("expected unknown binary version to fail")
	}
	if !strings.Contains(err.Error(), "not a release version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunUpdateSubcommandPermissionErrorIncludesSudoHint(t *testing.T) {
	restore := stubUpdateDependencies(t)
	defer restore()

	binaryVersion = "v1.2.3"
	latestReleaseForUpdate = func(ctx context.Context, owner, repo, token string) (update.Release, error) {
		_ = ctx
		_ = owner
		_ = repo
		_ = token
		return update.Release{
			TagName: "v1.2.4",
			Assets: []update.ReleaseAsset{
				{Name: "gopher-" + runtime.GOOS + "-" + runtime.GOARCH, URL: "https://example.test/asset"},
			},
		}, nil
	}
	executablePathForUpdate = func() (string, error) {
		return "/usr/local/bin/gopher", nil
	}
	shouldPromptSudoForUpdate = func() bool { return false }
	applyReleaseForUpdate = func(ctx context.Context, opts update.ApplyOptions) error {
		_ = ctx
		_ = opts
		return fmt.Errorf("write temporary update binary: %w", os.ErrPermission)
	}

	var out bytes.Buffer
	err := runUpdateSubcommand([]string{"--github-token", "token"}, &out, io.Discard)
	if err == nil {
		t.Fatalf("expected permission failure")
	}
	if !strings.Contains(err.Error(), "retry with elevated permissions") {
		t.Fatalf("expected elevated permission hint, got: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo -E") {
		t.Fatalf("expected sudo hint, got: %v", err)
	}
}

func TestRunUpdateSubcommandPermissionErrorRetriesWithSudo(t *testing.T) {
	restore := stubUpdateDependencies(t)
	defer restore()

	binaryVersion = "v1.2.3"
	latestReleaseForUpdate = func(ctx context.Context, owner, repo, token string) (update.Release, error) {
		_ = ctx
		_ = owner
		_ = repo
		_ = token
		return update.Release{
			TagName: "v1.2.4",
			Assets: []update.ReleaseAsset{
				{Name: "gopher-" + runtime.GOOS + "-" + runtime.GOARCH, URL: "https://example.test/asset"},
			},
		}, nil
	}
	executablePathForUpdate = func() (string, error) {
		return "/usr/local/bin/gopher", nil
	}
	shouldPromptSudoForUpdate = func() bool { return true }
	envLookupForUpdate = func(key string) string {
		_ = key
		return ""
	}
	applyReleaseForUpdate = func(ctx context.Context, opts update.ApplyOptions) error {
		_ = ctx
		_ = opts
		return fmt.Errorf("write temporary update binary: %w", os.ErrPermission)
	}

	retried := false
	retryWithSudoForUpdate = func(ctx context.Context, updateArgs []string, stdout, stderr io.Writer) error {
		_ = ctx
		_ = stdout
		_ = stderr
		retried = true
		if len(updateArgs) != 2 || updateArgs[0] != "--github-token" || updateArgs[1] != "token" {
			t.Fatalf("unexpected sudo retry args: %#v", updateArgs)
		}
		return nil
	}

	var out bytes.Buffer
	var errOut bytes.Buffer
	if err := runUpdateSubcommand([]string{"--github-token", "token"}, &out, &errOut); err != nil {
		t.Fatalf("runUpdateSubcommand() error: %v", err)
	}
	if !retried {
		t.Fatalf("expected sudo retry")
	}
	if !strings.Contains(errOut.String(), "retrying with sudo") {
		t.Fatalf("expected retry notice, got: %q", errOut.String())
	}
}

func stubUpdateDependencies(t *testing.T) func() {
	t.Helper()

	prevVersion := binaryVersion
	prevLatestRelease := latestReleaseForUpdate
	prevSelectAsset := selectAssetForUpdate
	prevSelectChecksums := selectChecksumsAssetForUpdate
	prevApply := applyReleaseForUpdate
	prevExecPath := executablePathForUpdate
	prevShouldPromptSudo := shouldPromptSudoForUpdate
	prevRetryWithSudo := retryWithSudoForUpdate
	prevEnvLookup := envLookupForUpdate

	return func() {
		binaryVersion = prevVersion
		latestReleaseForUpdate = prevLatestRelease
		selectAssetForUpdate = prevSelectAsset
		selectChecksumsAssetForUpdate = prevSelectChecksums
		applyReleaseForUpdate = prevApply
		executablePathForUpdate = prevExecPath
		shouldPromptSudoForUpdate = prevShouldPromptSudo
		retryWithSudoForUpdate = prevRetryWithSudo
		envLookupForUpdate = prevEnvLookup
	}
}
