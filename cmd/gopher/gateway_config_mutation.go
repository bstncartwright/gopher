package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

func setGatewayTelegramEnabled(path string, enabled bool) (bool, error) {
	target := strings.TrimSpace(path)
	if target == "" {
		return false, fmt.Errorf("gateway config path is required")
	}
	blob, err := os.ReadFile(target)
	if err != nil {
		return false, fmt.Errorf("read gateway config %s: %w", target, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(blob, &doc); err != nil {
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
	if current, ok := telegram["enabled"].(bool); ok && current == enabled {
		return false, nil
	}
	telegram["enabled"] = enabled
	updated, err := toml.Marshal(doc)
	if err != nil {
		return false, fmt.Errorf("serialize gateway config %s: %w", target, err)
	}
	if err := writeConfigFileWithBackup(target, updated); err != nil {
		return false, err
	}
	return true, nil
}

func ensureNestedMap(parent map[string]any, key string) (map[string]any, error) {
	value, ok := parent[key]
	if !ok || value == nil {
		child := map[string]any{}
		parent[key] = child
		return child, nil
	}
	child, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("invalid gateway config: %s must be a table", key)
	}
	return child, nil
}
