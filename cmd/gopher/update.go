package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
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
)

type noopRunner struct{}

func (noopRunner) Run(ctx context.Context, command string, args ...string) error {
	_ = ctx
	_ = command
	_ = args
	return nil
}

func runUpdateSubcommand(args []string, stdout, stderr io.Writer) error {
	_ = stderr
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

	release, err := latestReleaseForUpdate(ctx, strings.TrimSpace(*owner), strings.TrimSpace(*repo), ghToken)
	if err != nil {
		return err
	}
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

	asset, err := selectAssetForUpdate(release, runtime.GOOS, runtime.GOARCH, strings.TrimSpace(*assetPattern))
	if err != nil {
		return err
	}
	checksumsAsset, _ := selectChecksumsAssetForUpdate(release)

	runner := update.CommandRunner(noopRunner{})
	if strings.TrimSpace(*serviceName) != "" {
		runner = systemctlRunner{}
	}
	if err := applyReleaseForUpdate(ctx, update.ApplyOptions{
		BinaryPath:   targetBinaryPath,
		ServiceName:  strings.TrimSpace(*serviceName),
		Token:        ghToken,
		AssetURL:     asset.DownloadURL(),
		AssetName:    asset.Name,
		ChecksumsURL: checksumsAsset.DownloadURL(),
		Runner:       runner,
	}); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "updated binary to %s\n", release.TagName)
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
	fmt.Fprintln(out, "  --service-name <name>   optional systemd service to restart after update")
	fmt.Fprintln(out, "  --github-token <token>  github token (or GOPHER_GITHUB_TOKEN / GOPHER_GITHUB_UPDATE_TOKEN)")
}
