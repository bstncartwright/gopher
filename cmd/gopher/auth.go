package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type providerAuthSpec struct {
	Provider string
	EnvKeys  []string
	Mode     string
}

var providerAuthSpecs = []providerAuthSpec{
	{Provider: "openai", EnvKeys: []string{"OPENAI_API_KEY"}, Mode: "api_key"},
	{Provider: "zai", EnvKeys: []string{"ZAI_API_KEY"}, Mode: "api_key"},
	{Provider: "kimi-coding", EnvKeys: []string{"KIMI_API_KEY"}, Mode: "api_key"},
	{Provider: "anthropic", EnvKeys: []string{"ANTHROPIC_API_KEY"}, Mode: "api_key"},
	{Provider: "ollama", EnvKeys: []string{"OLLAMA_API_KEY"}, Mode: "optional_api_key"},
	{Provider: "openai-codex", EnvKeys: []string{"OPENAI_CODEX_API_KEY", "OPENAI_CODEX_TOKEN", "OPENAI_CODEX_REFRESH_TOKEN", "OPENAI_CODEX_TOKEN_EXPIRES"}, Mode: "oauth_or_api_key"},
}

var loginOpenAICodexForAuth = ai.LoginOpenAICodex

func runAuthSubcommand(args []string, stdout, stderr io.Writer) (err error) {
	finishLog := startCommandLog("auth", args)
	defer func() {
		finishLog(err)
	}()

	if len(args) == 0 || wantsHelp(args) {
		printAuthUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "providers":
		return runAuthProviders(stdout)
	case "list":
		return runAuthList(args[1:], stdout)
	case "login":
		return runAuthLogin(args[1:], os.Stdin, stdout)
	case "set", "create":
		return runAuthSet(args[1:], stdout)
	case "unset", "delete", "remove":
		return runAuthUnset(args[1:], stdout)
	default:
		printAuthUsage(stderr)
		return fmt.Errorf("unknown auth command %q", args[0])
	}
}

func printAuthUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher auth providers")
	fmt.Fprintln(out, "  gopher auth list [--env-file <path>]")
	fmt.Fprintln(out, "  gopher auth login --provider openai-codex [--env-file ...]")
	fmt.Fprintln(out, "  gopher auth set --provider zai --api-key <value> [--env-file ...]")
	fmt.Fprintln(out, "  gopher auth set --key <ENV_KEY> --value <value> [--env-file ...]")
	fmt.Fprintln(out, "  gopher auth unset --provider zai [--env-file ...]")
	fmt.Fprintln(out, "  gopher auth unset --key <ENV_KEY> [--env-file ...]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "default env file: $GOPHER_ENV_FILE, otherwise ~/.gopher/gopher.env (or /etc/gopher/gopher.env when running as root)")
}

func runAuthProviders(out io.Writer) error {
	slog.Debug("auth: listing supported providers", "providers", len(providerAuthSpecs))
	fmt.Fprintln(out, "supported providers:")
	for _, spec := range providerAuthSpecs {
		fmt.Fprintf(out, "  - %s (%s) -> %s\n", spec.Provider, spec.Mode, strings.Join(spec.EnvKeys, ", "))
	}
	return nil
}

func runAuthList(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("auth list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env-file", defaultAuthEnvFilePath(), "env file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	values, err := readEnvFileMap(strings.TrimSpace(*envFile))
	if err != nil {
		return err
	}
	slog.Debug("auth: loaded env file for list", "env_file", strings.TrimSpace(*envFile), "keys_count", len(values))

	fmt.Fprintln(out, "provider auth status:")
	for _, spec := range providerAuthSpecs {
		state := "missing"
		details := "none"
		for _, key := range spec.EnvKeys {
			if strings.TrimSpace(values[key]) != "" {
				state = "configured"
				details = key
				break
			}
		}
		fmt.Fprintf(out, "  - %s: %s (%s)\n", spec.Provider, state, details)
	}

	extras := make([]string, 0)
	for key, value := range values {
		if strings.HasPrefix(key, "GOPHER_") && strings.Contains(key, "TOKEN") && strings.TrimSpace(value) != "" {
			extras = append(extras, key)
		}
	}
	sort.Strings(extras)
	if len(extras) > 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "configured gopher tokens:")
		for _, key := range extras {
			fmt.Fprintf(out, "  - %s\n", key)
		}
	}
	return nil
}

func runAuthSet(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("auth set", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env-file", defaultAuthEnvFilePath(), "env file path")
	provider := flags.String("provider", "", "provider id")
	apiKey := flags.String("api-key", "", "provider API key/token")
	key := flags.String("key", "", "raw env key")
	value := flags.String("value", "", "raw env value")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	targetKey := strings.TrimSpace(*key)
	targetValue := strings.TrimSpace(*value)
	if strings.TrimSpace(*provider) != "" {
		spec, ok := findProviderSpec(strings.TrimSpace(*provider))
		if !ok {
			return fmt.Errorf("unknown provider %q", *provider)
		}
		targetKey = spec.EnvKeys[0]
		targetValue = strings.TrimSpace(*apiKey)
	}
	if targetKey == "" {
		return fmt.Errorf("either --provider or --key is required")
	}
	if targetValue == "" {
		return fmt.Errorf("secret value is required")
	}
	slog.Info("auth: setting credential key", "env_file", strings.TrimSpace(*envFile), "key", targetKey)

	if err := upsertEnvKey(strings.TrimSpace(*envFile), targetKey, targetValue); err != nil {
		return err
	}
	fmt.Fprintf(out, "set %s in %s\n", targetKey, strings.TrimSpace(*envFile))
	return nil
}

func runAuthLogin(args []string, in io.Reader, out io.Writer) error {
	flags := flag.NewFlagSet("auth login", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env-file", defaultAuthEnvFilePath(), "env file path")
	provider := flags.String("provider", "", "provider id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	providerID := strings.TrimSpace(strings.ToLower(*provider))
	if providerID == "" {
		return fmt.Errorf("--provider is required")
	}
	if providerID != "openai-codex" {
		return fmt.Errorf("interactive oauth login is not supported for provider %q", providerID)
	}
	slog.Info("auth: starting provider oauth login", "provider", providerID, "env_file", strings.TrimSpace(*envFile))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	reader := bufio.NewReader(in)
	credentials, err := loginOpenAICodexForAuth(ai.OAuthLoginCallbacks{
		Context: ctx,
		OnAuth: func(info ai.OAuthAuthInfo) {
			if text := strings.TrimSpace(info.Instructions); text != "" {
				fmt.Fprintln(out, text)
			}
			if u := strings.TrimSpace(info.URL); u != "" {
				fmt.Fprintf(out, "login url: %s\n", u)
			}
			fmt.Fprintln(out, "waiting for callback on http://localhost:1455/auth/callback")
			fmt.Fprintln(out, "if callback forwarding is unavailable, paste callback URL or code, then press Enter:")
		},
		OnProgress: func(message string) {
			if text := strings.TrimSpace(message); text != "" {
				fmt.Fprintln(out, text)
			}
		},
		OnManualCodeInput: func() (string, error) {
			for {
				text, err := reader.ReadString('\n')
				if err != nil {
					if errors.Is(err, io.EOF) && strings.TrimSpace(text) != "" {
						return strings.TrimSpace(text), nil
					}
					if errors.Is(err, io.EOF) {
						time.Sleep(100 * time.Millisecond)
						continue
					}
					return "", err
				}
				if strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text), nil
				}
			}
		},
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(credentials.Access) == "" {
		return fmt.Errorf("oauth login succeeded but did not return an access token")
	}
	if strings.TrimSpace(credentials.Refresh) == "" {
		return fmt.Errorf("oauth login succeeded but did not return a refresh token")
	}

	values := map[string]string{
		"OPENAI_CODEX_TOKEN":         credentials.Access,
		"OPENAI_CODEX_REFRESH_TOKEN": credentials.Refresh,
		"OPENAI_CODEX_TOKEN_EXPIRES": strconv.FormatInt(credentials.Expires, 10),
	}
	for key, value := range values {
		if err := upsertEnvKey(strings.TrimSpace(*envFile), key, value); err != nil {
			return err
		}
	}
	if err := removeEnvKeys(strings.TrimSpace(*envFile), []string{"OPENAI_CODEX_API_KEY"}); err != nil {
		return err
	}
	slog.Info("auth: oauth login completed", "provider", providerID, "env_file", strings.TrimSpace(*envFile))
	fmt.Fprintf(out, "logged in %s and wrote credentials to %s\n", providerID, strings.TrimSpace(*envFile))
	return nil
}

func runAuthUnset(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("auth unset", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	envFile := flags.String("env-file", defaultAuthEnvFilePath(), "env file path")
	provider := flags.String("provider", "", "provider id")
	key := flags.String("key", "", "raw env key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	targetKeys := make([]string, 0)
	if strings.TrimSpace(*provider) != "" {
		spec, ok := findProviderSpec(strings.TrimSpace(*provider))
		if !ok {
			return fmt.Errorf("unknown provider %q", *provider)
		}
		targetKeys = append(targetKeys, spec.EnvKeys...)
	}
	if strings.TrimSpace(*key) != "" {
		targetKeys = append(targetKeys, strings.TrimSpace(*key))
	}
	if len(targetKeys) == 0 {
		return fmt.Errorf("either --provider or --key is required")
	}
	slog.Info("auth: unsetting credential keys", "env_file", strings.TrimSpace(*envFile), "keys", strings.Join(targetKeys, ","))

	if err := removeEnvKeys(strings.TrimSpace(*envFile), targetKeys); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s from %s\n", strings.Join(targetKeys, ", "), strings.TrimSpace(*envFile))
	return nil
}

func findProviderSpec(provider string) (providerAuthSpec, bool) {
	normalized := strings.TrimSpace(strings.ToLower(provider))
	for _, spec := range providerAuthSpecs {
		if spec.Provider == normalized {
			return spec, true
		}
	}
	return providerAuthSpec{}, false
}

func defaultAuthEnvFilePath() string {
	if path := strings.TrimSpace(os.Getenv("GOPHER_ENV_FILE")); path != "" {
		return path
	}
	if os.Geteuid() == 0 {
		return "/etc/gopher/gopher.env"
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".gopher", "gopher.env")
	}
	return "/etc/gopher/gopher.env"
}

func readEnvFileMap(path string) (map[string]string, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(blob)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return values, nil
}

func upsertEnvKey(path, key, value string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create env dir %s: %w", dir, err)
	}

	lines := []string{}
	if blob, err := os.ReadFile(path); err == nil {
		lines = strings.Split(string(blob), "\n")
	}

	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			lines[i] = key + "=" + value
			found = true
		}
	}
	if !found {
		lines = append(lines, key+"="+value)
	}

	output := strings.Join(lines, "\n")
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		return fmt.Errorf("write env file %s: %w", path, err)
	}
	return nil
}

func removeEnvKeys(path string, keys []string) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read env file %s: %w", path, err)
	}
	removeSet := map[string]struct{}{}
	for _, key := range keys {
		removeSet[strings.TrimSpace(key)] = struct{}{}
	}

	lines := strings.Split(string(blob), "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "=") {
			parts := strings.SplitN(trimmed, "=", 2)
			if _, remove := removeSet[parts[0]]; remove {
				continue
			}
		}
		filtered = append(filtered, line)
	}

	output := strings.Join(filtered, "\n")
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		return fmt.Errorf("write env file %s: %w", path, err)
	}
	return nil
}
