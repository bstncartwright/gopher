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
	fmt.Fprintln(out, "  --matrix-enabled <bool>        override matrix transport enablement")
	fmt.Fprintln(out, "  --matrix-homeserver-url <url>  override matrix homeserver url")
	fmt.Fprintln(out, "  --matrix-appservice-id <id>    override matrix appservice id")
	fmt.Fprintln(out, "  --matrix-as-token <token>      override matrix appservice token")
	fmt.Fprintln(out, "  --matrix-hs-token <token>      override matrix homeserver token")
	fmt.Fprintln(out, "  --matrix-listen-addr <addr>    override matrix appservice listen address")
	fmt.Fprintln(out, "  --matrix-bot-user-id <mxid>    override matrix bot user id")
	fmt.Fprintln(out, "  --matrix-rich-text-enabled <bool> override matrix markdown/html formatting")
	fmt.Fprintln(out, "  --matrix-presence-enabled <bool> override matrix presence updates")
	fmt.Fprintln(out, "  --matrix-presence-interval <dur> override matrix presence keepalive interval")
	fmt.Fprintln(out, "  --matrix-presence-status-msg <text> override matrix presence status message")
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
	var matrixEnabled boolOverrideFlag
	var matrixRichTextEnabled boolOverrideFlag
	var matrixPresenceEnabled boolOverrideFlag
	var panelCaptureThinking boolOverrideFlag
	var cronEnabled boolOverrideFlag
	configPath := flags.String("config", "", "config path")
	nodeID := flags.String("node-id", "", "gateway node id override")
	gatewayID := flags.String("gateway-id", "", "gateway id override")
	natsURL := flags.String("nats-url", "", "nats url override")
	heartbeat := flags.Duration("heartbeat-interval", 0, "heartbeat interval override")
	prune := flags.Duration("prune-interval", 0, "prune interval override")
	flags.Var(&rawCaps, "capability", "repeatable capability kind:name")
	flags.Var(&matrixEnabled, "matrix-enabled", "matrix enabled override")
	matrixHomeserver := flags.String("matrix-homeserver-url", "", "matrix homeserver url override")
	matrixAppservice := flags.String("matrix-appservice-id", "", "matrix appservice id override")
	matrixASToken := flags.String("matrix-as-token", "", "matrix as token override")
	matrixHSToken := flags.String("matrix-hs-token", "", "matrix hs token override")
	matrixListenAddr := flags.String("matrix-listen-addr", "", "matrix listen addr override")
	matrixBotUserID := flags.String("matrix-bot-user-id", "", "matrix bot user id override")
	flags.Var(&matrixRichTextEnabled, "matrix-rich-text-enabled", "matrix rich text enabled override")
	flags.Var(&matrixPresenceEnabled, "matrix-presence-enabled", "matrix presence enabled override")
	matrixPresenceInterval := flags.Duration("matrix-presence-interval", 0, "matrix presence interval override")
	matrixPresenceStatusMsg := flags.String("matrix-presence-status-msg", "", "matrix presence status message override")
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
	if matrixEnabled.set {
		value := matrixEnabled.value
		inputs.Overrides.MatrixEnabled = &value
	}
	if strings.TrimSpace(*matrixHomeserver) != "" {
		value := strings.TrimSpace(*matrixHomeserver)
		inputs.Overrides.MatrixHomeserver = &value
	}
	if strings.TrimSpace(*matrixAppservice) != "" {
		value := strings.TrimSpace(*matrixAppservice)
		inputs.Overrides.MatrixAppservice = &value
	}
	if strings.TrimSpace(*matrixASToken) != "" {
		value := strings.TrimSpace(*matrixASToken)
		inputs.Overrides.MatrixASToken = &value
	}
	if strings.TrimSpace(*matrixHSToken) != "" {
		value := strings.TrimSpace(*matrixHSToken)
		inputs.Overrides.MatrixHSToken = &value
	}
	if strings.TrimSpace(*matrixListenAddr) != "" {
		value := strings.TrimSpace(*matrixListenAddr)
		inputs.Overrides.MatrixListenAddr = &value
	}
	if strings.TrimSpace(*matrixBotUserID) != "" {
		value := strings.TrimSpace(*matrixBotUserID)
		inputs.Overrides.MatrixBotUserID = &value
	}
	if matrixRichTextEnabled.set {
		value := matrixRichTextEnabled.value
		inputs.Overrides.MatrixRichTextEnabled = &value
	}
	if matrixPresenceEnabled.set {
		value := matrixPresenceEnabled.value
		inputs.Overrides.MatrixPresenceEnabled = &value
	}
	if *matrixPresenceInterval != 0 {
		value := *matrixPresenceInterval
		inputs.Overrides.MatrixPresenceInterval = &value
	}
	if strings.TrimSpace(*matrixPresenceStatusMsg) != "" {
		value := strings.TrimSpace(*matrixPresenceStatusMsg)
		inputs.Overrides.MatrixPresenceStatusMsg = &value
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
	logger := log.New(stderr, "", log.LstdFlags)

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

	workspace, err := os.Getwd()
	if err != nil {
		slog.Error("gateway_run: failed to resolve workspace", "error", err)
		return fmt.Errorf("resolve workspace directory: %w", err)
	}
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

	var matrixBridge *matrixDMBridge
	if cfg.Matrix.Enabled {
		slog.Info("gateway_run: matrix enabled, starting dm bridge")
		matrixBridge, err = startMatrixDMBridgeWithRuntime(ctx, cfg, workspace, agentRuntime, process.executor, logger)
		if err != nil {
			slog.Error("gateway_run: failed to start matrix dm bridge", "error", err)
			return err
		}
		defer matrixBridge.Stop()
	} else {
		slog.Info("gateway_run: matrix disabled")
	}

	var panelStore panel.SessionStore
	var panelSessionMetadata panel.SessionMetadataResolver
	if matrixBridge != nil && matrixBridge.store != nil {
		panelStore = matrixBridge.store
	}
	if matrixBridge != nil && matrixBridge.bindings != nil {
		bindings := matrixBridge.bindings
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
	if err := startGatewayPanel(ctx, cfg, process, panelStore, panelSessionMetadata, logger); err != nil {
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

func startGatewayProcess(
	ctx context.Context,
	cfg config.GatewayConfig,
	fabric fabricts.Fabric,
	executor sessionrt.AgentExecutor,
	logger *log.Logger,
) (*gatewayProcess, error) {
	return startGatewayProcessWithCapabilityResolver(ctx, cfg, fabric, executor, nil, logger)
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

	distributedExecutor, err := gateway.NewDistributedExecutor(gateway.DistributedExecutorOptions{
		GatewayNodeID:      cfg.GatewayNodeID,
		LocalExecutor:      executor,
		Scheduler:          schedulerInstance,
		Fabric:             fabric,
		CapabilityResolver: resolver,
	})
	if err != nil {
		syncer.Stop()
		return nil, fmt.Errorf("create distributed executor: %w", err)
	}

	runtime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            cfg.NodeID,
		IsGateway:         true,
		Capabilities:      cfg.Capabilities,
		Fabric:            fabric,
		Executor:          distributedExecutor,
		HeartbeatInterval: cfg.HeartbeatInterval,
	})
	if err != nil {
		syncer.Stop()
		return nil, fmt.Errorf("create gateway runtime: %w", err)
	}
	if err := runtime.Start(ctx); err != nil {
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
		if err := os.WriteFile(target, []byte(config.DefaultGatewayTOML()), 0o644); err != nil {
			return fmt.Errorf("write config file %s: %w", target, err)
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
