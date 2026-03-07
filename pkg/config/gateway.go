package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
	"github.com/pelletier/go-toml/v2"
)

const (
	DefaultGatewayNodeID       = "gateway"
	DefaultHeartbeatInterval   = 2 * time.Second
	DefaultPruneInterval       = 3 * time.Second
	DefaultCronPollInterval    = 1 * time.Second
	DefaultClientConnectWait   = 5 * time.Second
	DefaultClientReconnectGap  = 2 * time.Second
	DefaultA2ATaskPollInterval = 2 * time.Second
)

type GatewayConfig struct {
	NodeID            string
	GatewayNodeID     string
	NATSURL           string
	ConnectTimeout    time.Duration
	ReconnectWait     time.Duration
	HeartbeatInterval time.Duration
	PruneInterval     time.Duration
	Capabilities      []scheduler.Capability
	Telegram          TelegramConfig
	Panel             PanelConfig
	Cron              CronConfig
	Update            UpdateConfig
	A2A               A2AConfig
	PrimaryConfigPath string
	LocalConfigPath   string
}

type TelegramConfig struct {
	Enabled       bool
	Mode          string
	BotToken      string
	PollInterval  time.Duration
	PollTimeout   time.Duration
	AllowedUserID string
	AllowedChatID string
	Webhook       TelegramWebhookConfig
}

type TelegramWebhookConfig struct {
	ListenAddr string
	Path       string
	URL        string
	Secret     string
}

type PanelConfig struct {
	ListenAddr      string
	CaptureThinking bool
}

type CronConfig struct {
	Enabled         bool
	PollInterval    time.Duration
	DefaultTimezone string
}

type UpdateConfig struct {
	Enabled            bool
	RepoOwner          string
	RepoName           string
	Channel            string
	CheckInterval      time.Duration
	BinaryAssetPattern string
}

type A2AConfig struct {
	Enabled                   bool
	DiscoveryTimeout          time.Duration
	RequestTimeout            time.Duration
	TaskPollInterval          time.Duration
	StreamIdleTimeout         time.Duration
	CardRefreshInterval       time.Duration
	ResumeScanInterval        time.Duration
	CompatLegacyWellKnownPath bool
	Remotes                   []A2ARemoteConfig
}

type A2ARemoteConfig struct {
	ID               string
	DisplayName      string
	BaseURL          string
	CardURL          string
	Enabled          bool
	Headers          map[string]string
	AllowInsecureTLS bool
	RequestTimeout   time.Duration
	Tags             []string
}

type GatewayOverrides struct {
	NodeID                *string
	GatewayNodeID         *string
	NATSURL               *string
	HeartbeatInterval     *time.Duration
	PruneInterval         *time.Duration
	Capabilities          *[]scheduler.Capability
	TelegramEnabled       *bool
	TelegramBotToken      *string
	TelegramPollInterval  *time.Duration
	TelegramPollTimeout   *time.Duration
	TelegramAllowedUserID *string
	TelegramAllowedChatID *string
	TelegramMode          *string
	TelegramWebhookListen *string
	TelegramWebhookPath   *string
	TelegramWebhookURL    *string
	TelegramWebhookSecret *string
	PanelListenAddr       *string
	PanelCaptureThinking  *bool
	CronEnabled           *bool
	CronPollInterval      *time.Duration
	CronTimezone          *string
	UpdateEnabled         *bool
	UpdateRepoOwner       *string
	UpdateRepoName        *string
	UpdateChannel         *string
	UpdateCheckInterval   *time.Duration
	UpdateAssetPattern    *string
}

type GatewayLoadOptions struct {
	WorkingDir string
	ConfigPath string
	Env        map[string]string
	Overrides  GatewayOverrides
}

type rawGatewayRoot struct {
	Gateway rawGatewayConfig `toml:"gateway"`
}

type rawGatewayConfig struct {
	NodeID       *string             `toml:"node_id"`
	GatewayNode  *string             `toml:"gateway_id"`
	NATS         *rawNATSConfig      `toml:"nats"`
	Runtime      *rawRuntimeConfig   `toml:"runtime"`
	Capabilities []rawCapabilityItem `toml:"capabilities"`
	Telegram     *rawTelegramConfig  `toml:"telegram"`
	Panel        *rawPanelConfig     `toml:"panel"`
	Cron         *rawCronConfig      `toml:"cron"`
	Update       *rawUpdateConfig    `toml:"update"`
	A2A          *rawA2AConfig       `toml:"a2a"`
}

type rawNATSConfig struct {
	URL            *string `toml:"url"`
	ConnectTimeout *string `toml:"connect_timeout"`
	ReconnectWait  *string `toml:"reconnect_wait"`
}

type rawRuntimeConfig struct {
	HeartbeatInterval *string `toml:"heartbeat_interval"`
	PruneInterval     *string `toml:"prune_interval"`
}

type rawCapabilityItem struct {
	Kind string `toml:"kind"`
	Name string `toml:"name"`
}

type rawTelegramConfig struct {
	Enabled       *bool                     `toml:"enabled"`
	Mode          *string                   `toml:"mode"`
	BotToken      *string                   `toml:"bot_token"`
	PollInterval  *string                   `toml:"poll_interval"`
	PollTimeout   *string                   `toml:"poll_timeout"`
	AllowedUserID *string                   `toml:"allowed_user_id"`
	AllowedChatID *string                   `toml:"allowed_chat_id"`
	Webhook       *rawTelegramWebhookConfig `toml:"webhook"`
}

type rawTelegramWebhookConfig struct {
	ListenAddr *string `toml:"listen_addr"`
	Path       *string `toml:"path"`
	URL        *string `toml:"url"`
	Secret     *string `toml:"secret"`
}

type rawPanelConfig struct {
	ListenAddr      *string `toml:"listen_addr"`
	CaptureThinking *bool   `toml:"capture_thinking"`
}

type rawCronConfig struct {
	Enabled         *bool   `toml:"enabled"`
	PollInterval    *string `toml:"poll_interval"`
	DefaultTimezone *string `toml:"default_timezone"`
}

type rawUpdateConfig struct {
	Enabled            *bool   `toml:"enabled"`
	RepoOwner          *string `toml:"repo_owner"`
	RepoName           *string `toml:"repo_name"`
	Channel            *string `toml:"channel"`
	CheckInterval      *string `toml:"check_interval"`
	BinaryAssetPattern *string `toml:"binary_asset_pattern"`
}

type rawA2AConfig struct {
	Enabled                   *bool          `toml:"enabled"`
	DiscoveryTimeout          *string        `toml:"discovery_timeout"`
	RequestTimeout            *string        `toml:"request_timeout"`
	TaskPollInterval          *string        `toml:"task_poll_interval"`
	StreamIdleTimeout         *string        `toml:"stream_idle_timeout"`
	CardRefreshInterval       *string        `toml:"card_refresh_interval"`
	ResumeScanInterval        *string        `toml:"resume_scan_interval"`
	CompatLegacyWellKnownPath *bool          `toml:"compat_legacy_well_known_path"`
	Remotes                   []rawA2ARemote `toml:"remotes"`
}

type rawA2ARemote struct {
	ID               *string           `toml:"id"`
	DisplayName      *string           `toml:"display_name"`
	BaseURL          *string           `toml:"base_url"`
	CardURL          *string           `toml:"card_url"`
	Enabled          *bool             `toml:"enabled"`
	Headers          map[string]string `toml:"headers"`
	AllowInsecureTLS *bool             `toml:"allow_insecure_tls"`
	RequestTimeout   *string           `toml:"request_timeout"`
	Tags             []string          `toml:"tags"`
}

func LoadGatewayConfig(opts GatewayLoadOptions) (GatewayConfig, []string, error) {
	cfg := defaultGatewayConfig()
	sources := []string{"defaults"}
	slog.Debug("config_gateway: loading gateway config", "working_dir", strings.TrimSpace(opts.WorkingDir), "explicit_config_path", strings.TrimSpace(opts.ConfigPath))

	workingDir := strings.TrimSpace(opts.WorkingDir)
	if workingDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return GatewayConfig{}, nil, fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = cwd
	}
	workingDir, err := filepath.Abs(workingDir)
	if err != nil {
		return GatewayConfig{}, nil, fmt.Errorf("resolve working directory: %w", err)
	}
	slog.Debug("config_gateway: resolved working directory", "working_dir", workingDir)

	primaryPath, localPath, err := resolveConfigPaths(workingDir, strings.TrimSpace(opts.ConfigPath))
	if err != nil {
		return GatewayConfig{}, nil, err
	}
	slog.Debug("config_gateway: resolved config file paths", "primary_path", primaryPath, "local_path", localPath)

	if primaryPath != "" {
		raw, err := loadRawGatewayFile(primaryPath)
		if err != nil {
			return GatewayConfig{}, nil, err
		}
		if err := applyRawGatewayConfig(&cfg, raw); err != nil {
			return GatewayConfig{}, nil, fmt.Errorf("apply %s: %w", primaryPath, err)
		}
		cfg.PrimaryConfigPath = primaryPath
		sources = append(sources, primaryPath)
		slog.Debug("config_gateway: applied primary config file", "path", primaryPath)
	}

	if localPath != "" {
		raw, err := loadRawGatewayFile(localPath)
		if err != nil {
			return GatewayConfig{}, nil, err
		}
		if err := applyRawGatewayConfig(&cfg, raw); err != nil {
			return GatewayConfig{}, nil, fmt.Errorf("apply %s: %w", localPath, err)
		}
		cfg.LocalConfigPath = localPath
		sources = append(sources, localPath)
		slog.Debug("config_gateway: applied local config file", "path", localPath)
	}

	envMap := opts.Env
	if envMap == nil {
		envMap = currentEnvMap()
	}
	if err := applyGatewayEnv(&cfg, envMap); err != nil {
		return GatewayConfig{}, nil, err
	}
	if hasGatewayEnv(envMap) {
		sources = append(sources, "env:GOPHER_*")
		slog.Debug("config_gateway: applied environment overrides")
	}

	if err := applyGatewayOverrides(&cfg, opts.Overrides); err != nil {
		return GatewayConfig{}, nil, err
	}
	if hasGatewayOverrides(opts.Overrides) {
		sources = append(sources, "cli-flags")
		slog.Debug("config_gateway: applied cli overrides")
	}

	if err := expandGatewayA2AEnvTemplates(&cfg, envMap); err != nil {
		return GatewayConfig{}, nil, err
	}

	if err := validateGatewayConfig(&cfg); err != nil {
		return GatewayConfig{}, nil, err
	}
	slog.Info("config_gateway: gateway config loaded", "node_id", cfg.NodeID, "gateway_node_id", cfg.GatewayNodeID, "nats_url", cfg.NATSURL, "sources", strings.Join(sources, ","))
	return cfg, sources, nil
}

func DefaultGatewayTOML() string {
	return strings.TrimSpace(`
[gateway]
node_id = "gateway"
gateway_id = "gateway"

[gateway.nats]
url = "nats://127.0.0.1:4222"
connect_timeout = "5s"
reconnect_wait = "2s"

[gateway.runtime]
heartbeat_interval = "2s"
prune_interval = "3s"

[[gateway.capabilities]]
kind = "agent"
name = "agent"

[gateway.telegram]
enabled = false
mode = "polling"
bot_token = "replace-telegram-bot-token"
poll_interval = "2s"
poll_timeout = "30s"
allowed_user_id = ""
allowed_chat_id = ""

[gateway.telegram.webhook]
listen_addr = "127.0.0.1:29330"
path = "/_gopher/telegram/webhook"
url = ""
secret = ""

[gateway.panel]
listen_addr = "127.0.0.1:29329"
capture_thinking = true

[gateway.cron]
enabled = true
poll_interval = "1s"
default_timezone = "UTC"

[gateway.update]
enabled = false
repo_owner = "replace-owner"
repo_name = "replace-private-repo"
channel = "stable"
check_interval = "1h"
binary_asset_pattern = "linux"

[gateway.a2a]
enabled = false
discovery_timeout = "5s"
request_timeout = "30s"
task_poll_interval = "2s"
stream_idle_timeout = "30s"
card_refresh_interval = "5m"
resume_scan_interval = "10s"
compat_legacy_well_known_path = true
`) + "\n"
}

func ParseCapability(raw string) (scheduler.Capability, error) {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	if len(parts) != 2 {
		return scheduler.Capability{}, fmt.Errorf("invalid capability %q: expected kind:name", raw)
	}
	name := strings.TrimSpace(parts[1])
	if name == "" {
		return scheduler.Capability{}, fmt.Errorf("invalid capability %q: name is required", raw)
	}

	var kind scheduler.CapabilityKind
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "agent":
		kind = scheduler.CapabilityAgent
	case "tool":
		kind = scheduler.CapabilityTool
	case "system":
		kind = scheduler.CapabilitySystem
	default:
		return scheduler.Capability{}, fmt.Errorf("invalid capability %q: unknown kind %q", raw, strings.TrimSpace(parts[0]))
	}
	return scheduler.Capability{Kind: kind, Name: name}, nil
}

func defaultGatewayConfig() GatewayConfig {
	return GatewayConfig{
		NodeID:            DefaultGatewayNodeID,
		GatewayNodeID:     DefaultGatewayNodeID,
		NATSURL:           "",
		ConnectTimeout:    DefaultClientConnectWait,
		ReconnectWait:     DefaultClientReconnectGap,
		HeartbeatInterval: DefaultHeartbeatInterval,
		PruneInterval:     DefaultPruneInterval,
		Capabilities: []scheduler.Capability{
			{Kind: scheduler.CapabilityAgent, Name: "agent"},
		},
		Telegram: TelegramConfig{
			Enabled:       false,
			Mode:          "polling",
			BotToken:      "",
			PollInterval:  2 * time.Second,
			PollTimeout:   30 * time.Second,
			AllowedUserID: "",
			AllowedChatID: "",
			Webhook: TelegramWebhookConfig{
				ListenAddr: "127.0.0.1:29330",
				Path:       "/_gopher/telegram/webhook",
				URL:        "",
				Secret:     "",
			},
		},
		Panel: PanelConfig{
			ListenAddr:      "127.0.0.1:29329",
			CaptureThinking: true,
		},
		Cron: CronConfig{
			Enabled:         true,
			PollInterval:    DefaultCronPollInterval,
			DefaultTimezone: "UTC",
		},
		Update: UpdateConfig{
			Enabled:            false,
			RepoOwner:          "",
			RepoName:           "",
			Channel:            "stable",
			CheckInterval:      time.Hour,
			BinaryAssetPattern: "linux",
		},
		A2A: A2AConfig{
			Enabled:                   false,
			DiscoveryTimeout:          5 * time.Second,
			RequestTimeout:            30 * time.Second,
			TaskPollInterval:          DefaultA2ATaskPollInterval,
			StreamIdleTimeout:         30 * time.Second,
			CardRefreshInterval:       5 * time.Minute,
			ResumeScanInterval:        10 * time.Second,
			CompatLegacyWellKnownPath: true,
			Remotes:                   []A2ARemoteConfig{},
		},
	}
}

func resolveConfigPaths(workingDir string, explicitPath string) (string, string, error) {
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

	primary := filepath.Join(workingDir, "gopher.toml")
	local := filepath.Join(workingDir, "gopher.local.toml")
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

func loadRawGatewayFile(path string) (rawGatewayRoot, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return rawGatewayRoot{}, fmt.Errorf("read config file %s: %w", path, err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(blob))
	decoder.DisallowUnknownFields()
	var out rawGatewayRoot
	if err := decoder.Decode(&out); err != nil {
		return rawGatewayRoot{}, fmt.Errorf("decode config file %s: %w", path, err)
	}
	return out, nil
}

func applyRawGatewayConfig(cfg *GatewayConfig, raw rawGatewayRoot) error {
	gateway := raw.Gateway
	if gateway.NodeID != nil {
		cfg.NodeID = strings.TrimSpace(*gateway.NodeID)
	}
	if gateway.GatewayNode != nil {
		cfg.GatewayNodeID = strings.TrimSpace(*gateway.GatewayNode)
	}
	if gateway.NATS != nil {
		if gateway.NATS.URL != nil {
			cfg.NATSURL = strings.TrimSpace(*gateway.NATS.URL)
		}
		if gateway.NATS.ConnectTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.NATS.ConnectTimeout))
			if err != nil {
				return fmt.Errorf("invalid gateway.nats.connect_timeout: %w", err)
			}
			cfg.ConnectTimeout = duration
		}
		if gateway.NATS.ReconnectWait != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.NATS.ReconnectWait))
			if err != nil {
				return fmt.Errorf("invalid gateway.nats.reconnect_wait: %w", err)
			}
			cfg.ReconnectWait = duration
		}
	}
	if gateway.Runtime != nil {
		if gateway.Runtime.HeartbeatInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.Runtime.HeartbeatInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.runtime.heartbeat_interval: %w", err)
			}
			cfg.HeartbeatInterval = duration
		}
		if gateway.Runtime.PruneInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.Runtime.PruneInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.runtime.prune_interval: %w", err)
			}
			cfg.PruneInterval = duration
		}
	}
	if gateway.Capabilities != nil {
		caps := make([]scheduler.Capability, 0, len(gateway.Capabilities))
		for _, item := range gateway.Capabilities {
			capability, err := ParseCapability(strings.TrimSpace(item.Kind) + ":" + strings.TrimSpace(item.Name))
			if err != nil {
				return fmt.Errorf("invalid gateway.capabilities entry: %w", err)
			}
			caps = append(caps, capability)
		}
		cfg.Capabilities = caps
	}
	if gateway.Telegram != nil {
		if gateway.Telegram.Enabled != nil {
			cfg.Telegram.Enabled = *gateway.Telegram.Enabled
		}
		if gateway.Telegram.Mode != nil {
			cfg.Telegram.Mode = strings.TrimSpace(*gateway.Telegram.Mode)
		}
		if gateway.Telegram.BotToken != nil {
			cfg.Telegram.BotToken = strings.TrimSpace(*gateway.Telegram.BotToken)
		}
		if gateway.Telegram.PollInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.Telegram.PollInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.telegram.poll_interval: %w", err)
			}
			cfg.Telegram.PollInterval = duration
		}
		if gateway.Telegram.PollTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.Telegram.PollTimeout))
			if err != nil {
				return fmt.Errorf("invalid gateway.telegram.poll_timeout: %w", err)
			}
			cfg.Telegram.PollTimeout = duration
		}
		if gateway.Telegram.AllowedUserID != nil {
			cfg.Telegram.AllowedUserID = strings.TrimSpace(*gateway.Telegram.AllowedUserID)
		}
		if gateway.Telegram.AllowedChatID != nil {
			cfg.Telegram.AllowedChatID = strings.TrimSpace(*gateway.Telegram.AllowedChatID)
		}
		if gateway.Telegram.Webhook != nil {
			if gateway.Telegram.Webhook.ListenAddr != nil {
				cfg.Telegram.Webhook.ListenAddr = strings.TrimSpace(*gateway.Telegram.Webhook.ListenAddr)
			}
			if gateway.Telegram.Webhook.Path != nil {
				cfg.Telegram.Webhook.Path = strings.TrimSpace(*gateway.Telegram.Webhook.Path)
			}
			if gateway.Telegram.Webhook.URL != nil {
				cfg.Telegram.Webhook.URL = strings.TrimSpace(*gateway.Telegram.Webhook.URL)
			}
			if gateway.Telegram.Webhook.Secret != nil {
				cfg.Telegram.Webhook.Secret = strings.TrimSpace(*gateway.Telegram.Webhook.Secret)
			}
		}
	}
	if gateway.Panel != nil {
		if gateway.Panel.ListenAddr != nil {
			cfg.Panel.ListenAddr = strings.TrimSpace(*gateway.Panel.ListenAddr)
		}
		if gateway.Panel.CaptureThinking != nil {
			cfg.Panel.CaptureThinking = *gateway.Panel.CaptureThinking
		}
	}
	if gateway.Cron != nil {
		if gateway.Cron.Enabled != nil {
			cfg.Cron.Enabled = *gateway.Cron.Enabled
		}
		if gateway.Cron.PollInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.Cron.PollInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.cron.poll_interval: %w", err)
			}
			cfg.Cron.PollInterval = duration
		}
		if gateway.Cron.DefaultTimezone != nil {
			cfg.Cron.DefaultTimezone = strings.TrimSpace(*gateway.Cron.DefaultTimezone)
		}
	}
	if gateway.Update != nil {
		if gateway.Update.Enabled != nil {
			cfg.Update.Enabled = *gateway.Update.Enabled
		}
		if gateway.Update.RepoOwner != nil {
			cfg.Update.RepoOwner = strings.TrimSpace(*gateway.Update.RepoOwner)
		}
		if gateway.Update.RepoName != nil {
			cfg.Update.RepoName = strings.TrimSpace(*gateway.Update.RepoName)
		}
		if gateway.Update.Channel != nil {
			cfg.Update.Channel = strings.TrimSpace(*gateway.Update.Channel)
		}
		if gateway.Update.CheckInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.Update.CheckInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.update.check_interval: %w", err)
			}
			cfg.Update.CheckInterval = duration
		}
		if gateway.Update.BinaryAssetPattern != nil {
			cfg.Update.BinaryAssetPattern = strings.TrimSpace(*gateway.Update.BinaryAssetPattern)
		}
	}
	if gateway.A2A != nil {
		if gateway.A2A.Enabled != nil {
			cfg.A2A.Enabled = *gateway.A2A.Enabled
		}
		if gateway.A2A.DiscoveryTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.A2A.DiscoveryTimeout))
			if err != nil {
				return fmt.Errorf("invalid gateway.a2a.discovery_timeout: %w", err)
			}
			cfg.A2A.DiscoveryTimeout = duration
		}
		if gateway.A2A.RequestTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.A2A.RequestTimeout))
			if err != nil {
				return fmt.Errorf("invalid gateway.a2a.request_timeout: %w", err)
			}
			cfg.A2A.RequestTimeout = duration
		}
		if gateway.A2A.TaskPollInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.A2A.TaskPollInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.a2a.task_poll_interval: %w", err)
			}
			cfg.A2A.TaskPollInterval = duration
		}
		if gateway.A2A.StreamIdleTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.A2A.StreamIdleTimeout))
			if err != nil {
				return fmt.Errorf("invalid gateway.a2a.stream_idle_timeout: %w", err)
			}
			cfg.A2A.StreamIdleTimeout = duration
		}
		if gateway.A2A.CardRefreshInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.A2A.CardRefreshInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.a2a.card_refresh_interval: %w", err)
			}
			cfg.A2A.CardRefreshInterval = duration
		}
		if gateway.A2A.ResumeScanInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.A2A.ResumeScanInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.a2a.resume_scan_interval: %w", err)
			}
			cfg.A2A.ResumeScanInterval = duration
		}
		if gateway.A2A.CompatLegacyWellKnownPath != nil {
			cfg.A2A.CompatLegacyWellKnownPath = *gateway.A2A.CompatLegacyWellKnownPath
		}
		if gateway.A2A.Remotes != nil {
			remotes := make([]A2ARemoteConfig, 0, len(gateway.A2A.Remotes))
			for _, item := range gateway.A2A.Remotes {
				remote := A2ARemoteConfig{
					Enabled: true,
					Headers: map[string]string{},
					Tags:    []string{},
				}
				if item.ID != nil {
					remote.ID = strings.TrimSpace(*item.ID)
				}
				if item.DisplayName != nil {
					remote.DisplayName = strings.TrimSpace(*item.DisplayName)
				}
				if item.BaseURL != nil {
					remote.BaseURL = strings.TrimSpace(*item.BaseURL)
				}
				if item.CardURL != nil {
					remote.CardURL = strings.TrimSpace(*item.CardURL)
				}
				if item.Enabled != nil {
					remote.Enabled = *item.Enabled
				}
				if item.Headers != nil {
					remote.Headers = make(map[string]string, len(item.Headers))
					for key, value := range item.Headers {
						remote.Headers[key] = value
					}
				}
				if item.AllowInsecureTLS != nil {
					remote.AllowInsecureTLS = *item.AllowInsecureTLS
				}
				if item.RequestTimeout != nil {
					duration, err := time.ParseDuration(strings.TrimSpace(*item.RequestTimeout))
					if err != nil {
						return fmt.Errorf("invalid gateway.a2a.remotes.request_timeout: %w", err)
					}
					remote.RequestTimeout = duration
				}
				remote.Tags = append([]string(nil), item.Tags...)
				remotes = append(remotes, remote)
			}
			cfg.A2A.Remotes = remotes
		}
	}
	return nil
}

func applyGatewayEnv(cfg *GatewayConfig, env map[string]string) error {
	if value, ok := env["GOPHER_GATEWAY_NODE_ID"]; ok {
		cfg.NodeID = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_ID"]; ok {
		cfg.GatewayNodeID = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_NATS_URL"]; ok {
		cfg.NATSURL = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_HEARTBEAT_INTERVAL"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_HEARTBEAT_INTERVAL: %w", err)
		}
		cfg.HeartbeatInterval = duration
	}
	if value, ok := env["GOPHER_GATEWAY_PRUNE_INTERVAL"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_PRUNE_INTERVAL: %w", err)
		}
		cfg.PruneInterval = duration
	}
	if value, ok := env["GOPHER_GATEWAY_NATS_CONNECT_TIMEOUT"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_NATS_CONNECT_TIMEOUT: %w", err)
		}
		cfg.ConnectTimeout = duration
	}
	if value, ok := env["GOPHER_GATEWAY_NATS_RECONNECT_WAIT"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_NATS_RECONNECT_WAIT: %w", err)
		}
		cfg.ReconnectWait = duration
	}
	if value, ok := env["GOPHER_GATEWAY_CAPABILITIES"]; ok {
		list := strings.Split(strings.TrimSpace(value), ",")
		caps := make([]scheduler.Capability, 0, len(list))
		for _, raw := range list {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			capability, err := ParseCapability(raw)
			if err != nil {
				return fmt.Errorf("invalid GOPHER_GATEWAY_CAPABILITIES: %w", err)
			}
			caps = append(caps, capability)
		}
		cfg.Capabilities = caps
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_ENABLED"]; ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_TELEGRAM_ENABLED: %w", err)
		}
		cfg.Telegram.Enabled = enabled
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_BOT_TOKEN"]; ok {
		cfg.Telegram.BotToken = strings.TrimSpace(value)
	} else if value, ok := env["GOPHER_TELEGRAM_BOT_TOKEN"]; ok {
		// Backward compatibility for onboarding-managed env files.
		cfg.Telegram.BotToken = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_POLL_INTERVAL"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_TELEGRAM_POLL_INTERVAL: %w", err)
		}
		cfg.Telegram.PollInterval = duration
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_POLL_TIMEOUT"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_TELEGRAM_POLL_TIMEOUT: %w", err)
		}
		cfg.Telegram.PollTimeout = duration
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_ALLOWED_USER_ID"]; ok {
		cfg.Telegram.AllowedUserID = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_ALLOWED_CHAT_ID"]; ok {
		cfg.Telegram.AllowedChatID = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_MODE"]; ok {
		cfg.Telegram.Mode = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_WEBHOOK_LISTEN_ADDR"]; ok {
		cfg.Telegram.Webhook.ListenAddr = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_WEBHOOK_PATH"]; ok {
		cfg.Telegram.Webhook.Path = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_WEBHOOK_URL"]; ok {
		cfg.Telegram.Webhook.URL = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_TELEGRAM_WEBHOOK_SECRET"]; ok {
		cfg.Telegram.Webhook.Secret = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_PANEL_LISTEN_ADDR"]; ok {
		cfg.Panel.ListenAddr = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_PANEL_CAPTURE_THINKING"]; ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_PANEL_CAPTURE_THINKING: %w", err)
		}
		cfg.Panel.CaptureThinking = enabled
	}
	if value, ok := env["GOPHER_GATEWAY_CRON_ENABLED"]; ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_CRON_ENABLED: %w", err)
		}
		cfg.Cron.Enabled = enabled
	}
	if value, ok := env["GOPHER_GATEWAY_CRON_POLL_INTERVAL"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_CRON_POLL_INTERVAL: %w", err)
		}
		cfg.Cron.PollInterval = duration
	}
	if value, ok := env["GOPHER_GATEWAY_CRON_DEFAULT_TIMEZONE"]; ok {
		cfg.Cron.DefaultTimezone = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_UPDATE_ENABLED"]; ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_UPDATE_ENABLED: %w", err)
		}
		cfg.Update.Enabled = enabled
	}
	if value, ok := env["GOPHER_GATEWAY_UPDATE_REPO_OWNER"]; ok {
		cfg.Update.RepoOwner = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_UPDATE_REPO_NAME"]; ok {
		cfg.Update.RepoName = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_UPDATE_CHANNEL"]; ok {
		cfg.Update.Channel = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_UPDATE_CHECK_INTERVAL"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_UPDATE_CHECK_INTERVAL: %w", err)
		}
		cfg.Update.CheckInterval = duration
	}
	if value, ok := env["GOPHER_GATEWAY_UPDATE_BINARY_ASSET_PATTERN"]; ok {
		cfg.Update.BinaryAssetPattern = strings.TrimSpace(value)
	}
	return nil
}

func applyGatewayOverrides(cfg *GatewayConfig, overrides GatewayOverrides) error {
	if overrides.NodeID != nil {
		cfg.NodeID = strings.TrimSpace(*overrides.NodeID)
	}
	if overrides.GatewayNodeID != nil {
		cfg.GatewayNodeID = strings.TrimSpace(*overrides.GatewayNodeID)
	}
	if overrides.NATSURL != nil {
		cfg.NATSURL = strings.TrimSpace(*overrides.NATSURL)
	}
	if overrides.HeartbeatInterval != nil {
		cfg.HeartbeatInterval = *overrides.HeartbeatInterval
	}
	if overrides.PruneInterval != nil {
		cfg.PruneInterval = *overrides.PruneInterval
	}
	if overrides.Capabilities != nil {
		cfg.Capabilities = append([]scheduler.Capability(nil), (*overrides.Capabilities)...)
	}
	if overrides.TelegramEnabled != nil {
		cfg.Telegram.Enabled = *overrides.TelegramEnabled
	}
	if overrides.TelegramBotToken != nil {
		cfg.Telegram.BotToken = strings.TrimSpace(*overrides.TelegramBotToken)
	}
	if overrides.TelegramPollInterval != nil {
		cfg.Telegram.PollInterval = *overrides.TelegramPollInterval
	}
	if overrides.TelegramPollTimeout != nil {
		cfg.Telegram.PollTimeout = *overrides.TelegramPollTimeout
	}
	if overrides.TelegramAllowedUserID != nil {
		cfg.Telegram.AllowedUserID = strings.TrimSpace(*overrides.TelegramAllowedUserID)
	}
	if overrides.TelegramAllowedChatID != nil {
		cfg.Telegram.AllowedChatID = strings.TrimSpace(*overrides.TelegramAllowedChatID)
	}
	if overrides.TelegramMode != nil {
		cfg.Telegram.Mode = strings.TrimSpace(*overrides.TelegramMode)
	}
	if overrides.TelegramWebhookListen != nil {
		cfg.Telegram.Webhook.ListenAddr = strings.TrimSpace(*overrides.TelegramWebhookListen)
	}
	if overrides.TelegramWebhookPath != nil {
		cfg.Telegram.Webhook.Path = strings.TrimSpace(*overrides.TelegramWebhookPath)
	}
	if overrides.TelegramWebhookURL != nil {
		cfg.Telegram.Webhook.URL = strings.TrimSpace(*overrides.TelegramWebhookURL)
	}
	if overrides.TelegramWebhookSecret != nil {
		cfg.Telegram.Webhook.Secret = strings.TrimSpace(*overrides.TelegramWebhookSecret)
	}
	if overrides.PanelListenAddr != nil {
		cfg.Panel.ListenAddr = strings.TrimSpace(*overrides.PanelListenAddr)
	}
	if overrides.PanelCaptureThinking != nil {
		cfg.Panel.CaptureThinking = *overrides.PanelCaptureThinking
	}
	if overrides.CronEnabled != nil {
		cfg.Cron.Enabled = *overrides.CronEnabled
	}
	if overrides.CronPollInterval != nil {
		cfg.Cron.PollInterval = *overrides.CronPollInterval
	}
	if overrides.CronTimezone != nil {
		cfg.Cron.DefaultTimezone = strings.TrimSpace(*overrides.CronTimezone)
	}
	if overrides.UpdateEnabled != nil {
		cfg.Update.Enabled = *overrides.UpdateEnabled
	}
	if overrides.UpdateRepoOwner != nil {
		cfg.Update.RepoOwner = strings.TrimSpace(*overrides.UpdateRepoOwner)
	}
	if overrides.UpdateRepoName != nil {
		cfg.Update.RepoName = strings.TrimSpace(*overrides.UpdateRepoName)
	}
	if overrides.UpdateChannel != nil {
		cfg.Update.Channel = strings.TrimSpace(*overrides.UpdateChannel)
	}
	if overrides.UpdateCheckInterval != nil {
		cfg.Update.CheckInterval = *overrides.UpdateCheckInterval
	}
	if overrides.UpdateAssetPattern != nil {
		cfg.Update.BinaryAssetPattern = strings.TrimSpace(*overrides.UpdateAssetPattern)
	}
	return nil
}

func validateGatewayConfig(cfg *GatewayConfig) error {
	if cfg == nil {
		return fmt.Errorf("gateway config is required")
	}
	if strings.TrimSpace(cfg.NodeID) == "" {
		return fmt.Errorf("gateway.node_id is required")
	}
	if strings.TrimSpace(cfg.GatewayNodeID) == "" {
		cfg.GatewayNodeID = cfg.NodeID
	}
	if cfg.HeartbeatInterval <= 0 {
		return fmt.Errorf("gateway.runtime.heartbeat_interval must be > 0")
	}
	if cfg.PruneInterval <= 0 {
		return fmt.Errorf("gateway.runtime.prune_interval must be > 0")
	}
	if cfg.ConnectTimeout <= 0 {
		return fmt.Errorf("gateway.nats.connect_timeout must be > 0")
	}
	if cfg.ReconnectWait <= 0 {
		return fmt.Errorf("gateway.nats.reconnect_wait must be > 0")
	}
	if cfg.Cron.PollInterval <= 0 {
		return fmt.Errorf("gateway.cron.poll_interval must be > 0")
	}
	if cfg.Update.CheckInterval <= 0 {
		return fmt.Errorf("gateway.update.check_interval must be > 0")
	}
	if cfg.A2A.DiscoveryTimeout <= 0 {
		return fmt.Errorf("gateway.a2a.discovery_timeout must be > 0")
	}
	if cfg.A2A.RequestTimeout <= 0 {
		return fmt.Errorf("gateway.a2a.request_timeout must be > 0")
	}
	if cfg.A2A.TaskPollInterval <= 0 {
		return fmt.Errorf("gateway.a2a.task_poll_interval must be > 0")
	}
	if cfg.A2A.StreamIdleTimeout <= 0 {
		return fmt.Errorf("gateway.a2a.stream_idle_timeout must be > 0")
	}
	if cfg.A2A.CardRefreshInterval <= 0 {
		return fmt.Errorf("gateway.a2a.card_refresh_interval must be > 0")
	}
	if cfg.A2A.ResumeScanInterval <= 0 {
		return fmt.Errorf("gateway.a2a.resume_scan_interval must be > 0")
	}
	if strings.TrimSpace(cfg.Cron.DefaultTimezone) == "" {
		cfg.Cron.DefaultTimezone = "UTC"
	}
	if _, err := time.LoadLocation(strings.TrimSpace(cfg.Cron.DefaultTimezone)); err != nil {
		return fmt.Errorf("gateway.cron.default_timezone is invalid: %w", err)
	}
	if strings.TrimSpace(cfg.NATSURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(cfg.NATSURL))
		if err != nil || parsed.Scheme == "" {
			return fmt.Errorf("gateway.nats.url is invalid: %q", cfg.NATSURL)
		}
	}
	for _, capability := range cfg.Capabilities {
		if strings.TrimSpace(capability.Name) == "" {
			return fmt.Errorf("gateway capability name is required")
		}
	}
	cfg.Telegram.Mode = strings.ToLower(strings.TrimSpace(cfg.Telegram.Mode))
	if cfg.Telegram.Mode == "" {
		cfg.Telegram.Mode = "polling"
	}
	if cfg.Telegram.Mode != "polling" && cfg.Telegram.Mode != "webhook" {
		return fmt.Errorf("gateway.telegram.mode must be either polling or webhook")
	}
	if cfg.Telegram.Enabled {
		if strings.TrimSpace(cfg.Telegram.BotToken) == "" {
			return fmt.Errorf("gateway.telegram.bot_token is required when telegram is enabled")
		}
		switch cfg.Telegram.Mode {
		case "polling":
			if cfg.Telegram.PollInterval <= 0 {
				return fmt.Errorf("gateway.telegram.poll_interval must be > 0 when telegram is enabled in polling mode")
			}
			if cfg.Telegram.PollTimeout <= 0 {
				return fmt.Errorf("gateway.telegram.poll_timeout must be > 0 when telegram is enabled in polling mode")
			}
		case "webhook":
			if err := validateLoopbackListenAddr(strings.TrimSpace(cfg.Telegram.Webhook.ListenAddr), "gateway.telegram.webhook.listen_addr"); err != nil {
				return err
			}
			path := strings.TrimSpace(cfg.Telegram.Webhook.Path)
			if path == "" {
				return fmt.Errorf("gateway.telegram.webhook.path is required when telegram webhook mode is enabled")
			}
			if !strings.HasPrefix(path, "/") {
				return fmt.Errorf("gateway.telegram.webhook.path must start with /")
			}
			if strings.TrimSpace(cfg.Telegram.Webhook.Secret) == "" {
				return fmt.Errorf("gateway.telegram.webhook.secret is required when telegram webhook mode is enabled")
			}
			webhookURL := strings.TrimSpace(cfg.Telegram.Webhook.URL)
			if webhookURL == "" {
				return fmt.Errorf("gateway.telegram.webhook.url is required when telegram webhook mode is enabled")
			}
			parsedWebhookURL, err := url.Parse(webhookURL)
			if err != nil || strings.TrimSpace(parsedWebhookURL.Scheme) == "" || strings.TrimSpace(parsedWebhookURL.Host) == "" {
				return fmt.Errorf("gateway.telegram.webhook.url is invalid: %q", cfg.Telegram.Webhook.URL)
			}
			if !strings.EqualFold(strings.TrimSpace(parsedWebhookURL.Scheme), "https") {
				return fmt.Errorf("gateway.telegram.webhook.url must use https")
			}
		}
	}
	if err := validateLoopbackListenAddr(strings.TrimSpace(cfg.Panel.ListenAddr), "gateway.panel.listen_addr"); err != nil {
		return err
	}
	if cfg.Update.Enabled {
		if strings.TrimSpace(cfg.Update.RepoOwner) == "" {
			return fmt.Errorf("gateway.update.repo_owner is required when update is enabled")
		}
		if strings.TrimSpace(cfg.Update.RepoName) == "" {
			return fmt.Errorf("gateway.update.repo_name is required when update is enabled")
		}
	}
	remoteIDs := map[string]struct{}{}
	for i := range cfg.A2A.Remotes {
		remote := &cfg.A2A.Remotes[i]
		if remote.RequestTimeout <= 0 {
			remote.RequestTimeout = cfg.A2A.RequestTimeout
		}
		if err := validateA2ARemoteConfig(remote); err != nil {
			return err
		}
		if _, exists := remoteIDs[remote.ID]; exists {
			return fmt.Errorf("gateway.a2a.remotes[%s] is duplicated", remote.ID)
		}
		remoteIDs[remote.ID] = struct{}{}
	}
	return nil
}

func validateA2ARemoteConfig(remote *A2ARemoteConfig) error {
	if remote == nil {
		return fmt.Errorf("gateway.a2a.remote is required")
	}
	id := strings.TrimSpace(remote.ID)
	if id == "" {
		return fmt.Errorf("gateway.a2a.remote id is required")
	}
	if strings.Contains(id, ":") {
		return fmt.Errorf("gateway.a2a.remote id %q must not contain ':'", id)
	}
	baseURL := strings.TrimSpace(remote.BaseURL)
	cardURL := strings.TrimSpace(remote.CardURL)
	if baseURL == "" && cardURL == "" {
		return fmt.Errorf("gateway.a2a.remotes[%s] requires base_url or card_url", id)
	}
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err != nil || strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Host) == "" {
			return fmt.Errorf("gateway.a2a.remotes[%s].base_url is invalid", id)
		}
	}
	if cardURL != "" {
		parsed, err := url.Parse(cardURL)
		if err != nil || strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Host) == "" {
			return fmt.Errorf("gateway.a2a.remotes[%s].card_url is invalid", id)
		}
	}
	if remote.RequestTimeout <= 0 {
		return fmt.Errorf("gateway.a2a.remotes[%s].request_timeout must be > 0", id)
	}
	return nil
}

func expandGatewayA2AEnvTemplates(cfg *GatewayConfig, env map[string]string) error {
	if cfg == nil {
		return nil
	}
	for i := range cfg.A2A.Remotes {
		remote := &cfg.A2A.Remotes[i]
		if len(remote.Headers) == 0 {
			continue
		}
		expanded := make(map[string]string, len(remote.Headers))
		for key, value := range remote.Headers {
			resolved, err := expandGatewayEnvTemplate(value, env)
			if err != nil {
				return fmt.Errorf("gateway.a2a.remotes[%s].headers[%s]: %w", remote.ID, key, err)
			}
			expanded[strings.TrimSpace(key)] = resolved
		}
		remote.Headers = expanded
	}
	return nil
}

func expandGatewayEnvTemplate(value string, env map[string]string) (string, error) {
	out := value
	for {
		start := strings.Index(out, "${")
		if start < 0 {
			return out, nil
		}
		end := strings.Index(out[start:], "}")
		if end < 0 {
			return "", fmt.Errorf("unterminated environment placeholder")
		}
		end += start
		key := strings.TrimSpace(out[start+2 : end])
		if key == "" {
			return "", fmt.Errorf("empty environment placeholder")
		}
		envValue, ok := env[key]
		if !ok {
			return "", fmt.Errorf("missing environment variable %q", key)
		}
		out = out[:start] + envValue + out[end+1:]
	}
}

func validateLoopbackListenAddr(value string, fieldName string) error {
	if value == "" {
		return fmt.Errorf("%s is required", fieldName)
	}
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("%s is invalid: %w", fieldName, err)
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("%s host is required", fieldName)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s must bind to loopback only (localhost/127.0.0.1/::1)", fieldName)
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func currentEnvMap() map[string]string {
	values := make(map[string]string, len(os.Environ()))
	for _, pair := range os.Environ() {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[parts[0]] = parts[1]
	}
	return values
}

func hasGatewayEnv(env map[string]string) bool {
	for key := range env {
		if strings.HasPrefix(key, "GOPHER_") {
			return true
		}
	}
	return false
}

func hasGatewayOverrides(overrides GatewayOverrides) bool {
	return overrides.NodeID != nil ||
		overrides.GatewayNodeID != nil ||
		overrides.NATSURL != nil ||
		overrides.HeartbeatInterval != nil ||
		overrides.PruneInterval != nil ||
		overrides.Capabilities != nil ||
		overrides.TelegramEnabled != nil ||
		overrides.TelegramBotToken != nil ||
		overrides.TelegramPollInterval != nil ||
		overrides.TelegramPollTimeout != nil ||
		overrides.TelegramAllowedUserID != nil ||
		overrides.TelegramAllowedChatID != nil ||
		overrides.TelegramMode != nil ||
		overrides.TelegramWebhookListen != nil ||
		overrides.TelegramWebhookPath != nil ||
		overrides.TelegramWebhookURL != nil ||
		overrides.TelegramWebhookSecret != nil ||
		overrides.PanelListenAddr != nil ||
		overrides.PanelCaptureThinking != nil ||
		overrides.CronEnabled != nil ||
		overrides.CronPollInterval != nil ||
		overrides.CronTimezone != nil ||
		overrides.UpdateEnabled != nil ||
		overrides.UpdateRepoOwner != nil ||
		overrides.UpdateRepoName != nil ||
		overrides.UpdateChannel != nil ||
		overrides.UpdateCheckInterval != nil ||
		overrides.UpdateAssetPattern != nil
}
