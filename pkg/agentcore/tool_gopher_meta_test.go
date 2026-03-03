package agentcore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	rdebug "runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestBinaryVersionFromBuildInfoParsesLDFlags(t *testing.T) {
	info := &rdebug.BuildInfo{
		Main: rdebug.Module{
			Path:    "github.com/bstncartwright/gopher",
			Version: "(devel)",
		},
		Settings: []rdebug.BuildSetting{
			{Key: "-ldflags", Value: "-s -w -X main.binaryVersion=v1.2.3"},
		},
	}
	if got := binaryVersionFromBuildInfo(info); got != "v1.2.3" {
		t.Fatalf("binaryVersionFromBuildInfo() = %q, want v1.2.3", got)
	}
}

func TestBinaryVersionFromBuildInfoParsesQuotedLDFlags(t *testing.T) {
	info := &rdebug.BuildInfo{
		Main: rdebug.Module{
			Path:    "github.com/bstncartwright/gopher",
			Version: "v0.0.0-20260227100000-deadbeefcafe",
		},
		Settings: []rdebug.BuildSetting{
			{Key: "-ldflags", Value: `-s -w -X \"main.binaryVersion=v1.2.3\"`},
		},
	}
	if got := binaryVersionFromBuildInfo(info); got != "v1.2.3" {
		t.Fatalf("binaryVersionFromBuildInfo() = %q, want v1.2.3", got)
	}
}

func TestBinaryVersionFromBuildInfoSkipsPseudoModuleVersion(t *testing.T) {
	info := &rdebug.BuildInfo{
		Main: rdebug.Module{
			Path:    "github.com/bstncartwright/gopher",
			Version: "v0.0.0-20260227100000-deadbeefcafe",
		},
	}
	if got := binaryVersionFromBuildInfo(info); got != "" {
		t.Fatalf("binaryVersionFromBuildInfo() = %q, want empty", got)
	}
}

func TestSemverVersionFromBuildInfoUsesBinaryVersion(t *testing.T) {
	info := &rdebug.BuildInfo{
		Main: rdebug.Module{
			Path:    "github.com/bstncartwright/gopher",
			Version: "(devel)",
		},
		Settings: []rdebug.BuildSetting{
			{Key: "-ldflags", Value: "-s -w -X main.binaryVersion=v1.2.3"},
			{Key: "vcs.revision", Value: "deadbeef"},
		},
	}
	if got := semverVersionFromBuildInfo(info); got != "v1.2.3" {
		t.Fatalf("semverVersionFromBuildInfo() = %q, want v1.2.3", got)
	}
}

func TestSemverVersionFromBuildInfoStripsPseudoVersionHash(t *testing.T) {
	info := &rdebug.BuildInfo{
		Main: rdebug.Module{
			Path:    "github.com/bstncartwright/gopher",
			Version: "v1.2.3-20260227100000-deadbeefcafe",
		},
	}
	if got := semverVersionFromBuildInfo(info); got != "v1.2.3" {
		t.Fatalf("semverVersionFromBuildInfo() = %q, want v1.2.3", got)
	}
}

func TestGopherMetaToolRunDetectsStaleRuntimeVersion(t *testing.T) {
	dir := t.TempDir()
	currentExecutable := filepath.Join(dir, "current-gopher")
	pathExecutable := filepath.Join(dir, "path-gopher")
	if err := os.WriteFile(currentExecutable, []byte("current"), 0o755); err != nil {
		t.Fatalf("write current executable: %v", err)
	}
	if err := os.WriteFile(pathExecutable, []byte("path"), 0o755); err != nil {
		t.Fatalf("write path executable: %v", err)
	}

	prevNow := gopherMetaNow
	prevStart := gopherMetaProcessStartTime
	prevExecutablePath := gopherMetaExecutablePath
	prevLookPath := gopherMetaLookPath
	prevReadBuildInfo := gopherMetaReadBuildInfo
	prevReadFileBuildInfo := gopherMetaReadFileBuildInfo
	defer func() {
		gopherMetaNow = prevNow
		gopherMetaProcessStartTime = prevStart
		gopherMetaExecutablePath = prevExecutablePath
		gopherMetaLookPath = prevLookPath
		gopherMetaReadBuildInfo = prevReadBuildInfo
		gopherMetaReadFileBuildInfo = prevReadFileBuildInfo
	}()

	gopherMetaNow = func() time.Time { return time.Date(2026, 2, 27, 12, 30, 0, 0, time.UTC) }
	gopherMetaProcessStartTime = time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC)
	gopherMetaExecutablePath = func() (string, error) { return currentExecutable, nil }
	gopherMetaLookPath = func(file string) (string, error) {
		if file != "gopher" {
			return "", errors.New("unexpected binary lookup")
		}
		return pathExecutable, nil
	}
	gopherMetaReadBuildInfo = func() (*rdebug.BuildInfo, bool) {
		return &rdebug.BuildInfo{
			GoVersion: "go1.26.0",
			Main: rdebug.Module{
				Path:    "github.com/bstncartwright/gopher",
				Version: "(devel)",
			},
			Settings: []rdebug.BuildSetting{
				{Key: "-ldflags", Value: "-s -w -X main.binaryVersion=v1.2.3"},
				{Key: "vcs.revision", Value: "rev-old"},
			},
		}, true
	}
	gopherMetaReadFileBuildInfo = func(path string) (*rdebug.BuildInfo, error) {
		switch filepath.Clean(path) {
		case filepath.Clean(currentExecutable):
			return &rdebug.BuildInfo{
				GoVersion: "go1.26.0",
				Main: rdebug.Module{
					Path:    "github.com/bstncartwright/gopher",
					Version: "(devel)",
				},
				Settings: []rdebug.BuildSetting{
					{Key: "-ldflags", Value: "-s -w -X main.binaryVersion=v1.2.4"},
					{Key: "vcs.revision", Value: "rev-new"},
				},
			}, nil
		case filepath.Clean(pathExecutable):
			return &rdebug.BuildInfo{
				GoVersion: "go1.26.0",
				Main: rdebug.Module{
					Path:    "github.com/bstncartwright/gopher",
					Version: "(devel)",
				},
				Settings: []rdebug.BuildSetting{
					{Key: "-ldflags", Value: "-s -w -X main.binaryVersion=v1.2.5"},
					{Key: "vcs.revision", Value: "rev-path"},
				},
			}, nil
		default:
			return nil, errors.New("unexpected path")
		}
	}

	tool := &gopherMetaTool{}
	output, err := tool.Run(context.Background(), ToolInput{})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}

	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map[string]any", output.Result)
	}
	running := mustMap(t, result, "running")
	if got := running["binary_version"]; got != "v1.2.3" {
		t.Fatalf("running.binary_version = %v, want v1.2.3", got)
	}
	if got := running["semver_version"]; got != "v1.2.3" {
		t.Fatalf("running.semver_version = %v, want v1.2.3", got)
	}
	if got := running["build_version"]; got != "(devel)" {
		t.Fatalf("running.build_version = %v, want (devel)", got)
	}

	current := mustMap(t, result, "on_disk_current_executable")
	if got := current["binary_version"]; got != "v1.2.4" {
		t.Fatalf("on_disk_current_executable.binary_version = %v, want v1.2.4", got)
	}
	if got := current["semver_version"]; got != "v1.2.4" {
		t.Fatalf("on_disk_current_executable.semver_version = %v, want v1.2.4", got)
	}

	gopherPath := mustMap(t, result, "on_disk_gopher_path")
	if got := gopherPath["binary_version"]; got != "v1.2.5" {
		t.Fatalf("on_disk_gopher_path.binary_version = %v, want v1.2.5", got)
	}
	if got := gopherPath["semver_version"]; got != "v1.2.5" {
		t.Fatalf("on_disk_gopher_path.semver_version = %v, want v1.2.5", got)
	}

	stale := mustMap(t, result, "stale_runtime")
	detected, _ := stale["detected"].(bool)
	if !detected {
		t.Fatalf("stale_runtime.detected = %v, want true", stale["detected"])
	}
	reason, _ := stale["reason"].(string)
	if !strings.Contains(reason, "v1.2.3") || !strings.Contains(reason, "v1.2.4") {
		t.Fatalf("stale_runtime.reason = %q, want running and disk versions", reason)
	}
}

func mustMap(t *testing.T, root map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := root[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	typed, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("key %q type = %T, want map[string]any", key, value)
	}
	return typed
}
