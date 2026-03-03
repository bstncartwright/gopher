package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/update"
)

type serviceRuntime interface {
	Install(ctx context.Context, opts serviceInstallOptions) error
	Uninstall(ctx context.Context) error
	Status(ctx context.Context, opts serviceStatusOptions) error
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Restart(ctx context.Context, opts serviceTargetOptions) error
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
	Target serviceTarget
}

type serviceStatusOptions struct {
	Target serviceTarget
}

type serviceTargetOptions struct {
	Target serviceTarget
}

type serviceTarget string

const (
	serviceTargetAuto    serviceTarget = "auto"
	serviceTargetGateway serviceTarget = "gateway"
	serviceTargetNode    serviceTarget = "node"
)

func parseServiceTarget(value string) (serviceTarget, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(serviceTargetAuto):
		return serviceTargetAuto, nil
	case string(serviceTargetGateway):
		return serviceTargetGateway, nil
	case string(serviceTargetNode):
		return serviceTargetNode, nil
	default:
		return "", fmt.Errorf("invalid --role value: %s (expected auto, gateway, or node)", strings.TrimSpace(value))
	}
}

var newServiceRuntime = defaultServiceRuntime
var shouldPromptSudoForService = isInteractiveTerminal
var retryWithSudoForService = rerunServiceWithSudo
var envLookupForService = os.Getenv
var serviceGetEUID = os.Geteuid

func runServiceSubcommand(args []string, stdout, stderr io.Writer) (err error) {
	finishLog := startCommandLog("service", args)
	defer func() {
		finishLog(err)
	}()

	if len(args) == 0 || wantsHelp(args) {
		printServiceUsage(stdout)
		return nil
	}
	runtimeImpl := newServiceRuntime(stdout, stderr)
	ctx := context.Background()
	runWithSudoRetry := func(commandErr error) error {
		if !isLikelyPermissionError(commandErr) {
			return commandErr
		}
		if envLookupForService("GOPHER_SERVICE_ELEVATED") != "1" && shouldPromptSudoForService() {
			if stderr != nil {
				fmt.Fprintln(stderr, "permission denied running service command; retrying with sudo")
			}
			if retryErr := retryWithSudoForService(ctx, args, stdout, stderr); retryErr == nil {
				return nil
			}
		}
		commandName := filepath.Base(os.Args[0])
		if commandName == "." || commandName == "/" || commandName == "" {
			commandName = "gopher"
		}
		return fmt.Errorf("%w\nhint: service commands may require elevated permissions; retry with:\n  sudo -E %s service %s", commandErr, commandName, strings.Join(args, " "))
	}

	switch strings.TrimSpace(args[0]) {
	case "install":
		flags := flag.NewFlagSet("service install", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		configPath := flags.String("config", defaultServiceGatewayConfigPath(), "gateway config path")
		envPath := flags.String("env-file", defaultServiceEnvPath(), "service env file")
		binaryPath := flags.String("binary-path", defaultServiceBinaryPath(), "binary path for service")
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
		slog.Info(
			"service: install requested",
			"role", normalizedRole,
			"config_path", strings.TrimSpace(*configPath),
			"env_path", strings.TrimSpace(*envPath),
			"binary_path", strings.TrimSpace(*binaryPath),
		)
		if err := runtimeImpl.Install(ctx, serviceInstallOptions{
			ConfigPath: strings.TrimSpace(*configPath),
			EnvPath:    strings.TrimSpace(*envPath),
			BinaryPath: strings.TrimSpace(*binaryPath),
			Role:       normalizedRole,
		}); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	case "uninstall":
		slog.Info("service: uninstall requested")
		if err := runtimeImpl.Uninstall(ctx); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	case "status":
		flags := flag.NewFlagSet("service status", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		role := flags.String("role", "auto", "service role target (auto|gateway|node)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if len(flags.Args()) > 0 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		}
		target, err := parseServiceTarget(*role)
		if err != nil {
			return err
		}
		slog.Debug("service: status requested", "target", target)
		if err := runtimeImpl.Status(ctx, serviceStatusOptions{Target: target}); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	case "start":
		slog.Info("service: start requested")
		if err := runtimeImpl.Start(ctx); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	case "stop":
		slog.Info("service: stop requested")
		if err := runtimeImpl.Stop(ctx); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	case "restart":
		flags := flag.NewFlagSet("service restart", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		role := flags.String("role", "auto", "service role target (auto|gateway|node)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if len(flags.Args()) > 0 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		}
		target, err := parseServiceTarget(*role)
		if err != nil {
			return err
		}
		slog.Info("service: restart requested", "target", target)
		if err := runtimeImpl.Restart(ctx, serviceTargetOptions{Target: target}); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	case "logs":
		flags := flag.NewFlagSet("service logs", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		role := flags.String("role", "auto", "service role target (auto|gateway|node)")
		unit := flags.String("unit", "", "systemd unit name (overrides --role)")
		lines := flags.Int("lines", 200, "number of journal lines")
		follow := flags.Bool("follow", false, "follow logs")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if len(flags.Args()) > 0 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		}
		target, err := parseServiceTarget(*role)
		if err != nil {
			return err
		}
		slog.Info(
			"service: logs requested",
			"target", target,
			"unit", strings.TrimSpace(*unit),
			"lines", *lines,
			"follow", *follow,
		)
		if err := runtimeImpl.Logs(ctx, serviceLogsOptions{
			Unit:   strings.TrimSpace(*unit),
			Lines:  *lines,
			Follow: *follow,
			Target: target,
		}); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	case "update":
		slog.Info("service: update requested")
		if err := runServiceUpdateSubcommand(ctx, args[1:], stdout, stderr); err != nil {
			return runWithSudoRetry(err)
		}
		return nil
	default:
		printServiceUsage(stderr)
		return fmt.Errorf("unknown service command %q", args[0])
	}
}

func defaultServiceGatewayConfigPath() string {
	if serviceGetEUID() == 0 {
		return "/etc/gopher/gopher.toml"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".gopher", "gopher.toml")
	}
	return "/etc/gopher/gopher.toml"
}

func defaultServiceEnvPath() string {
	if serviceGetEUID() == 0 {
		return "/etc/gopher/gopher.env"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".gopher", "gopher.env")
	}
	return "/etc/gopher/gopher.env"
}

func defaultServiceBinaryPath() string {
	if path, err := os.Executable(); err == nil && strings.TrimSpace(path) != "" {
		return path
	}
	if serviceGetEUID() == 0 {
		return "/usr/local/bin/gopher"
	}
	return "gopher"
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
	slog.Info(
		"service: update invocation resolved",
		"config_path", strings.TrimSpace(*configPath),
		"binary_path", strings.TrimSpace(*binaryPath),
		"service_name", strings.TrimSpace(*serviceName),
	)

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
	fmt.Fprintln(out, "  gopher service status [--role auto|gateway|node]")
	fmt.Fprintln(out, "  gopher service start")
	fmt.Fprintln(out, "  gopher service stop")
	fmt.Fprintln(out, "  gopher service restart [--role auto|gateway|node]")
	fmt.Fprintln(out, "  gopher service logs [--role auto|gateway|node] [--unit <name>] [--lines 200] [--follow]")
	fmt.Fprintln(out, "  gopher service update check [flags]")
	fmt.Fprintln(out, "  gopher service update apply [flags]")
}

func rerunServiceWithSudo(ctx context.Context, serviceArgs []string, stdout, stderr io.Writer) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current executable for sudo retry: %w", err)
	}
	sudoArgs := []string{"-E", execPath, "service"}
	sudoArgs = append(sudoArgs, serviceArgs...)
	cmd := exec.CommandContext(ctx, "sudo", sudoArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "GOPHER_SERVICE_ELEVATED=1")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("run sudo service command: %w", err)
	}
	return nil
}
