package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bstncartwright/gopher/pkg/update"
)

const (
	defaultUpdateRepoOwner = "bstncartwright"
	defaultUpdateRepoName  = "gopher"
)

var (
	latestReleaseForUpdate = func(ctx context.Context, owner, repo, token string) (update.Release, error) {
		client := update.GitHubReleasesClient{
			Owner: owner,
			Repo:  repo,
			Token: token,
		}
		return client.LatestRelease(ctx)
	}
	selectAssetForUpdate          = update.SelectAsset
	selectChecksumsAssetForUpdate = update.SelectChecksumsAsset
	applyReleaseForUpdate         = update.ApplyRelease
	executablePathForUpdate       = os.Executable
	shouldPromptSudoForUpdate     = isInteractiveTerminal
	retryWithSudoForUpdate        = rerunUpdateWithSudo
	envLookupForUpdate            = os.Getenv
	defaultServiceNameForUpdate   = inferDefaultServiceNameForUpdate
	updateGetEUIDForScope         = os.Geteuid
	updateUserHomeDirForScope     = os.UserHomeDir
)

type noopRunner struct{}

func (noopRunner) Run(ctx context.Context, command string, args ...string) error {
	_ = ctx
	_ = command
	_ = args
	return nil
}

type scopedSystemctlRunner struct {
	userScope bool
}

func (r scopedSystemctlRunner) Run(ctx context.Context, command string, args ...string) error {
	if command == "systemctl" && r.userScope {
		args = append([]string{"--user"}, args...)
	}
	return systemctlRunner{}.Run(ctx, command, args...)
}

func runUpdateSubcommand(args []string, stdout, stderr io.Writer) (err error) {
	finishLog := startCommandLog("update", args)
	defer func() {
		finishLog(err)
	}()

	if wantsHelp(args) {
		printUpdateUsage(stdout)
		return nil
	}
	ctx := context.Background()

	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	owner := flags.String("owner", defaultUpdateRepoOwner, "github release owner")
	repo := flags.String("repo", defaultUpdateRepoName, "github release repository")
	token := flags.String("github-token", "", "github token (defaults to GOPHER_GITHUB_TOKEN or GOPHER_GITHUB_UPDATE_TOKEN)")
	binaryPath := flags.String("binary-path", "", "target binary path (defaults to current executable)")
	assetPattern := flags.String("asset-pattern", "", "extra release asset name filter")
	serviceName := flags.String("service-name", "", "optional systemd service to restart after update")
	noServiceRestart := flags.Bool("no-service-restart", false, "disable post-update systemd service restart")
	checkOnly := flags.Bool("check", false, "check for update without applying")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	currentVersion := currentBinaryVersion()
	if !strings.HasPrefix(currentVersion, "v") {
		return fmt.Errorf("current binary version %q is not a release version; build with -ldflags \"-X main.binaryVersion=vX.Y.Z\"", currentVersion)
	}
	ghToken := resolvedGitHubToken(strings.TrimSpace(*token))
	if ghToken == "" {
		return fmt.Errorf("github token is required via --github-token, GOPHER_GITHUB_TOKEN, or GOPHER_GITHUB_UPDATE_TOKEN")
	}
	slog.Info(
		"update: checking latest release",
		"owner", strings.TrimSpace(*owner),
		"repo", strings.TrimSpace(*repo),
		"current_version", currentVersion,
		"check_only", *checkOnly,
	)

	release, err := latestReleaseForUpdate(ctx, strings.TrimSpace(*owner), strings.TrimSpace(*repo), ghToken)
	if err != nil {
		return err
	}
	slog.Info("update: latest release resolved", "latest_tag", release.TagName, "assets_count", len(release.Assets))
	cmp, err := update.CompareVersions(currentVersion, release.TagName)
	if err != nil {
		return err
	}

	fmt.Fprintf(stdout, "current version: %s\n", currentVersion)
	fmt.Fprintf(stdout, "latest version:  %s\n", release.TagName)
	if cmp > 0 {
		fmt.Fprintln(stdout, "current binary is newer than the latest published release; skipping update")
		return nil
	}
	if cmp == 0 {
		fmt.Fprintln(stdout, "already up to date")
		return nil
	}
	if *checkOnly {
		fmt.Fprintln(stdout, "update available")
		return nil
	}

	targetBinaryPath := strings.TrimSpace(*binaryPath)
	if targetBinaryPath == "" {
		targetBinaryPath, err = executablePathForUpdate()
		if err != nil {
			return fmt.Errorf("resolve current executable path: %w", err)
		}
	}
	resolvedServiceName := strings.TrimSpace(*serviceName)
	if *noServiceRestart {
		resolvedServiceName = ""
	} else if resolvedServiceName == "" {
		resolvedServiceName = strings.TrimSpace(defaultServiceNameForUpdate())
	}

	asset, err := selectAssetForUpdate(release, runtime.GOOS, runtime.GOARCH, strings.TrimSpace(*assetPattern))
	if err != nil {
		return err
	}
	checksumsAsset, _ := selectChecksumsAssetForUpdate(release)
	slog.Info(
		"update: applying release",
		"target_binary_path", targetBinaryPath,
		"asset_name", asset.Name,
		"service_restart", resolvedServiceName != "",
		"service_name", resolvedServiceName,
	)

	runner := update.CommandRunner(noopRunner{})
	if resolvedServiceName != "" {
		runner = scopedSystemctlRunner{userScope: inferUpdateUserScope()}
	}
	if err := applyReleaseForUpdate(ctx, update.ApplyOptions{
		BinaryPath:   targetBinaryPath,
		ServiceName:  resolvedServiceName,
		Token:        ghToken,
		AssetURL:     asset.DownloadURL(),
		AssetName:    asset.Name,
		ChecksumsURL: checksumsAsset.DownloadURL(),
		Runner:       runner,
	}); err != nil {
		if isLikelyPermissionError(err) {
			if envLookupForUpdate("GOPHER_UPDATE_ELEVATED") != "1" && shouldPromptSudoForUpdate() {
				if stderr != nil {
					fmt.Fprintln(stderr, "permission denied updating binary; retrying with sudo")
				}
				if retryErr := retryWithSudoForUpdate(ctx, args, stdout, stderr); retryErr == nil {
					return nil
				} else {
					return retryErr
				}
			}
			commandName := filepath.Base(os.Args[0])
			if commandName == "." || commandName == "/" || commandName == "" {
				commandName = "gopher"
			}
			if errors.Is(err, fs.ErrPermission) {
				return fmt.Errorf("%w\nhint: %q is not writable by the current user; retry with elevated permissions:\n  sudo -E %s update", err, targetBinaryPath, commandName)
			}
			return fmt.Errorf("%w\nhint: update operations may require elevated permissions; retry with:\n  sudo -E %s update", err, commandName)
		}
		return err
	}

	slog.Info("update: release applied successfully", "updated_tag", release.TagName, "target_binary_path", targetBinaryPath)
	fmt.Fprintf(stdout, "updated binary to %s\n", release.TagName)
	if resolvedServiceName != "" {
		fmt.Fprintf(stdout, "restarted service %s\n", resolvedServiceName)
	}
	return nil
}

func resolvedGitHubToken(explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	if value := strings.TrimSpace(os.Getenv("GOPHER_GITHUB_TOKEN")); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("GOPHER_GITHUB_UPDATE_TOKEN"))
}

func printUpdateUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher update [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "flags:")
	fmt.Fprintln(out, "  --check                 only check for a newer release")
	fmt.Fprintln(out, "  --owner <owner>         github owner (default: bstncartwright)")
	fmt.Fprintln(out, "  --repo <repo>           github repo (default: gopher)")
	fmt.Fprintln(out, "  --binary-path <path>    binary path to replace (default: current executable)")
	fmt.Fprintln(out, "  --asset-pattern <text>  additional asset name filter")
	fmt.Fprintln(out, "  --service-name <name>   systemd service to restart after update (default: auto-detect gateway service on linux)")
	fmt.Fprintln(out, "  --no-service-restart    disable post-update systemd restart")
	fmt.Fprintln(out, "  --github-token <token>  github token (or GOPHER_GITHUB_TOKEN / GOPHER_GITHUB_UPDATE_TOKEN)")
}

func inferDefaultServiceNameForUpdate() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	const gatewayServiceName = "gopher-gateway.service"
	paths := updateCandidateServiceUnitPaths(gatewayServiceName)
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return gatewayServiceName
		}
	}
	return ""
}

func updateCandidateServiceUnitPaths(serviceName string) []string {
	if inferUpdateUserScope() {
		if home, err := updateUserHomeDirForScope(); err == nil && strings.TrimSpace(home) != "" {
			return []string{
				filepath.Join(home, ".config", "systemd", "user", serviceName),
			}
		}
	}
	return []string{
		filepath.Join("/etc/systemd/system", serviceName),
		filepath.Join("/lib/systemd/system", serviceName),
		filepath.Join("/usr/lib/systemd/system", serviceName),
	}
}

func inferUpdateUserScope() bool {
	return runtime.GOOS == "linux" && updateGetEUIDForScope() != 0
}

func isInteractiveTerminal() bool {
	stdin, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (stdin.Mode() & os.ModeCharDevice) != 0
}

func rerunUpdateWithSudo(ctx context.Context, updateArgs []string, stdout, stderr io.Writer) error {
	execPath, err := executablePathForUpdate()
	if err != nil {
		return fmt.Errorf("resolve current executable for sudo retry: %w", err)
	}
	sudoArgs := []string{"-E", execPath, "update"}
	sudoArgs = append(sudoArgs, updateArgs...)
	cmd := exec.CommandContext(ctx, "sudo", sudoArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "GOPHER_UPDATE_ELEVATED=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run sudo update command: %w", err)
	}
	return nil
}
