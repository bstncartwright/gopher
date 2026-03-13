package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func setGatewayTelegramEnabled(path string, enabled bool) (bool, error) {
	return setGatewayTelegramConfig(path, gatewayTelegramMutation{Enabled: &enabled})
}

type gatewayTelegramMutation struct {
	Enabled           *bool
	Mode              *string
	WebhookListenAddr *string
	WebhookPath       *string
	WebhookURL        *string
	WebhookSecret     *string
}

func setGatewayTelegramConfig(path string, mutation gatewayTelegramMutation) (bool, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		slog.Error("gateway_config_mutation: gateway config path is required")
		return false, fmt.Errorf("gateway config path is required")
	}
	slog.Debug(
		"gateway_config_mutation: applying telegram config mutation",
		"path", target,
		"set_enabled", mutation.Enabled != nil,
		"set_mode", mutation.Mode != nil,
		"set_webhook_listen_addr", mutation.WebhookListenAddr != nil,
		"set_webhook_path", mutation.WebhookPath != nil,
		"set_webhook_url", mutation.WebhookURL != nil,
		"set_webhook_secret", mutation.WebhookSecret != nil,
	)
	blob, err := os.ReadFile(target)
	if err != nil {
		slog.Error("gateway_config_mutation: failed to read gateway config", "path", target, "error", err)
		return false, fmt.Errorf("read gateway config %s: %w", target, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(blob, &doc); err != nil {
		slog.Error("gateway_config_mutation: failed to parse gateway config", "path", target, "error", err)
		return false, fmt.Errorf("parse gateway config %s: %w", target, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	gateway, err := ensureNestedMap(doc, "gateway")
	if err != nil {
		return false, err
	}
	telegram, err := ensureNestedMap(gateway, "telegram")
	if err != nil {
		return false, err
	}
	changed := false
	if mutation.Enabled != nil {
		if current, ok := telegram["enabled"].(bool); !ok || current != *mutation.Enabled {
			telegram["enabled"] = *mutation.Enabled
			changed = true
		}
	}
	if mutation.Mode != nil {
		mode := strings.ToLower(strings.TrimSpace(*mutation.Mode))
		if current, ok := telegram["mode"].(string); !ok || strings.TrimSpace(current) != mode {
			telegram["mode"] = mode
			changed = true
		}
	}
	if mutation.WebhookListenAddr != nil || mutation.WebhookPath != nil || mutation.WebhookURL != nil || mutation.WebhookSecret != nil {
		webhook, err := ensureNestedMap(telegram, "webhook")
		if err != nil {
			return false, err
		}
		if mutation.WebhookListenAddr != nil {
			value := strings.TrimSpace(*mutation.WebhookListenAddr)
			if current, ok := webhook["listen_addr"].(string); !ok || strings.TrimSpace(current) != value {
				webhook["listen_addr"] = value
				changed = true
			}
		}
		if mutation.WebhookPath != nil {
			value := strings.TrimSpace(*mutation.WebhookPath)
			if current, ok := webhook["path"].(string); !ok || strings.TrimSpace(current) != value {
				webhook["path"] = value
				changed = true
			}
		}
		if mutation.WebhookURL != nil {
			value := strings.TrimSpace(*mutation.WebhookURL)
			if current, ok := webhook["url"].(string); !ok || strings.TrimSpace(current) != value {
				webhook["url"] = value
				changed = true
			}
		}
		if mutation.WebhookSecret != nil {
			value := strings.TrimSpace(*mutation.WebhookSecret)
			if current, ok := webhook["secret"].(string); !ok || strings.TrimSpace(current) != value {
				webhook["secret"] = value
				changed = true
			}
		}
	}
	if !changed {
		slog.Debug("gateway_config_mutation: config already up to date", "path", target)
		return false, nil
	}
	updated, err := toml.Marshal(doc)
	if err != nil {
		slog.Error("gateway_config_mutation: failed to serialize gateway config", "path", target, "error", err)
		return false, fmt.Errorf("serialize gateway config %s: %w", target, err)
	}
	if err := writeConfigFileWithBackup(target, updated); err != nil {
		slog.Error("gateway_config_mutation: failed to persist gateway config", "path", target, "error", err)
		return false, err
	}
	slog.Info("gateway_config_mutation: updated gateway telegram config", "path", target)
	return true, nil
}

func ensureNestedMap(parent map[string]any, key string) (map[string]any, error) {
	value, ok := parent[key]
	if !ok || value == nil {
		slog.Debug("gateway_config_mutation: creating nested table", "key", key)
		child := map[string]any{}
		parent[key] = child
		return child, nil
	}
	child, ok := value.(map[string]any)
	if !ok {
		slog.Error("gateway_config_mutation: invalid nested config table", "key", key)
		return nil, fmt.Errorf("invalid gateway config: %s must be a table", key)
	}
	return child, nil
}
