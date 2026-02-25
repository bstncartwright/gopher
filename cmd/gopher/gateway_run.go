package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/bstncartwright/gopher/pkg/config"
	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/gateway"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/panel"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type gatewayRunInputs struct {
	ConfigPath string
	Overrides  config.GatewayOverrides
}

type capabilityFlag struct {
	values []string
}

func (c *capabilityFlag) String() string {
	return strings.Join(c.values, ",")
}

func (c *capabilityFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("capability cannot be empty")
	}
	c.values = append(c.values, value)
	return nil
}

type boolOverrideFlag struct {
	set   bool
	value bool
}

func (f *boolOverrideFlag) String() string {
	if !f.set {
		return ""
	}
	if f.value {
		return "true"
	}
	return "false"
}

func (f *boolOverrideFlag) Set(value string) error {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return err
	}
	f.set = true
	f.value = parsed
	return nil
}

type gatewayProcess struct {
	registry  *scheduler.Registry
	scheduler *scheduler.Scheduler
	syncer    *gateway.NodeRegistrySync
	updater   *gateway.NodeUpdateCoordinator
	runtime   *node.Runtime
	executor  sessionrt.AgentExecutor
}

type panelRuntime interface {
	RunWithRetry(ctx context.Context) error
}

var newGatewayPanel = func(opts panel.ServerOptions) (panelRuntime, error) {
	return panel.NewServer(opts)
}

func runGatewaySubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printGatewayUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "run":
		if wantsHelp(args[1:]) {
			printGatewayUsage(stdout)
			return nil
		}
		inputs, err := parseGatewayRunFlags(args[1:])
		if err != nil {
			return err
		}
		workingDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		cfg, sources, err := config.LoadGatewayConfig(config.GatewayLoadOptions{
			WorkingDir: workingDir,
			ConfigPath: inputs.ConfigPath,
			Overrides:  inputs.Overrides,
		})
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runGatewayWithContext(ctx, cfg, sources, stderr)
	case "config":
		return runGatewayConfigSubcommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printGatewayUsage(stdout)
		return nil
	default:
		printGatewayUsage(stderr)
		return fmt.Errorf("unknown gateway command %q", args[0])
	}
}

func printGatewayUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher gateway run [flags]")
	fmt.Fprintln(out, "  gopher gateway config init [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "run flags:")
	fmt.Fprintln(out, "  --config <path>                path to toml config (default: ./gopher.toml)")
	fmt.Fprintln(out, "  --node-id <id>                 override gateway node id")
	fmt.Fprintln(out, "  --gateway-id <id>              override scheduler gateway id")
	fmt.Fprintln(out, "  --nats-url <url>               override nats server url")
	fmt.Fprintln(out, "  --heartbeat-interval <dur>     override heartbeat interval")
	fmt.Fprintln(out, "  --prune-interval <dur>         override prune interval")
	fmt.Fprintln(out, "  --capability <kind:name>       repeatable capability override")
	fmt.Fprintln(out, "  --telegram-enabled <bool>      override telegram transport enablement")
	fmt.Fprintln(out, "  --telegram-bot-token <token>   override telegram bot token")
	fmt.Fprintln(out, "  --telegram-poll-interval <dur> override telegram poll loop interval")
	fmt.Fprintln(out, "  --telegram-poll-timeout <dur>  override telegram getUpdates timeout")
	fmt.Fprintln(out, "  --telegram-allowed-user-id <id> override authorized telegram user id")
	fmt.Fprintln(out, "  --telegram-allowed-chat-id <id> override authorized telegram chat id")
	fmt.Fprintln(out, "  --panel-listen-addr <addr>     override observability panel listen address")
	fmt.Fprintln(out, "  --panel-capture-thinking <bool> override panel thinking capture")
	fmt.Fprintln(out, "  --cron-enabled <bool>          override cron subsystem enablement")
	fmt.Fprintln(out, "  --cron-poll-interval <dur>     override cron polling interval")
	fmt.Fprintln(out, "  --cron-default-timezone <tz>   override default cron timezone")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "config init flags:")
	fmt.Fprintln(out, "  --path <path>                  output path (default: ./gopher.toml)")
	fmt.Fprintln(out, "  --force                        overwrite if file exists")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "example:")
	fmt.Fprintln(out, "  gopher gateway config init")
	fmt.Fprintln(out, "  gopher gateway run --config ./gopher.toml --capability tool:gpu")
}

func parseGatewayRunFlags(args []string) (gatewayRunInputs, error) {
	flags := flag.NewFlagSet("gateway run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var rawCaps capabilityFlag
	var telegramEnabled boolOverrideFlag
	var panelCaptureThinking boolOverrideFlag
	var cronEnabled boolOverrideFlag
	configPath := flags.String("config", "", "config path")
	nodeID := flags.String("node-id", "", "gateway node id override")
	gatewayID := flags.String("gateway-id", "", "gateway id override")
	natsURL := flags.String("nats-url", "", "nats url override")
	heartbeat := flags.Duration("heartbeat-interval", 0, "heartbeat interval override")
	prune := flags.Duration("prune-interval", 0, "prune interval override")
	flags.Var(&rawCaps, "capability", "repeatable capability kind:name")
	flags.Var(&telegramEnabled, "telegram-enabled", "telegram enabled override")
	telegramBotToken := flags.String("telegram-bot-token", "", "telegram bot token override")
	telegramPollInterval := flags.Duration("telegram-poll-interval", 0, "telegram poll interval override")
	telegramPollTimeout := flags.Duration("telegram-poll-timeout", 0, "telegram poll timeout override")
	telegramAllowedUserID := flags.String("telegram-allowed-user-id", "", "telegram allowed user id override")
	telegramAllowedChatID := flags.String("telegram-allowed-chat-id", "", "telegram allowed chat id override")
	panelListenAddr := flags.String("panel-listen-addr", "", "panel listen addr override")
	flags.Var(&panelCaptureThinking, "panel-capture-thinking", "panel capture thinking override")
	flags.Var(&cronEnabled, "cron-enabled", "cron enabled override")
	cronPollInterval := flags.Duration("cron-poll-interval", 0, "cron poll interval override")
	cronDefaultTimezone := flags.String("cron-default-timezone", "", "cron default timezone override")

	if err := flags.Parse(args); err != nil {
		return gatewayRunInputs{}, err
	}
	if len(flags.Args()) > 0 {
		return gatewayRunInputs{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	inputs := gatewayRunInputs{
		ConfigPath: strings.TrimSpace(*configPath),
		Overrides:  config.GatewayOverrides{},
	}
	if strings.TrimSpace(*nodeID) != "" {
		value := strings.TrimSpace(*nodeID)
		inputs.Overrides.NodeID = &value
	}
	if strings.TrimSpace(*gatewayID) != "" {
		value := strings.TrimSpace(*gatewayID)
		inputs.Overrides.GatewayNodeID = &value
	}
	if strings.TrimSpace(*natsURL) != "" {
		value := strings.TrimSpace(*natsURL)
		inputs.Overrides.NATSURL = &value
	}
	if *heartbeat != 0 {
		value := *heartbeat
		inputs.Overrides.HeartbeatInterval = &value
	}
	if *prune != 0 {
		value := *prune
		inputs.Overrides.PruneInterval = &value
	}
	if len(rawCaps.values) > 0 {
		caps := make([]scheduler.Capability, 0, len(rawCaps.values))
		for _, raw := range rawCaps.values {
			capability, err := config.ParseCapability(raw)
			if err != nil {
				return gatewayRunInputs{}, err
			}
			caps = append(caps, capability)
		}
		inputs.Overrides.Capabilities = &caps
	}
	if telegramEnabled.set {
		value := telegramEnabled.value
		inputs.Overrides.TelegramEnabled = &value
	}
	if strings.TrimSpace(*telegramBotToken) != "" {
		value := strings.TrimSpace(*telegramBotToken)
		inputs.Overrides.TelegramBotToken = &value
	}
	if *telegramPollInterval != 0 {
		value := *telegramPollInterval
		inputs.Overrides.TelegramPollInterval = &value
	}
	if *telegramPollTimeout != 0 {
		value := *telegramPollTimeout
		inputs.Overrides.TelegramPollTimeout = &value
	}
	if strings.TrimSpace(*telegramAllowedUserID) != "" {
		value := strings.TrimSpace(*telegramAllowedUserID)
		inputs.Overrides.TelegramAllowedUserID = &value
	}
	if strings.TrimSpace(*telegramAllowedChatID) != "" {
		value := strings.TrimSpace(*telegramAllowedChatID)
		inputs.Overrides.TelegramAllowedChatID = &value
	}
	if strings.TrimSpace(*panelListenAddr) != "" {
		value := strings.TrimSpace(*panelListenAddr)
		inputs.Overrides.PanelListenAddr = &value
	}
	if panelCaptureThinking.set {
		value := panelCaptureThinking.value
		inputs.Overrides.PanelCaptureThinking = &value
	}
	if cronEnabled.set {
		value := cronEnabled.value
		inputs.Overrides.CronEnabled = &value
	}
	if *cronPollInterval != 0 {
		value := *cronPollInterval
		inputs.Overrides.CronPollInterval = &value
	}
	if strings.TrimSpace(*cronDefaultTimezone) != "" {
		value := strings.TrimSpace(*cronDefaultTimezone)
		inputs.Overrides.CronTimezone = &value
	}
	return inputs, nil
}

func wantsHelp(args []string) bool {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

func runGatewayWithContext(ctx context.Context, cfg config.GatewayConfig, sources []string, stderr io.Writer) error {
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve workspace directory: %w", err)
	}
	logger, cleanupLogs, err := setupProcessLogging(workspace, "gateway", stderr)
	if err != nil {
		return err
	}
	defer cleanupLogs()

	slog.Info("gateway_run: starting gateway", "node_id", cfg.NodeID, "gateway_id", cfg.GatewayNodeID)
	client, err := fabricts.NewClient(fabricts.ClientOptions{
		URL:            cfg.NATSURL,
		Name:           "gopher-gateway-" + cfg.NodeID,
		ConnectTimeout: cfg.ConnectTimeout,
		ReconnectWait:  cfg.ReconnectWait,
	})
	if err != nil {
		slog.Error("gateway_run: failed to create nats client", "error", err)
		return fmt.Errorf("create nats client: %w", err)
	}
	defer client.Close()
	slog.Debug("gateway_run: nats client created", "url", cfg.NATSURL)

	agentRuntime, err := loadGatewayAgentRuntimeWithOptions(workspace, agentRuntimeOptions{
		CaptureDeltas:   true,
		CaptureThinking: cfg.Panel.CaptureThinking,
	})
	if err != nil {
		slog.Error("gateway_run: failed to load agent runtime", "workspace", workspace, "error", err)
		return err
	}
	slog.Info("gateway_run: agent runtime loaded", "agents_count", len(agentRuntime.Agents), "default_agent_id", agentRuntime.DefaultActorID)
	capabilityResolver, err := buildRequiredCapabilityResolver(agentRuntime)
	if err != nil {
		slog.Error("gateway_run: failed to build capability resolver", "error", err)
		return err
	}

	process, err := startGatewayProcessWithCapabilityResolver(ctx, cfg, client, agentRuntime.Executor, capabilityResolver, logger)
	if err != nil {
		slog.Error("gateway_run: failed to start gateway process", "error", err)
		return err
	}
	defer process.Stop()

	dataDir := resolveGatewayDataDir(workspace)
	var telegramBridge *telegramDMBridge
	if cfg.Telegram.Enabled {
		slog.Info("gateway_run: telegram enabled, starting dm bridge")
		telegramBridge, err = startTelegramDMBridgeWithRuntime(ctx, cfg, workspace, agentRuntime, process.executor, logger)
		if err != nil {
			slog.Error("gateway_run: failed to start telegram dm bridge", "error", err)
			return err
		}
		defer telegramBridge.Stop()
		newControlActionApplier(telegramBridge.manager, dataDir, logger).Start(ctx)
		newControlSessionWatcher(telegramBridge.store, dataDir, logger).Start(ctx)
	} else {
		slog.Info("gateway_run: telegram disabled")
	}

	var panelStore panel.SessionStore
	var panelSessionMetadata panel.SessionMetadataResolver
	if telegramBridge != nil && telegramBridge.store != nil {
		panelStore = telegramBridge.store
	}
	if telegramBridge != nil && telegramBridge.bindings != nil {
		bindings := telegramBridge.bindings
		panelSessionMetadata = func(sessionID sessionrt.SessionID) (panel.SessionMetadata, bool) {
			binding, ok := bindings.GetBySession(sessionID)
			if !ok {
				return panel.SessionMetadata{}, false
			}
			return panel.SessionMetadata{
				ConversationID:   binding.ConversationID,
				ConversationName: binding.ConversationName,
			}, true
		}
	}
	if err := startGatewayPanel(ctx, cfg, process, panelStore, panelSessionMetadata, dataDir, logger); err != nil {
		slog.Error("gateway_run: failed to start panel server", "error", err)
		return err
	}

	slog.Info("gateway_run: gateway running",
		"node_id", cfg.NodeID,
		"gateway_id", cfg.GatewayNodeID,
		"nats_url", cfg.NATSURL,
		"heartbeat_interval", cfg.HeartbeatInterval.String(),
		"prune_interval", cfg.PruneInterval.String(),
		"capabilities", mustJSON(cfg.Capabilities),
		"config_sources", strings.Join(sources, ","),
	)
	<-ctx.Done()
	slog.Info("gateway_run: gateway shutting down", "reason", ctx.Err())
	return nil
}

func startGatewayProcessWithCapabilityResolver(
	ctx context.Context,
	cfg config.GatewayConfig,
	fabric fabricts.Fabric,
	executor sessionrt.AgentExecutor,
	resolver gateway.CapabilityResolver,
	logger *log.Logger,
) (*gatewayProcess, error) {
	registry := scheduler.NewRegistry(0)
	registry.Upsert(scheduler.NodeInfo{
		NodeID:       cfg.NodeID,
		IsGateway:    true,
		Version:      currentBinaryVersion(),
		Capabilities: cfg.Capabilities,
	})
	schedulerInstance := scheduler.NewScheduler(cfg.GatewayNodeID, registry)

	syncer, err := gateway.NewNodeRegistrySync(gateway.NodeRegistrySyncOptions{
		Fabric:        fabric,
		Registry:      registry,
		PruneInterval: cfg.PruneInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("create node registry sync: %w", err)
	}
	if err := syncer.Start(ctx); err != nil {
		return nil, fmt.Errorf("start node registry sync: %w", err)
	}
	updater, err := gateway.NewNodeUpdateCoordinator(gateway.NodeUpdateCoordinatorOptions{
		Fabric:         fabric,
		GatewayNodeID:  cfg.NodeID,
		GatewayVersion: currentBinaryVersion(),
	})
	if err != nil {
		syncer.Stop()
		return nil, fmt.Errorf("create node update coordinator: %w", err)
	}
	if err := updater.Start(ctx); err != nil {
		syncer.Stop()
		return nil, fmt.Errorf("start node update coordinator: %w", err)
	}

	distributedExecutor, err := gateway.NewDistributedExecutor(gateway.DistributedExecutorOptions{
		GatewayNodeID:      cfg.GatewayNodeID,
		LocalExecutor:      executor,
		Scheduler:          schedulerInstance,
		Fabric:             fabric,
		CapabilityResolver: resolver,
	})
	if err != nil {
		updater.Stop()
		syncer.Stop()
		return nil, fmt.Errorf("create distributed executor: %w", err)
	}

	runtime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            cfg.NodeID,
		IsGateway:         true,
		Version:           currentBinaryVersion(),
		Capabilities:      cfg.Capabilities,
		Fabric:            fabric,
		Executor:          distributedExecutor,
		HeartbeatInterval: cfg.HeartbeatInterval,
	})
	if err != nil {
		updater.Stop()
		syncer.Stop()
		return nil, fmt.Errorf("create gateway runtime: %w", err)
	}
	if err := runtime.Start(ctx); err != nil {
		updater.Stop()
		syncer.Stop()
		return nil, fmt.Errorf("start gateway runtime: %w", err)
	}

	if logger != nil {
		logger.Printf("gateway components started node_id=%s", cfg.NodeID)
	}
	return &gatewayProcess{
		registry:  registry,
		scheduler: schedulerInstance,
		syncer:    syncer,
		updater:   updater,
		runtime:   runtime,
		executor:  distributedExecutor,
	}, nil
}

func runGatewayConfigSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printGatewayUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "init":
		flags := flag.NewFlagSet("gateway config init", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		path := flags.String("path", "gopher.toml", "output path")
		force := flags.Bool("force", false, "overwrite existing file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if len(flags.Args()) > 0 {
			return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
		}
		target := strings.TrimSpace(*path)
		if target == "" {
			return fmt.Errorf("path is required")
		}
		if !filepath.IsAbs(target) {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve working directory: %w", err)
			}
			target = filepath.Join(cwd, target)
		}
		if _, err := os.Stat(target); err == nil && !*force {
			return fmt.Errorf("file already exists: %s (use --force to overwrite)", target)
		}
		if err := writeConfigFileWithBackup(target, []byte(config.DefaultGatewayTOML())); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", target)
		return nil
	case "help", "-h", "--help":
		printGatewayUsage(stdout)
		return nil
	default:
		printGatewayUsage(stderr)
		return fmt.Errorf("unknown gateway config command %q", args[0])
	}
}

func startGatewayPanel(
	ctx context.Context,
	cfg config.GatewayConfig,
	process *gatewayProcess,
	store panel.SessionStore,
	sessionMetadata panel.SessionMetadataResolver,
	controlDir string,
	logger *log.Logger,
) error {
	if process == nil || process.registry == nil {
		slog.Error("gateway_run: gateway process is not initialized for panel")
		return fmt.Errorf("create observability panel server: gateway process is not initialized")
	}
	panelServer, err := newGatewayPanel(panel.ServerOptions{
		ListenAddr:      cfg.Panel.ListenAddr,
		Store:           store,
		SessionMetadata: sessionMetadata,
		ControlDir:      filepath.Join(controlDir, "control"),
		NodeSnapshot: func() []scheduler.NodeInfo {
			return process.registry.Snapshot()
		},
	})
	if err != nil {
		return fmt.Errorf("create observability panel server: %w", err)
	}
	go func() {
		if runErr := panelServer.RunWithRetry(ctx); runErr != nil && ctx.Err() == nil && logger != nil {
			logger.Printf("panel server stopped err=%v", runErr)
		}
	}()
	return nil
}

func (p *gatewayProcess) Stop() {
	if p == nil {
		return
	}
	if p.runtime != nil {
		p.runtime.Stop()
	}
	if p.updater != nil {
		p.updater.Stop()
	}
	if p.syncer != nil {
		p.syncer.Stop()
	}
}

func mustJSON(value any) string {
	blob, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(blob)
}

func buildRequiredCapabilityResolver(runtime *gatewayAgentRuntime) (gateway.CapabilityResolver, error) {
	if runtime == nil {
		return nil, fmt.Errorf("agent runtime is required")
	}
	requiredByActor := make(map[sessionrt.ActorID][]scheduler.Capability, len(runtime.Agents)*2)
	for actorID, agent := range runtime.Agents {
		if agent == nil {
			continue
		}
		canonicalActor := normalizeCapabilityResolverActorID(actorID)
		if canonicalActor == "" {
			continue
		}
		required := make([]scheduler.Capability, 0, len(agent.Config.Execution.RequiredCapabilities))
		for _, raw := range agent.Config.Execution.RequiredCapabilities {
			capability, err := config.ParseCapability(raw)
			if err != nil {
				return nil, fmt.Errorf("parse execution.required_capabilities for agent %q: %w", actorID, err)
			}
			required = append(required, capability)
		}
		requiredByActor[canonicalActor] = required
		alt := alternateCapabilityResolverActorID(canonicalActor)
		if alt != "" {
			requiredByActor[alt] = required
		}
	}
	return func(input sessionrt.AgentInput) []scheduler.Capability {
		actorID := normalizeCapabilityResolverActorID(input.ActorID)
		if actorID == "" {
			return nil
		}
		required, ok := requiredByActor[actorID]
		if !ok {
			return nil
		}
		return append([]scheduler.Capability(nil), required...)
	}, nil
}

func normalizeCapabilityResolverActorID(actorID sessionrt.ActorID) sessionrt.ActorID {
	value := strings.TrimSpace(string(actorID))
	if value == "" {
		return ""
	}
	return sessionrt.ActorID(value)
}

func alternateCapabilityResolverActorID(actorID sessionrt.ActorID) sessionrt.ActorID {
	value := string(actorID)
	if strings.HasPrefix(value, "agent:") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(value, "agent:"))
		if trimmed == "" {
			return ""
		}
		return sessionrt.ActorID(trimmed)
	}
	return sessionrt.ActorID("agent:" + value)
}
