package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/bstncartwright/gopher/pkg/config"
)

const (
	telegramBotTokenEnvKey           = "GOPHER_TELEGRAM_BOT_TOKEN"
	defaultTelegramWebhookListenAddr = "127.0.0.1:29330"
	defaultTelegramWebhookPath       = "/_gopher/telegram/webhook"
)

func runOnboardingSubcommand(args []string, in io.Reader, stdout, stderr io.Writer) (err error) {
	finishLog := startCommandLog("onboard", args)
	defer func() {
		finishLog(err)
	}()

	flags := flag.NewFlagSet("onboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	gatewayConfigPath := flags.String("gateway-config-path", defaultOnboardingGatewayConfigPath(), "gateway config output path")
	nodeConfigPath := flags.String("node-config-path", "", "optional node config output path (default: disabled)")
	envFile := flags.String("env-file", defaultAuthEnvFilePath(), "env file path")
	force := flags.Bool("force", false, "overwrite existing config files")
	nonInteractive := flags.Bool("non-interactive", false, "require all needed values via flags")
	authProvider := flags.String("auth-provider", "", "provider to configure when auth is missing")
	authAPIKey := flags.String("auth-api-key", "", "api key/token for --auth-provider")
	telegramBotToken := flags.String("telegram-bot-token", "", "telegram bot token")
	telegramMode := flags.String("telegram-mode", "", "telegram mode (polling|websocket|webhook)")
	telegramWebhookURL := flags.String("telegram-webhook-url", "", "telegram webhook public https url")
	telegramWebhookSecret := flags.String("telegram-webhook-secret", "", "telegram webhook secret")
	telegramWebhookListenAddr := flags.String("telegram-webhook-listen-addr", "", "telegram webhook listen addr (default 127.0.0.1:29330)")
	telegramWebhookPath := flags.String("telegram-webhook-path", "", "telegram webhook path (default /_gopher/telegram/webhook)")

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
	nodePath := ""
	if raw := strings.TrimSpace(*nodeConfigPath); raw != "" {
		nodePath, err = resolveAbsolutePath(raw)
		if err != nil {
			return fmt.Errorf("resolve node config path: %w", err)
		}
	}
	envPath, err := resolveAbsolutePath(strings.TrimSpace(*envFile))
	if err != nil {
		return fmt.Errorf("resolve env file path: %w", err)
	}

	if err := writeDefaultConfig(gatewayPath, []byte(config.DefaultGatewayTOML()), *force); err != nil {
		return err
	}
	if nodePath != "" {
		if err := writeDefaultConfig(nodePath, []byte(config.DefaultNodeTOML()), *force); err != nil {
			return err
		}
	}

	fmt.Fprintf(stdout, "gateway config ready: %s\n", gatewayPath)
	if nodePath != "" {
		fmt.Fprintf(stdout, "node config ready: %s\n", nodePath)
	}
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

	providerValue := strings.TrimSpace(*authProvider)
	apiKeyValue := strings.TrimSpace(*authAPIKey)
	telegramTokenValue := strings.TrimSpace(*telegramBotToken)
	if telegramTokenValue == "" {
		telegramTokenValue = strings.TrimSpace(envValues[telegramBotTokenEnvKey])
	}
	telegramModeValue, err := normalizeOnboardingTelegramMode(strings.TrimSpace(*telegramMode))
	if err != nil {
		return err
	}
	if telegramModeValue == "" {
		telegramModeValue = "polling"
	}
	telegramWebhookURLValue := strings.TrimSpace(*telegramWebhookURL)
	telegramWebhookSecretValue := strings.TrimSpace(*telegramWebhookSecret)
	telegramWebhookListenAddrValue := strings.TrimSpace(*telegramWebhookListenAddr)
	if telegramWebhookListenAddrValue == "" {
		telegramWebhookListenAddrValue = defaultTelegramWebhookListenAddr
	}
	telegramWebhookPathValue := strings.TrimSpace(*telegramWebhookPath)
	if telegramWebhookPathValue == "" {
		telegramWebhookPathValue = defaultTelegramWebhookPath
	}
	configureTelegram := telegramTokenValue != ""

	if !*nonInteractive {
		wizardValues, err := runInteractiveOnboardingWizard(in, stdout, onboardingWizardDefaults{
			Provider:                  providerValue,
			ProviderAPIKey:            apiKeyValue,
			ConfigureTelegram:         configureTelegram,
			TelegramMode:              telegramModeValue,
			TelegramBotToken:          telegramTokenValue,
			TelegramWebhookURL:        telegramWebhookURLValue,
			TelegramWebhookSecret:     telegramWebhookSecretValue,
			TelegramWebhookListenAddr: telegramWebhookListenAddrValue,
			TelegramWebhookPath:       telegramWebhookPathValue,
		})
		if err != nil {
			return err
		}
		providerValue = wizardValues.Provider
		apiKeyValue = wizardValues.ProviderAPIKey
		configureTelegram = wizardValues.ConfigureTelegram
		telegramModeValue = wizardValues.TelegramMode
		telegramTokenValue = wizardValues.TelegramBotToken
		telegramWebhookURLValue = wizardValues.TelegramWebhookURL
		telegramWebhookSecretValue = wizardValues.TelegramWebhookSecret
		telegramWebhookListenAddrValue = wizardValues.TelegramWebhookListenAddr
		telegramWebhookPathValue = wizardValues.TelegramWebhookPath
	}

	if strings.TrimSpace(providerValue) == "" {
		if !hasAnyProviderAuth(envValues) {
			return fmt.Errorf("auth is missing: provide --auth-provider and --auth-api-key, or run without --non-interactive")
		}
		fmt.Fprintln(stdout, "provider auth already configured")
	} else {
		spec, ok := findProviderSpec(providerValue)
		if !ok {
			return fmt.Errorf("unknown auth provider %q", providerValue)
		}
		if spec.Provider == "openai-codex" {
			if strings.TrimSpace(apiKeyValue) != "" {
				fmt.Fprintln(stdout, "ignoring api key for openai-codex; using oauth login flow")
			}
			if err := runAuthLogin([]string{"--provider", "openai-codex", "--env-file", envPath}, in, stdout); err != nil {
				return fmt.Errorf("configure auth provider %s: %w", spec.Provider, err)
			}
		} else {
			if strings.TrimSpace(apiKeyValue) == "" {
				return fmt.Errorf("auth is missing: provide --auth-provider and --auth-api-key, or run without --non-interactive")
			}
			if err := upsertEnvKey(envPath, spec.EnvKeys[0], strings.TrimSpace(apiKeyValue)); err != nil {
				return err
			}
			slog.Info("onboard: provider auth configured", "provider", spec.Provider, "env_file", envPath, "key", spec.EnvKeys[0])
			fmt.Fprintf(stdout, "configured auth provider %s in %s\n", spec.Provider, envPath)
		}
	}

	if configureTelegram && telegramTokenValue != "" {
		if err := upsertEnvKey(envPath, telegramBotTokenEnvKey, telegramTokenValue); err != nil {
			return err
		}
		slog.Info("onboard: telegram bot token configured", "env_file", envPath, "key", telegramBotTokenEnvKey)
		fmt.Fprintf(stdout, "configured %s in %s\n", telegramBotTokenEnvKey, envPath)

		mode := telegramModeValue
		if mode == "" {
			mode = "polling"
		}
		mutation := gatewayTelegramMutation{}
		enabled := true
		mutation.Enabled = &enabled
		mutation.Mode = &mode
		if mode == "webhook" {
			if strings.TrimSpace(telegramWebhookURLValue) == "" || strings.TrimSpace(telegramWebhookSecretValue) == "" {
				return fmt.Errorf("telegram webhook mode requires webhook url and secret")
			}
			mutation.WebhookListenAddr = &telegramWebhookListenAddrValue
			mutation.WebhookPath = &telegramWebhookPathValue
			mutation.WebhookURL = &telegramWebhookURLValue
			mutation.WebhookSecret = &telegramWebhookSecretValue
		}
		changed, err := setGatewayTelegramConfig(gatewayPath, mutation)
		if err != nil {
			return fmt.Errorf("configure gateway telegram in %s: %w", gatewayPath, err)
		}
		if changed {
			slog.Info("onboard: configured telegram gateway integration", "gateway_config_path", gatewayPath, "mode", mode)
			fmt.Fprintf(stdout, "configured gateway telegram in %s (mode=%s)\n", gatewayPath, mode)
		}
	} else {
		fmt.Fprintln(stdout, "telegram config incomplete (set bot token to enable telegram integration)")
	}

	return nil
}

type onboardingWizardDefaults struct {
	Provider                  string
	ProviderAPIKey            string
	ConfigureTelegram         bool
	TelegramMode              string
	TelegramBotToken          string
	TelegramWebhookURL        string
	TelegramWebhookSecret     string
	TelegramWebhookListenAddr string
	TelegramWebhookPath       string
}

type onboardingWizardValues struct {
	Provider                  string
	ProviderAPIKey            string
	ConfigureTelegram         bool
	TelegramMode              string
	TelegramBotToken          string
	TelegramWebhookURL        string
	TelegramWebhookSecret     string
	TelegramWebhookListenAddr string
	TelegramWebhookPath       string
}

func runInteractiveOnboardingWizard(in io.Reader, out io.Writer, defaults onboardingWizardDefaults) (onboardingWizardValues, error) {
	values := onboardingWizardValues{
		Provider:                  strings.TrimSpace(defaults.Provider),
		ProviderAPIKey:            strings.TrimSpace(defaults.ProviderAPIKey),
		ConfigureTelegram:         defaults.ConfigureTelegram,
		TelegramMode:              strings.TrimSpace(defaults.TelegramMode),
		TelegramBotToken:          strings.TrimSpace(defaults.TelegramBotToken),
		TelegramWebhookURL:        strings.TrimSpace(defaults.TelegramWebhookURL),
		TelegramWebhookSecret:     strings.TrimSpace(defaults.TelegramWebhookSecret),
		TelegramWebhookListenAddr: strings.TrimSpace(defaults.TelegramWebhookListenAddr),
		TelegramWebhookPath:       strings.TrimSpace(defaults.TelegramWebhookPath),
	}
	if values.Provider == "" {
		values.Provider = "openai-codex"
	}
	mode, err := normalizeOnboardingTelegramMode(values.TelegramMode)
	if err != nil {
		values.TelegramMode = "polling"
	} else {
		values.TelegramMode = mode
	}
	if values.TelegramMode == "" {
		values.TelegramMode = "polling"
	}
	if values.TelegramWebhookListenAddr == "" {
		values.TelegramWebhookListenAddr = defaultTelegramWebhookListenAddr
	}
	if values.TelegramWebhookPath == "" {
		values.TelegramWebhookPath = defaultTelegramWebhookPath
	}

	var acceptedDanger bool
	if err := runHuhForm(in, out, huh.NewGroup(
		huh.NewConfirm().
			Title("gopher can execute local commands with broad access. continue?").
			Description("only continue if you trust this setup and understand it can modify files, run processes, and call external services.").
			Affirmative("Yes, continue").
			Negative("No, cancel").
			Value(&acceptedDanger),
	)); err != nil {
		return onboardingWizardValues{}, err
	}
	if !acceptedDanger {
		return onboardingWizardValues{}, fmt.Errorf("onboarding cancelled")
	}

	providerOptions := make([]huh.Option[string], 0, len(providerAuthSpecs))
	for _, spec := range providerAuthSpecs {
		label := spec.Provider
		if spec.Provider == "openai-codex" {
			label = "openai-codex (oauth)"
		}
		providerOptions = append(providerOptions, huh.NewOption(label, spec.Provider))
	}
	if err := runHuhForm(in, out, huh.NewGroup(
		huh.NewSelect[string]().
			Title("choose ai provider").
			Options(providerOptions...).
			Value(&values.Provider).
			Validate(func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("provider is required")
				}
				return nil
			}),
	)); err != nil {
		return onboardingWizardValues{}, err
	}

	if values.Provider != "openai-codex" {
		if err := runHuhForm(in, out, huh.NewGroup(
			huh.NewInput().
				Title(fmt.Sprintf("enter %s api key/token", values.Provider)).
				Description("this is stored in the onboarding env file.").
				Value(&values.ProviderAPIKey).
				EchoMode(huh.EchoModePassword).
				Validate(func(v string) error {
					if strings.TrimSpace(v) == "" {
						return fmt.Errorf("api key/token is required")
					}
					return nil
				}),
		)); err != nil {
			return onboardingWizardValues{}, err
		}
	}

	if err := runHuhForm(in, out, huh.NewGroup(
		huh.NewConfirm().
			Title("configure telegram integration?").
			Affirmative("Yes").
			Negative("No").
			Value(&values.ConfigureTelegram),
	)); err != nil {
		return onboardingWizardValues{}, err
	}
	if !values.ConfigureTelegram {
		return values, nil
	}

	if err := runHuhForm(in, out, huh.NewGroup(
		huh.NewSelect[string]().
			Title("telegram ingress mode").
			Options(
				huh.NewOption("polling", "polling"),
				huh.NewOption("websocket/webhook", "webhook"),
			).
			Value(&values.TelegramMode),
	)); err != nil {
		return onboardingWizardValues{}, err
	}

	if err := runHuhForm(in, out, huh.NewGroup(
		huh.NewInput().
			Title("telegram bot token").
			Value(&values.TelegramBotToken).
			EchoMode(huh.EchoModePassword).
			Validate(func(v string) error {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("telegram bot token is required")
				}
				return nil
			}),
	)); err != nil {
		return onboardingWizardValues{}, err
	}

	if values.TelegramMode == "webhook" {
		if err := runHuhForm(in, out, huh.NewGroup(
			huh.NewInput().
				Title("telegram webhook url (https)").
				Placeholder("https://example.ts.net/_gopher/telegram/webhook").
				Value(&values.TelegramWebhookURL).
				Validate(func(v string) error {
					if strings.TrimSpace(v) == "" {
						return fmt.Errorf("webhook url is required")
					}
					if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "https://") {
						return fmt.Errorf("webhook url must use https")
					}
					return nil
				}),
			huh.NewInput().
				Title("telegram webhook secret").
				Value(&values.TelegramWebhookSecret).
				EchoMode(huh.EchoModePassword).
				Validate(func(v string) error {
					if strings.TrimSpace(v) == "" {
						return fmt.Errorf("webhook secret is required")
					}
					return nil
				}),
		)); err != nil {
			return onboardingWizardValues{}, err
		}
	}

	return values, nil
}

func runHuhForm(in io.Reader, out io.Writer, groups ...*huh.Group) error {
	form := huh.NewForm(groups...).WithInput(in).WithOutput(out)
	if err := form.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return fmt.Errorf("onboarding cancelled")
		}
		return err
	}
	return nil
}

func normalizeOnboardingTelegramMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "":
		return "", nil
	case "polling":
		return "polling", nil
	case "webhook", "websocket":
		return "webhook", nil
	default:
		return "", fmt.Errorf("invalid telegram mode %q: expected polling or websocket", raw)
	}
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory %s: %w", filepath.Dir(path), err)
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

func defaultOnboardingGatewayConfigPath() string {
	if os.Geteuid() == 0 {
		return "/etc/gopher/gopher.toml"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "gopher.toml"
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "gopher.toml"
	}
	return filepath.Join(home, ".gopher", "gopher.toml")
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
