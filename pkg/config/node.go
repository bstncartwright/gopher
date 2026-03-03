package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
	"github.com/pelletier/go-toml/v2"
)

const DefaultNodeNodeID = "node"

type NodeConfig struct {
	NodeID            string
	NATSURL           string
	ConnectTimeout    time.Duration
	ReconnectWait     time.Duration
	HeartbeatInterval time.Duration
	Capabilities      []scheduler.Capability
	PrimaryConfigPath string
	LocalConfigPath   string
}

type NodeOverrides struct {
	NodeID            *string
	NATSURL           *string
	HeartbeatInterval *time.Duration
	Capabilities      *[]scheduler.Capability
}

type NodeLoadOptions struct {
	WorkingDir string
	ConfigPath string
	Env        map[string]string
	Overrides  NodeOverrides
}

type rawNodeRoot struct {
	Node rawNodeConfig `toml:"node"`
}

type rawNodeConfig struct {
	NodeID       *string             `toml:"node_id"`
	NATS         *rawNATSConfig      `toml:"nats"`
	Runtime      *rawNodeRuntime     `toml:"runtime"`
	Capabilities []rawCapabilityItem `toml:"capabilities"`
}

type rawNodeRuntime struct {
	HeartbeatInterval *string `toml:"heartbeat_interval"`
}

func LoadNodeConfig(opts NodeLoadOptions) (NodeConfig, []string, error) {
	cfg := defaultNodeConfig()
	sources := []string{"defaults"}
	slog.Debug("config_node: loading node config", "working_dir", strings.TrimSpace(opts.WorkingDir), "explicit_config_path", strings.TrimSpace(opts.ConfigPath))

	workingDir := strings.TrimSpace(opts.WorkingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return NodeConfig{}, nil, fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = cwd
	}
	workingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return NodeConfig{}, nil, fmt.Errorf("resolve working directory: %w", err)
	}
	slog.Debug("config_node: resolved working directory", "working_dir", workingDir)

	primaryPath, localPath, err := resolveNodeConfigPaths(workingDir, strings.TrimSpace(opts.ConfigPath))
	if err != nil {
		return NodeConfig{}, nil, err
	}
	slog.Debug("config_node: resolved config file paths", "primary_path", primaryPath, "local_path", localPath)

	if primaryPath != "" {
		raw, err := loadRawNodeFile(primaryPath)
		if err != nil {
			return NodeConfig{}, nil, err
		}
		if err := applyRawNodeConfig(&cfg, raw); err != nil {
			return NodeConfig{}, nil, fmt.Errorf("apply %s: %w", primaryPath, err)
		}
		cfg.PrimaryConfigPath = primaryPath
		sources = append(sources, primaryPath)
		slog.Debug("config_node: applied primary config file", "path", primaryPath)
	}

	if localPath != "" {
		raw, err := loadRawNodeFile(localPath)
		if err != nil {
			return NodeConfig{}, nil, err
		}
		if err := applyRawNodeConfig(&cfg, raw); err != nil {
			return NodeConfig{}, nil, fmt.Errorf("apply %s: %w", localPath, err)
		}
		cfg.LocalConfigPath = localPath
		sources = append(sources, localPath)
		slog.Debug("config_node: applied local config file", "path", localPath)
	}

	envMap := opts.Env
	if envMap == nil {
		envMap = currentEnvMap()
	}
	if err := applyNodeEnv(&cfg, envMap); err != nil {
		return NodeConfig{}, nil, err
	}
	if hasNodeEnv(envMap) {
		sources = append(sources, "env:GOPHER_NODE_*")
		slog.Debug("config_node: applied environment overrides")
	}

	if err := applyNodeOverrides(&cfg, opts.Overrides); err != nil {
		return NodeConfig{}, nil, err
	}
	if hasNodeOverrides(opts.Overrides) {
		sources = append(sources, "cli-flags")
		slog.Debug("config_node: applied cli overrides")
	}

	if err := validateNodeConfig(&cfg); err != nil {
		return NodeConfig{}, nil, err
	}
	slog.Info("config_node: node config loaded", "node_id", cfg.NodeID, "nats_url", cfg.NATSURL, "sources", strings.Join(sources, ","))
	return cfg, sources, nil
}

func DefaultNodeTOML() string {
	return strings.TrimSpace(`
[node]
node_id = "node"

[node.nats]
url = "nats://127.0.0.1:4222"
connect_timeout = "5s"
reconnect_wait = "2s"

[node.runtime]
heartbeat_interval = "2s"

[[node.capabilities]]
kind = "agent"
name = "agent"
`) + "\n"
}

func defaultNodeConfig() NodeConfig {
	return NodeConfig{
		NodeID:            DefaultNodeNodeID,
		NATSURL:           "",
		ConnectTimeout:    DefaultClientConnectWait,
		ReconnectWait:     DefaultClientReconnectGap,
		HeartbeatInterval: DefaultHeartbeatInterval,
		Capabilities: []scheduler.Capability{
			{Kind: scheduler.CapabilityAgent, Name: "agent"},
		},
	}
}

func resolveNodeConfigPaths(workingDir string, explicitPath string) (string, string, error) {
	if explicitPath != "" {
		path := explicitPath
		if !filepath.IsAbs(path) {
			path = filepath.Join(workingDir, path)
		}
		path = filepath.Clean(path)
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				return "", "", fmt.Errorf("config file not found: %s", path)
			}
			return "", "", fmt.Errorf("stat config file %s: %w", path, err)
		}
		return path, "", nil
	}

	primary := filepath.Join(workingDir, "node.toml")
	local := filepath.Join(workingDir, "node.local.toml")
	primaryExists := fileExists(primary)
	localExists := fileExists(local)
	if !primaryExists && !localExists {
		return "", "", nil
	}
	if !primaryExists {
		return "", local, nil
	}
	if !localExists {
		return primary, "", nil
	}
	return primary, local, nil
}

func loadRawNodeFile(path string) (rawNodeRoot, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return rawNodeRoot{}, fmt.Errorf("read config file %s: %w", path, err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	var out rawNodeRoot
	if err := decoder.Decode(&out); err != nil {
		return rawNodeRoot{}, fmt.Errorf("decode config file %s: %w", path, err)
	}
	return out, nil
}

func applyRawNodeConfig(cfg *NodeConfig, raw rawNodeRoot) error {
	node := raw.Node
	if node.NodeID != nil {
		cfg.NodeID = strings.TrimSpace(*node.NodeID)
	}
	if node.NATS != nil {
		if node.NATS.URL != nil {
			cfg.NATSURL = strings.TrimSpace(*node.NATS.URL)
		}
		if node.NATS.ConnectTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*node.NATS.ConnectTimeout))
			if err != nil {
				return fmt.Errorf("invalid node.nats.connect_timeout: %w", err)
			}
			cfg.ConnectTimeout = duration
		}
		if node.NATS.ReconnectWait != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*node.NATS.ReconnectWait))
			if err != nil {
				return fmt.Errorf("invalid node.nats.reconnect_wait: %w", err)
			}
			cfg.ReconnectWait = duration
		}
	}
	if node.Runtime != nil && node.Runtime.HeartbeatInterval != nil {
		duration, err := time.ParseDuration(strings.TrimSpace(*node.Runtime.HeartbeatInterval))
		if err != nil {
			return fmt.Errorf("invalid node.runtime.heartbeat_interval: %w", err)
		}
		cfg.HeartbeatInterval = duration
	}
	if node.Capabilities != nil {
		caps := make([]scheduler.Capability, 0, len(node.Capabilities))
		for _, item := range node.Capabilities {
			capability, err := ParseCapability(strings.TrimSpace(item.Kind) + ":" + strings.TrimSpace(item.Name))
			if err != nil {
				return fmt.Errorf("invalid node.capabilities entry: %w", err)
			}
			caps = append(caps, capability)
		}
		cfg.Capabilities = caps
	}
	return nil
}

func applyNodeEnv(cfg *NodeConfig, env map[string]string) error {
	if value, ok := env["GOPHER_NODE_ID"]; ok {
		cfg.NodeID = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_NODE_NATS_URL"]; ok {
		cfg.NATSURL = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_NODE_HEARTBEAT_INTERVAL"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_NODE_HEARTBEAT_INTERVAL: %w", err)
		}
		cfg.HeartbeatInterval = duration
	}
	if value, ok := env["GOPHER_NODE_NATS_CONNECT_TIMEOUT"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_NODE_NATS_CONNECT_TIMEOUT: %w", err)
		}
		cfg.ConnectTimeout = duration
	}
	if value, ok := env["GOPHER_NODE_NATS_RECONNECT_WAIT"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_NODE_NATS_RECONNECT_WAIT: %w", err)
		}
		cfg.ReconnectWait = duration
	}
	if value, ok := env["GOPHER_NODE_CAPABILITIES"]; ok {
		list := strings.Split(strings.TrimSpace(value), ",")
		caps := make([]scheduler.Capability, 0, len(list))
		for _, raw := range list {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			capability, err := ParseCapability(raw)
			if err != nil {
				return fmt.Errorf("invalid GOPHER_NODE_CAPABILITIES: %w", err)
			}
			caps = append(caps, capability)
		}
		cfg.Capabilities = caps
	}
	return nil
}

func applyNodeOverrides(cfg *NodeConfig, overrides NodeOverrides) error {
	if overrides.NodeID != nil {
		cfg.NodeID = strings.TrimSpace(*overrides.NodeID)
	}
	if overrides.NATSURL != nil {
		cfg.NATSURL = strings.TrimSpace(*overrides.NATSURL)
	}
	if overrides.HeartbeatInterval != nil {
		cfg.HeartbeatInterval = *overrides.HeartbeatInterval
	}
	if overrides.Capabilities != nil {
		cfg.Capabilities = append([]scheduler.Capability(nil), (*overrides.Capabilities)...)
	}
	return nil
}

func validateNodeConfig(cfg *NodeConfig) error {
	if cfg == nil {
		return fmt.Errorf("node config is required")
	}
	if strings.TrimSpace(cfg.NodeID) == "" {
		return fmt.Errorf("node.node_id is required")
	}
	if cfg.HeartbeatInterval <= 0 {
		return fmt.Errorf("node.runtime.heartbeat_interval must be > 0")
	}
	if cfg.ConnectTimeout <= 0 {
		return fmt.Errorf("node.nats.connect_timeout must be > 0")
	}
	if cfg.ReconnectWait <= 0 {
		return fmt.Errorf("node.nats.reconnect_wait must be > 0")
	}
	if strings.TrimSpace(cfg.NATSURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(cfg.NATSURL))
		if err != nil || parsed.Scheme == "" {
			return fmt.Errorf("node.nats.url is invalid: %q", cfg.NATSURL)
		}
	}
	for _, capability := range cfg.Capabilities {
		if strings.TrimSpace(capability.Name) == "" {
			return fmt.Errorf("node capability name is required")
		}
	}
	return nil
}

func ValidateNodeConfig(cfg *NodeConfig) error {
	return validateNodeConfig(cfg)
}

func RenderNodeTOML(cfg NodeConfig) (string, error) {
	if err := validateNodeConfig(&cfg); err != nil {
		return "", err
	}
	var body strings.Builder
	body.WriteString("[node]\n")
	body.WriteString(fmt.Sprintf("node_id = %q\n\n", strings.TrimSpace(cfg.NodeID)))

	body.WriteString("[node.nats]\n")
	body.WriteString(fmt.Sprintf("url = %q\n", strings.TrimSpace(cfg.NATSURL)))
	body.WriteString(fmt.Sprintf("connect_timeout = %q\n", cfg.ConnectTimeout.String()))
	body.WriteString(fmt.Sprintf("reconnect_wait = %q\n\n", cfg.ReconnectWait.String()))

	body.WriteString("[node.runtime]\n")
	body.WriteString(fmt.Sprintf("heartbeat_interval = %q\n", cfg.HeartbeatInterval.String()))

	capabilities := append([]scheduler.Capability(nil), cfg.Capabilities...)
	sort.Slice(capabilities, func(i, j int) bool {
		if capabilities[i].Kind == capabilities[j].Kind {
			return strings.TrimSpace(capabilities[i].Name) < strings.TrimSpace(capabilities[j].Name)
		}
		return capabilities[i].Kind < capabilities[j].Kind
	})
	for _, capability := range capabilities {
		body.WriteString("\n[[node.capabilities]]\n")
		body.WriteString(fmt.Sprintf("kind = %q\n", nodeCapabilityKindText(capability.Kind)))
		body.WriteString(fmt.Sprintf("name = %q\n", strings.TrimSpace(capability.Name)))
	}
	body.WriteString("\n")
	return body.String(), nil
}

func WriteNodeConfigFile(path string, cfg NodeConfig) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("config file path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve node config path: %w", err)
	}
	rendered, err := RenderNodeTOML(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("create node config dir %s: %w", filepath.Dir(absPath), err)
	}
	tmpPath := absPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(rendered), 0o644); err != nil {
		return fmt.Errorf("write node config temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		return fmt.Errorf("replace node config file %s: %w", absPath, err)
	}
	return nil
}

func nodeCapabilityKindText(kind scheduler.CapabilityKind) string {
	switch kind {
	case scheduler.CapabilityAgent:
		return "agent"
	case scheduler.CapabilityTool:
		return "tool"
	case scheduler.CapabilitySystem:
		return "system"
	default:
		return "agent"
	}
}

func hasNodeEnv(env map[string]string) bool {
	for key := range env {
		if strings.HasPrefix(key, "GOPHER_NODE_") {
			return true
		}
	}
	return false
}

func hasNodeOverrides(overrides NodeOverrides) bool {
	return overrides.NodeID != nil ||
		overrides.NATSURL != nil ||
		overrides.HeartbeatInterval != nil ||
		overrides.Capabilities != nil
}
