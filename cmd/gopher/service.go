package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/update"
)

type serviceRuntime interface {
	Install(ctx context.Context, opts serviceInstallOptions) error
	Uninstall(ctx context.Context) error
	Status(ctx context.Context) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context) error
	Logs(ctx context.Context, opts serviceLogsOptions) error
}

type serviceInstallOptions struct {
	ConfigPath string
	EnvPath    string
	BinaryPath string
	Role       string
}

type serviceLogsOptions struct {
	Unit   string
	Lines  int
	Follow bool
}

var newServiceRuntime = defaultServiceRuntime

func runServiceSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || wantsHelp(args) {
		printServiceUsage(stdout)
		return nil
	}
	runtimeImpl := newServiceRuntime(stdout, stderr)
	ctx := context.Background()

	switch strings.TrimSpace(args[0]) {
	case "install":
		flags := flag.NewFlagSet("service install", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		configPath := flags.String("config", "/etc/gopher/gopher.toml", "gateway config path")
		envPath := flags.String("env-file", "/etc/gopher/gopher.env", "service env file")
		binaryPath := flags.String("binary-path", "/usr/local/bin/gopher", "binary path for service")
		role := flags.String("role", "gateway", "service role (gateway|node)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if len(flags.Args()) > 0 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		}
		normalizedRole := strings.ToLower(strings.TrimSpace(*role))
		if normalizedRole != "gateway" && normalizedRole != "node" {
			return fmt.Errorf("invalid --role value: %s (expected gateway or node)", strings.TrimSpace(*role))
		}
		return runtimeImpl.Install(ctx, serviceInstallOptions{
			ConfigPath: strings.TrimSpace(*configPath),
			EnvPath:    strings.TrimSpace(*envPath),
			BinaryPath: strings.TrimSpace(*binaryPath),
			Role:       normalizedRole,
		})
	case "uninstall":
		return runtimeImpl.Uninstall(ctx)
	case "status":
		return runtimeImpl.Status(ctx)
	case "start":
		return runtimeImpl.Start(ctx)
	case "stop":
		return runtimeImpl.Stop(ctx)
	case "restart":
		return runtimeImpl.Restart(ctx)
	case "logs":
		flags := flag.NewFlagSet("service logs", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		unit := flags.String("unit", "gopher-gateway.service", "systemd unit name")
		lines := flags.Int("lines", 200, "number of journal lines")
		follow := flags.Bool("follow", false, "follow logs")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if len(flags.Args()) > 0 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		}
		return runtimeImpl.Logs(ctx, serviceLogsOptions{
			Unit:   strings.TrimSpace(*unit),
			Lines:  *lines,
			Follow: *follow,
		})
	case "update":
		return runServiceUpdateSubcommand(ctx, args[1:], stdout, stderr)
	default:
		printServiceUsage(stderr)
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func runServiceUpdateSubcommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || wantsHelp(args) {
		printServiceUsage(stdout)
		return nil
	}
	flags := flag.NewFlagSet("service update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "gopher.toml", "gateway config path")
	binaryPath := flags.String("binary-path", "/usr/local/bin/gopher", "binary path")
	serviceName := flags.String("service-name", "gopher-gateway.service", "systemd service name")
	token := flags.String("github-token", "", "github token (defaults to GOPHER_GITHUB_TOKEN)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	cfg, _, err := config.LoadGatewayConfig(config.GatewayLoadOptions{
		ConfigPath: strings.TrimSpace(*configPath),
	})
	if err != nil {
		return err
	}
	ghToken := strings.TrimSpace(*token)
	if ghToken == "" {
		ghToken = strings.TrimSpace(os.Getenv("GOPHER_GITHUB_TOKEN"))
	}
	if ghToken == "" {
		return fmt.Errorf("github token is required via --github-token or GOPHER_GITHUB_TOKEN")
	}
	client := update.GitHubReleasesClient{
		Owner: cfg.Update.RepoOwner,
		Repo:  cfg.Update.RepoName,
		Token: ghToken,
	}
	release, err := client.LatestRelease(ctx)
	if err != nil {
		return err
	}
	asset, err := update.SelectAsset(release, runtime.GOOS, runtime.GOARCH, cfg.Update.BinaryAssetPattern)
	if err != nil {
		return err
	}

	switch strings.TrimSpace(args[0]) {
	case "check":
		fmt.Fprintf(stdout, "latest release: %s\n", release.TagName)
		fmt.Fprintf(stdout, "selected asset: %s\n", asset.Name)
		return nil
	case "apply":
		checksumsAsset, _ := update.SelectChecksumsAsset(release)
		applyErr := update.ApplyRelease(ctx, update.ApplyOptions{
			BinaryPath:   strings.TrimSpace(*binaryPath),
			ServiceName:  strings.TrimSpace(*serviceName),
			Token:        ghToken,
			AssetURL:     asset.DownloadURL(),
			AssetName:    asset.Name,
			ChecksumsURL: checksumsAsset.DownloadURL(),
			Runner:       systemctlRunner{},
		})
		if applyErr != nil {
			return applyErr
		}
		fmt.Fprintf(stdout, "updated to release: %s\n", release.TagName)
		return nil
	default:
		printServiceUsage(stderr)
		return fmt.Errorf("unknown service update command %q", args[0])
	}
}

func printServiceUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher service install [--role gateway|node] [flags]")
	fmt.Fprintln(out, "  gopher service uninstall")
	fmt.Fprintln(out, "  gopher service status")
	fmt.Fprintln(out, "  gopher service start")
	fmt.Fprintln(out, "  gopher service stop")
	fmt.Fprintln(out, "  gopher service restart")
	fmt.Fprintln(out, "  gopher service logs [--unit gopher-gateway.service] [--lines 200] [--follow]")
	fmt.Fprintln(out, "  gopher service update check [flags]")
	fmt.Fprintln(out, "  gopher service update apply [flags]")
}
