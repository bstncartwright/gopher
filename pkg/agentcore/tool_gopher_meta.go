package agentcore

import (
	"context"
	"crypto/sha256"
	gobuildinfo "debug/buildinfo"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	osExec "os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	rdebug "runtime/debug"
	"strings"
	"time"
)

var (
	gopherMetaProcessStartTime     = time.Now().UTC()
	gopherMetaNow                  = time.Now
	gopherMetaExecutablePath       = os.Executable
	gopherMetaLookPath             = osExec.LookPath
	gopherMetaReadBuildInfo        = rdebug.ReadBuildInfo
	gopherMetaReadFileBuildInfo    = gobuildinfo.ReadFile
	gopherMetaStat                 = os.Stat
	gopherMetaReadFile             = os.ReadFile
	gopherMetaBinaryVersionPattern = regexp.MustCompile(`(?:^|\s)-X(?:=|\s+)(?:["']?)main\.binaryVersion=([^\s"']+)(?:["']?)`)
	gopherMetaReleaseVersion       = regexp.MustCompile(`^v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	gopherMetaPseudoVersion        = regexp.MustCompile(`^v\d+\.\d+\.\d+-\d{14}-[0-9a-f]{12}$`)
	gopherMetaPseudoVersionPrefix  = regexp.MustCompile(`^(v\d+\.\d+\.\d+)-\d{14}-[0-9a-f]{12}$`)
)

type gopherMetaTool struct{}

func (t *gopherMetaTool) Name() string {
	return "gopher_meta"
}

func (t *gopherMetaTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Get gopher runtime and binary metadata (running process version, executable details, and stale-update detection).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"include_sha256": map[string]any{
					"type":        "boolean",
					"description": "If true, include SHA-256 hashes for inspected binaries.",
				},
			},
		},
	}
}

func (t *gopherMetaTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	includeSHA := false
	if raw, exists := input.Args["include_sha256"]; exists {
		value, ok := raw.(bool)
		if !ok {
			err := fmt.Errorf("include_sha256 must be a boolean")
			slog.Error("gopher_meta_tool: invalid include_sha256 type", "value", raw)
			return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
		}
		includeSHA = value
	}

	executablePath, err := gopherMetaExecutablePath()
	if err != nil {
		slog.Error("gopher_meta_tool: resolve executable path failed", "error", err)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": fmt.Sprintf("resolve executable path: %v", err)}}, err
	}
	executablePath = strings.TrimSpace(executablePath)
	if executablePath == "" {
		err := fmt.Errorf("resolve executable path: empty path")
		slog.Error("gopher_meta_tool: executable path is empty")
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	if abs, absErr := filepath.Abs(executablePath); absErr == nil {
		executablePath = abs
	}

	collectedAt := gopherMetaNow().UTC().Format(time.RFC3339)
	running := map[string]any{
		"pid":             os.Getpid(),
		"ppid":            os.Getppid(),
		"started_at":      gopherMetaProcessStartTime.Format(time.RFC3339),
		"goos":            runtime.GOOS,
		"goarch":          runtime.GOARCH,
		"executable_path": executablePath,
	}
	if info, ok := gopherMetaReadBuildInfo(); ok && info != nil {
		running["build_info_available"] = true
		applyBuildInfoMetadata(running, info)
	} else {
		running["build_info_available"] = false
	}

	onDiskCurrent, currentWarnings := inspectBinaryMetadata(executablePath, includeSHA)
	warnings := append([]string{}, currentWarnings...)

	lookedPath := ""
	onDiskGopherPath := map[string]any(nil)
	if path, lookErr := gopherMetaLookPath("gopher"); lookErr != nil {
		warnings = append(warnings, fmt.Sprintf("lookpath gopher: %v", lookErr))
	} else {
		lookedPath = strings.TrimSpace(path)
		if lookedPath != "" {
			if abs, absErr := filepath.Abs(lookedPath); absErr == nil {
				lookedPath = abs
			}
			if sameBinaryPath(lookedPath, executablePath) {
				onDiskGopherPath = onDiskCurrent
			} else {
				var pathWarnings []string
				onDiskGopherPath, pathWarnings = inspectBinaryMetadata(lookedPath, includeSHA)
				warnings = append(warnings, pathWarnings...)
			}
		}
	}

	staleDetected, staleReason := detectStaleRuntime(running, onDiskCurrent)

	result := map[string]any{
		"collected_at":                   collectedAt,
		"running":                        running,
		"on_disk_current_executable":     onDiskCurrent,
		"gopher_path":                    lookedPath,
		"on_disk_gopher_path":            onDiskGopherPath,
		"stale_runtime":                  map[string]any{"detected": staleDetected, "reason": staleReason},
		"gopher_path_matches_executable": lookedPath != "" && sameBinaryPath(lookedPath, executablePath),
	}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}

	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}

func inspectBinaryMetadata(path string, includeSHA bool) (map[string]any, []string) {
	meta := map[string]any{
		"path": path,
	}
	warnings := []string{}

	info, err := gopherMetaStat(path)
	if err != nil {
		meta["exists"] = false
		meta["error"] = err.Error()
		return meta, warnings
	}

	meta["exists"] = true
	meta["size_bytes"] = info.Size()
	meta["mod_time"] = info.ModTime().UTC().Format(time.RFC3339)

	if includeSHA {
		sha, shaErr := sha256ForFile(path)
		if shaErr != nil {
			warnings = append(warnings, fmt.Sprintf("sha256 %s: %v", path, shaErr))
		} else {
			meta["sha256"] = sha
		}
	}

	buildInfo, buildInfoErr := gopherMetaReadFileBuildInfo(path)
	if buildInfoErr != nil {
		meta["build_info_available"] = false
		meta["build_info_error"] = buildInfoErr.Error()
		return meta, warnings
	}
	meta["build_info_available"] = true
	applyBuildInfoMetadata(meta, buildInfo)
	return meta, warnings
}

func sha256ForFile(path string) (string, error) {
	blob, err := gopherMetaReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:]), nil
}

func applyBuildInfoMetadata(target map[string]any, info *rdebug.BuildInfo) {
	if target == nil || info == nil {
		return
	}
	modulePath := strings.TrimSpace(info.Main.Path)
	if modulePath != "" {
		target["module_path"] = modulePath
	}
	moduleVersion := strings.TrimSpace(info.Main.Version)
	if moduleVersion != "" {
		target["module_version"] = moduleVersion
	}
	goVersion := strings.TrimSpace(info.GoVersion)
	if goVersion != "" {
		target["go_version"] = goVersion
	}
	if version := binaryVersionFromBuildInfo(info); version != "" {
		target["binary_version"] = version
	}
	if version := semverVersionFromBuildInfo(info); version != "" {
		target["semver_version"] = version
	}
	if value := strings.TrimSpace(info.Main.Version); value != "" {
		target["build_version"] = value
	}

	settings := buildInfoSettings(info)
	if value := strings.TrimSpace(settings["vcs.revision"]); value != "" {
		target["vcs_revision"] = value
	}
	if value := strings.TrimSpace(settings["vcs.time"]); value != "" {
		target["vcs_time"] = value
	}
	if value := strings.TrimSpace(settings["vcs.modified"]); value != "" {
		target["vcs_modified"] = value
	}
}

func binaryVersionFromBuildInfo(info *rdebug.BuildInfo) string {
	if info == nil {
		return ""
	}
	settings := buildInfoSettings(info)
	ldflags := strings.TrimSpace(settings["-ldflags"])
	ldflags = strings.ReplaceAll(ldflags, `\"`, `"`)
	if ldflags != "" {
		matches := gopherMetaBinaryVersionPattern.FindStringSubmatch(ldflags)
		if len(matches) == 2 {
			value := strings.TrimSpace(strings.Trim(matches[1], `"'`))
			value = strings.TrimPrefix(value, `\"`)
			value = strings.TrimSuffix(value, `\"`)
			if value != "" {
				return value
			}
		}
	}

	moduleVersion := strings.TrimSpace(info.Main.Version)
	if gopherMetaReleaseVersion.MatchString(moduleVersion) && !gopherMetaPseudoVersion.MatchString(moduleVersion) {
		return moduleVersion
	}
	return ""
}

func semverVersionFromBuildInfo(info *rdebug.BuildInfo) string {
	if info == nil {
		return ""
	}
	if value := semverVersionFromString(binaryVersionFromBuildInfo(info)); value != "" {
		return value
	}
	settings := buildInfoSettings(info)
	if value := semverVersionFromString(settings["vcs.tag"]); value != "" {
		return value
	}
	if value := semverVersionFromString(info.Main.Version); value != "" {
		return value
	}
	return ""
}

func semverVersionFromString(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if matches := gopherMetaPseudoVersionPrefix.FindStringSubmatch(value); len(matches) == 2 {
		return matches[1]
	}
	if gopherMetaReleaseVersion.MatchString(value) {
		return value
	}
	return ""
}

func buildInfoSettings(info *rdebug.BuildInfo) map[string]string {
	if info == nil {
		return nil
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		key := strings.TrimSpace(setting.Key)
		if key == "" {
			continue
		}
		settings[key] = strings.TrimSpace(setting.Value)
	}
	return settings
}

func detectStaleRuntime(running map[string]any, onDiskCurrent map[string]any) (bool, string) {
	runningVersion := lookupString(running, "binary_version")
	diskVersion := lookupString(onDiskCurrent, "binary_version")
	if runningVersion != "" && diskVersion != "" && runningVersion != diskVersion {
		return true, fmt.Sprintf("running binary version %s differs from on-disk executable version %s", runningVersion, diskVersion)
	}

	runningRevision := lookupString(running, "vcs_revision")
	diskRevision := lookupString(onDiskCurrent, "vcs_revision")
	if runningRevision != "" && diskRevision != "" && runningRevision != diskRevision {
		return true, fmt.Sprintf("running vcs revision %s differs from on-disk executable revision %s", runningRevision, diskRevision)
	}

	return false, ""
}

func lookupString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	typed, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(typed)
}

func sameBinaryPath(left, right string) bool {
	l := strings.TrimSpace(left)
	r := strings.TrimSpace(right)
	if l == "" || r == "" {
		return false
	}

	if resolved, err := filepath.EvalSymlinks(l); err == nil && strings.TrimSpace(resolved) != "" {
		l = resolved
	}
	if resolved, err := filepath.EvalSymlinks(r); err == nil && strings.TrimSpace(resolved) != "" {
		r = resolved
	}

	return filepath.Clean(l) == filepath.Clean(r)
}
