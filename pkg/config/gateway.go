package config

import (
	"bytes"
	"fmt"
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
	DefaultGatewayNodeID      = "gateway"
	DefaultHeartbeatInterval  = 2 * time.Second
	DefaultPruneInterval      = 3 * time.Second
	DefaultCronPollInterval   = 1 * time.Second
	DefaultClientConnectWait  = 5 * time.Second
	DefaultClientReconnectGap = 2 * time.Second
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
	Matrix            MatrixConfig
	Cron              CronConfig
	Update            UpdateConfig
	PrimaryConfigPath string
	LocalConfigPath   string
}

type MatrixConfig struct {
	Enabled           bool
	HomeserverURL     string
	AppserviceID      string
	ASToken           string
	HSToken           string
	ListenAddr        string
	BotUserID         string
	RichTextEnabled   bool
	PresenceEnabled   bool
	PresenceInterval  time.Duration
	PresenceStatusMsg string
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

type GatewayOverrides struct {
	NodeID                  *string
	GatewayNodeID           *string
	NATSURL                 *string
	HeartbeatInterval       *time.Duration
	PruneInterval           *time.Duration
	Capabilities            *[]scheduler.Capability
	MatrixEnabled           *bool
	MatrixHomeserver        *string
	MatrixAppservice        *string
	MatrixASToken           *string
	MatrixHSToken           *string
	MatrixListenAddr        *string
	MatrixBotUserID         *string
	MatrixRichTextEnabled   *bool
	MatrixPresenceEnabled   *bool
	MatrixPresenceInterval  *time.Duration
	MatrixPresenceStatusMsg *string
	CronEnabled             *bool
	CronPollInterval        *time.Duration
	CronTimezone            *string
	UpdateEnabled           *bool
	UpdateRepoOwner         *string
	UpdateRepoName          *string
	UpdateChannel           *string
	UpdateCheckInterval     *time.Duration
	UpdateAssetPattern      *string
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
	Matrix       *rawMatrixConfig    `toml:"matrix"`
	Cron         *rawCronConfig      `toml:"cron"`
	Update       *rawUpdateConfig    `toml:"update"`
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

type rawMatrixConfig struct {
	Enabled           *bool   `toml:"enabled"`
	HomeserverURL     *string `toml:"homeserver_url"`
	AppserviceID      *string `toml:"appservice_id"`
	ASToken           *string `toml:"as_token"`
	HSToken           *string `toml:"hs_token"`
	ListenAddr        *string `toml:"listen_addr"`
	BotUserID         *string `toml:"bot_user_id"`
	RichTextEnabled   *bool   `toml:"rich_text_enabled"`
	PresenceEnabled   *bool   `toml:"presence_enabled"`
	PresenceInterval  *string `toml:"presence_interval"`
	PresenceStatusMsg *string `toml:"presence_status_msg"`
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

func LoadGatewayConfig(opts GatewayLoadOptions) (GatewayConfig, []string, error) {
	cfg := defaultGatewayConfig()
	sources := []string{"defaults"}

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

	primaryPath, localPath, err := resolveConfigPaths(workingDir, strings.TrimSpace(opts.ConfigPath))
	if err != nil {
		return GatewayConfig{}, nil, err
	}

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
	}

	if err := applyGatewayOverrides(&cfg, opts.Overrides); err != nil {
		return GatewayConfig{}, nil, err
	}
	if hasGatewayOverrides(opts.Overrides) {
		sources = append(sources, "cli-flags")
	}

	if err := validateGatewayConfig(&cfg); err != nil {
		return GatewayConfig{}, nil, err
	}
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

[gateway.matrix]
enabled = false
homeserver_url = "http://127.0.0.1:8008"
appservice_id = "gopher"
as_token = "replace-as-token"
hs_token = "replace-hs-token"
listen_addr = "127.0.0.1:29328"
bot_user_id = "@gopher:localhost"
rich_text_enabled = true
presence_enabled = true
presence_interval = "60s"
presence_status_msg = ""

[gateway.cron]
enabled = false
poll_interval = "1s"
default_timezone = "UTC"

[gateway.update]
enabled = false
repo_owner = "replace-owner"
repo_name = "replace-private-repo"
channel = "stable"
check_interval = "1h"
binary_asset_pattern = "linux"
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
		Matrix: MatrixConfig{
			Enabled:           false,
			HomeserverURL:     "",
			AppserviceID:      "gopher",
			ASToken:           "",
			HSToken:           "",
			ListenAddr:        "127.0.0.1:29328",
			BotUserID:         "",
			RichTextEnabled:   true,
			PresenceEnabled:   true,
			PresenceInterval:  60 * time.Second,
			PresenceStatusMsg: "",
		},
		Cron: CronConfig{
			Enabled:         false,
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
	if gateway.Matrix != nil {
		if gateway.Matrix.Enabled != nil {
			cfg.Matrix.Enabled = *gateway.Matrix.Enabled
		}
		if gateway.Matrix.HomeserverURL != nil {
			cfg.Matrix.HomeserverURL = strings.TrimSpace(*gateway.Matrix.HomeserverURL)
		}
		if gateway.Matrix.AppserviceID != nil {
			cfg.Matrix.AppserviceID = strings.TrimSpace(*gateway.Matrix.AppserviceID)
		}
		if gateway.Matrix.ASToken != nil {
			cfg.Matrix.ASToken = strings.TrimSpace(*gateway.Matrix.ASToken)
		}
		if gateway.Matrix.HSToken != nil {
			cfg.Matrix.HSToken = strings.TrimSpace(*gateway.Matrix.HSToken)
		}
		if gateway.Matrix.ListenAddr != nil {
			cfg.Matrix.ListenAddr = strings.TrimSpace(*gateway.Matrix.ListenAddr)
		}
		if gateway.Matrix.BotUserID != nil {
			cfg.Matrix.BotUserID = strings.TrimSpace(*gateway.Matrix.BotUserID)
		}
		if gateway.Matrix.RichTextEnabled != nil {
			cfg.Matrix.RichTextEnabled = *gateway.Matrix.RichTextEnabled
		}
		if gateway.Matrix.PresenceEnabled != nil {
			cfg.Matrix.PresenceEnabled = *gateway.Matrix.PresenceEnabled
		}
		if gateway.Matrix.PresenceInterval != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*gateway.Matrix.PresenceInterval))
			if err != nil {
				return fmt.Errorf("invalid gateway.matrix.presence_interval: %w", err)
			}
			cfg.Matrix.PresenceInterval = duration
		}
		if gateway.Matrix.PresenceStatusMsg != nil {
			cfg.Matrix.PresenceStatusMsg = strings.TrimSpace(*gateway.Matrix.PresenceStatusMsg)
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
	if value, ok := env["GOPHER_GATEWAY_MATRIX_ENABLED"]; ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_MATRIX_ENABLED: %w", err)
		}
		cfg.Matrix.Enabled = enabled
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_HOMESERVER_URL"]; ok {
		cfg.Matrix.HomeserverURL = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_APPSERVICE_ID"]; ok {
		cfg.Matrix.AppserviceID = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_AS_TOKEN"]; ok {
		cfg.Matrix.ASToken = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_HS_TOKEN"]; ok {
		cfg.Matrix.HSToken = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_LISTEN_ADDR"]; ok {
		cfg.Matrix.ListenAddr = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_BOT_USER_ID"]; ok {
		cfg.Matrix.BotUserID = strings.TrimSpace(value)
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_RICH_TEXT_ENABLED"]; ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_MATRIX_RICH_TEXT_ENABLED: %w", err)
		}
		cfg.Matrix.RichTextEnabled = enabled
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_PRESENCE_ENABLED"]; ok {
		enabled, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_MATRIX_PRESENCE_ENABLED: %w", err)
		}
		cfg.Matrix.PresenceEnabled = enabled
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_PRESENCE_INTERVAL"]; ok {
		duration, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid GOPHER_GATEWAY_MATRIX_PRESENCE_INTERVAL: %w", err)
		}
		cfg.Matrix.PresenceInterval = duration
	}
	if value, ok := env["GOPHER_GATEWAY_MATRIX_PRESENCE_STATUS_MSG"]; ok {
		cfg.Matrix.PresenceStatusMsg = strings.TrimSpace(value)
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
	if overrides.MatrixEnabled != nil {
		cfg.Matrix.Enabled = *overrides.MatrixEnabled
	}
	if overrides.MatrixHomeserver != nil {
		cfg.Matrix.HomeserverURL = strings.TrimSpace(*overrides.MatrixHomeserver)
	}
	if overrides.MatrixAppservice != nil {
		cfg.Matrix.AppserviceID = strings.TrimSpace(*overrides.MatrixAppservice)
	}
	if overrides.MatrixASToken != nil {
		cfg.Matrix.ASToken = strings.TrimSpace(*overrides.MatrixASToken)
	}
	if overrides.MatrixHSToken != nil {
		cfg.Matrix.HSToken = strings.TrimSpace(*overrides.MatrixHSToken)
	}
	if overrides.MatrixListenAddr != nil {
		cfg.Matrix.ListenAddr = strings.TrimSpace(*overrides.MatrixListenAddr)
	}
	if overrides.MatrixBotUserID != nil {
		cfg.Matrix.BotUserID = strings.TrimSpace(*overrides.MatrixBotUserID)
	}
	if overrides.MatrixRichTextEnabled != nil {
		cfg.Matrix.RichTextEnabled = *overrides.MatrixRichTextEnabled
	}
	if overrides.MatrixPresenceEnabled != nil {
		cfg.Matrix.PresenceEnabled = *overrides.MatrixPresenceEnabled
	}
	if overrides.MatrixPresenceInterval != nil {
		cfg.Matrix.PresenceInterval = *overrides.MatrixPresenceInterval
	}
	if overrides.MatrixPresenceStatusMsg != nil {
		cfg.Matrix.PresenceStatusMsg = strings.TrimSpace(*overrides.MatrixPresenceStatusMsg)
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
	if cfg.Matrix.Enabled {
		if strings.TrimSpace(cfg.Matrix.HomeserverURL) == "" {
			return fmt.Errorf("gateway.matrix.homeserver_url is required when matrix is enabled")
		}
		if _, err := url.Parse(strings.TrimSpace(cfg.Matrix.HomeserverURL)); err != nil {
			return fmt.Errorf("gateway.matrix.homeserver_url is invalid: %w", err)
		}
		if strings.TrimSpace(cfg.Matrix.AppserviceID) == "" {
			return fmt.Errorf("gateway.matrix.appservice_id is required when matrix is enabled")
		}
		if strings.TrimSpace(cfg.Matrix.ASToken) == "" {
			return fmt.Errorf("gateway.matrix.as_token is required when matrix is enabled")
		}
		if strings.TrimSpace(cfg.Matrix.HSToken) == "" {
			return fmt.Errorf("gateway.matrix.hs_token is required when matrix is enabled")
		}
		if strings.TrimSpace(cfg.Matrix.ListenAddr) == "" {
			return fmt.Errorf("gateway.matrix.listen_addr is required when matrix is enabled")
		}
		if cfg.Matrix.PresenceEnabled && cfg.Matrix.PresenceInterval <= 0 {
			return fmt.Errorf("gateway.matrix.presence_interval must be > 0 when matrix presence is enabled")
		}
	}
	if cfg.Update.Enabled {
		if strings.TrimSpace(cfg.Update.RepoOwner) == "" {
			return fmt.Errorf("gateway.update.repo_owner is required when update is enabled")
		}
		if strings.TrimSpace(cfg.Update.RepoName) == "" {
			return fmt.Errorf("gateway.update.repo_name is required when update is enabled")
		}
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
		overrides.MatrixEnabled != nil ||
		overrides.MatrixHomeserver != nil ||
		overrides.MatrixAppservice != nil ||
		overrides.MatrixASToken != nil ||
		overrides.MatrixHSToken != nil ||
		overrides.MatrixListenAddr != nil ||
		overrides.MatrixBotUserID != nil ||
		overrides.MatrixRichTextEnabled != nil ||
		overrides.MatrixPresenceEnabled != nil ||
		overrides.MatrixPresenceInterval != nil ||
		overrides.MatrixPresenceStatusMsg != nil ||
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
