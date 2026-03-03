package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bstncartwright/gopher/pkg/config"
)

const (
	telegramBotTokenEnvKey = "GOPHER_TELEGRAM_BOT_TOKEN"
)

func runOnboardingSubcommand(args []string, in io.Reader, stdout, stderr io.Writer) (err error) {
	finishLog := startCommandLog("onboard", args)
	defer func() {
		finishLog(err)
	}()

	flags := flag.NewFlagSet("onboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	gatewayConfigPath := flags.String("gateway-config-path", "gopher.toml", "gateway config output path")
	nodeConfigPath := flags.String("node-config-path", "node.toml", "node config output path")
	envFile := flags.String("env-file", defaultAuthEnvFilePath(), "env file path")
	force := flags.Bool("force", false, "overwrite existing config files")
	nonInteractive := flags.Bool("non-interactive", false, "require all needed values via flags")
	authProvider := flags.String("auth-provider", "", "provider to configure when auth is missing")
	authAPIKey := flags.String("auth-api-key", "", "api key/token for --auth-provider")
	telegramBotToken := flags.String("telegram-bot-token", "", "telegram bot token")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	gatewayPath, err := resolveAbsolutePath(strings.TrimSpace(*gatewayConfigPath))
	if err != nil {
		return fmt.Errorf("resolve gateway config path: %w", err)
	}
	nodePath, err := resolveAbsolutePath(strings.TrimSpace(*nodeConfigPath))
	if err != nil {
		return fmt.Errorf("resolve node config path: %w", err)
	}
	envPath, err := resolveAbsolutePath(strings.TrimSpace(*envFile))
	if err != nil {
		return fmt.Errorf("resolve env file path: %w", err)
	}

	if err := writeDefaultConfig(gatewayPath, []byte(config.DefaultGatewayTOML()), *force); err != nil {
		return err
	}
	if err := writeDefaultConfig(nodePath, []byte(config.DefaultNodeTOML()), *force); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "gateway config ready: %s\n", gatewayPath)
	fmt.Fprintf(stdout, "node config ready: %s\n", nodePath)
	slog.Info(
		"onboard: base config files ready",
		"gateway_config_path", gatewayPath,
		"node_config_path", nodePath,
		"env_file", envPath,
		"force", *force,
		"non_interactive", *nonInteractive,
	)

	envValues, err := readEnvFileMap(envPath)
	if err != nil {
		return err
	}

	if !hasAnyProviderAuth(envValues) {
		providerValue := strings.TrimSpace(*authProvider)
		apiKeyValue := strings.TrimSpace(*authAPIKey)

		if providerValue == "" && !*nonInteractive {
			providerValue, err = promptLine(in, stdout, "no provider auth found. provider to configure (for example: zai, openai, anthropic, openai-codex): ")
			if err != nil {
				return err
			}
		}

		if providerValue != "" {
			spec, ok := findProviderSpec(providerValue)
			if !ok {
				return fmt.Errorf("unknown auth provider %q", providerValue)
			}
			if apiKeyValue == "" && !*nonInteractive {
				apiKeyValue, err = promptLine(in, stdout, fmt.Sprintf("enter %s api key/token: ", spec.Provider))
				if err != nil {
					return err
				}
			}
			if strings.TrimSpace(apiKeyValue) == "" {
				if *nonInteractive {
					return fmt.Errorf("auth is missing: provide --auth-provider and --auth-api-key, or run without --non-interactive")
				}
				fmt.Fprintln(stdout, "auth skipped (no api key provided)")
			} else {
				if err := upsertEnvKey(envPath, spec.EnvKeys[0], strings.TrimSpace(apiKeyValue)); err != nil {
					return err
				}
				slog.Info("onboard: provider auth configured", "provider", spec.Provider, "env_file", envPath, "key", spec.EnvKeys[0])
				fmt.Fprintf(stdout, "configured auth provider %s in %s\n", spec.Provider, envPath)
			}
		} else if *nonInteractive {
			return fmt.Errorf("auth is missing: provide --auth-provider and --auth-api-key, or run without --non-interactive")
		} else {
			fmt.Fprintln(stdout, "auth skipped")
		}
	} else {
		fmt.Fprintln(stdout, "provider auth already configured")
	}

	telegramTokenValue := strings.TrimSpace(*telegramBotToken)
	if telegramTokenValue == "" {
		telegramTokenValue = strings.TrimSpace(envValues[telegramBotTokenEnvKey])
	}

	if telegramTokenValue == "" && !*nonInteractive {
		telegramTokenValue, err = promptLine(in, stdout, "telegram bot token (leave blank to skip): ")
		if err != nil {
			return err
		}
	}

	if telegramTokenValue != "" {
		if err := upsertEnvKey(envPath, telegramBotTokenEnvKey, telegramTokenValue); err != nil {
			return err
		}
		slog.Info("onboard: telegram bot token configured", "env_file", envPath, "key", telegramBotTokenEnvKey)
		fmt.Fprintf(stdout, "configured %s in %s\n", telegramBotTokenEnvKey, envPath)
		enabled, err := setGatewayTelegramEnabled(gatewayPath, true)
		if err != nil {
			return fmt.Errorf("enable gateway telegram in %s: %w", gatewayPath, err)
		}
		if enabled {
			slog.Info("onboard: enabled telegram gateway integration", "gateway_config_path", gatewayPath)
			fmt.Fprintf(stdout, "enabled gateway telegram in %s\n", gatewayPath)
		}
	}
	if telegramTokenValue == "" {
		fmt.Fprintln(stdout, "telegram config incomplete (set bot token to enable telegram integration)")
	}

	return nil
}

func runFactoryResetSubcommand(args []string, stdout, stderr io.Writer) (err error) {
	finishLog := startCommandLog("reset", args)
	defer func() {
		finishLog(err)
	}()

	flags := flag.NewFlagSet("reset", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	workspace := flags.String("workspace", "", "workspace root (default: current directory)")
	envFile := flags.String("env-file", defaultAuthEnvFilePath(), "auth env file to preserve")
	yes := flags.Bool("yes", false, "confirm destructive reset")

	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if !*yes {
		return fmt.Errorf("reset is destructive; re-run with --yes to continue")
	}

	workspacePath := strings.TrimSpace(*workspace)
	if workspacePath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		workspacePath = cwd
	}
	workspacePath, err = resolveAbsolutePath(workspacePath)
	if err != nil {
		return fmt.Errorf("resolve workspace path: %w", err)
	}
	authPath, err := resolveAbsolutePath(strings.TrimSpace(*envFile))
	if err != nil {
		return fmt.Errorf("resolve env file path: %w", err)
	}

	var authBlob []byte
	authPerm := os.FileMode(0o600)
	if info, statErr := os.Stat(authPath); statErr == nil && !info.IsDir() {
		authPerm = info.Mode().Perm()
		authBlob, err = os.ReadFile(authPath)
		if err != nil {
			return fmt.Errorf("read auth env file %s: %w", authPath, err)
		}
	}

	pathsToRemove, err := defaultFactoryResetPaths(workspacePath)
	if err != nil {
		return err
	}
	slog.Info("reset: removing workspace and global state", "workspace", workspacePath, "paths_count", len(pathsToRemove), "auth_path", authPath)

	removed := make([]string, 0, len(pathsToRemove))
	skipped := make([]string, 0)

	for _, target := range pathsToRemove {
		target = filepath.Clean(strings.TrimSpace(target))
		if target == "" {
			continue
		}
		if samePath(target, authPath) {
			skipped = append(skipped, target+" (preserved auth)")
			continue
		}
		if _, statErr := os.Stat(target); statErr != nil {
			if !os.IsNotExist(statErr) {
				skipped = append(skipped, target+" ("+statErr.Error()+")")
			}
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			skipped = append(skipped, target+" ("+err.Error()+")")
			continue
		}
		removed = append(removed, target)
	}

	if authBlob != nil {
		if err := os.MkdirAll(filepath.Dir(authPath), 0o755); err != nil {
			return fmt.Errorf("restore auth env directory %s: %w", filepath.Dir(authPath), err)
		}
		if err := os.WriteFile(authPath, authBlob, authPerm); err != nil {
			return fmt.Errorf("restore auth env file %s: %w", authPath, err)
		}
	}

	sort.Strings(removed)
	sort.Strings(skipped)
	for _, path := range removed {
		fmt.Fprintf(stdout, "removed %s\n", path)
	}
	for _, item := range skipped {
		fmt.Fprintf(stderr, "skipped %s\n", item)
	}
	slog.Info("reset: completed", "removed_count", len(removed), "skipped_count", len(skipped), "auth_path", authPath)
	fmt.Fprintf(stdout, "reset complete (auth preserved at %s)\n", authPath)
	return nil
}

func writeDefaultConfig(path string, content []byte, force bool) error {
	if path == "" {
		return fmt.Errorf("config path is required")
	}
	if _, err := os.Stat(path); err == nil {
		if !force {
			return fmt.Errorf("file already exists: %s (use --force to overwrite)", path)
		}
	}
	if err := writeConfigFileWithBackup(path, content); err != nil {
		return err
	}
	return nil
}

func hasAnyProviderAuth(values map[string]string) bool {
	for _, spec := range providerAuthSpecs {
		for _, key := range spec.EnvKeys {
			if strings.TrimSpace(values[key]) != "" {
				return true
			}
		}
	}
	return false
}

func promptLine(in io.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	reader := bufio.NewReader(in)
	text, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

func defaultFactoryResetPaths(workspace string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve user home: %w", err)
	}

	workspace = filepath.Clean(strings.TrimSpace(workspace))
	out := []string{
		filepath.Join(workspace, "gopher.toml"),
		filepath.Join(workspace, "gopher.local.toml"),
		filepath.Join(workspace, "node.toml"),
		filepath.Join(workspace, "node.local.toml"),
		filepath.Join(workspace, ".gopher"),
		filepath.Join(home, ".gopher"),
		"/etc/gopher/gopher.toml",
		"/etc/gopher/node.toml",
	}
	return dedupePaths(out), nil
}

func dedupePaths(paths []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "" {
			continue
		}
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

func resolveAbsolutePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func samePath(a, b string) bool {
	return filepath.Clean(strings.TrimSpace(a)) == filepath.Clean(strings.TrimSpace(b))
}
