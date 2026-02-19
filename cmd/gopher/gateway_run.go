package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
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
	case "nodes":
		return runGatewayNodesSubcommand(args[1:], stdout, stderr)
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
	fmt.Fprintln(out, "  gopher gateway nodes list [flags]")
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
	fmt.Fprintln(out, "  --cron-enabled <bool>          override cron subsystem enablement")
	fmt.Fprintln(out, "  --cron-poll-interval <dur>     override cron polling interval")
	fmt.Fprintln(out, "  --cron-default-timezone <tz>   override default cron timezone")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "config init flags:")
	fmt.Fprintln(out, "  --path <path>                  output path (default: ./gopher.toml)")
	fmt.Fprintln(out, "  --force                        overwrite if file exists")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "nodes list flags:")
	fmt.Fprintln(out, "  --config <path>                path to toml config (default: ./gopher.toml)")
	fmt.Fprintln(out, "  --nats-url <url>               override nats server url")
	fmt.Fprintln(out, "  --wait <dur>                   observe heartbeats for this duration (default: 3s)")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "example:")
	fmt.Fprintln(out, "  gopher gateway config init")
	fmt.Fprintln(out, "  gopher gateway run --config ./gopher.toml --capability tool:gpu")
	fmt.Fprintln(out, "  gopher gateway nodes list --wait 5s")
}

func parseGatewayRunFlags(args []string) (gatewayRunInputs, error) {
	flags := flag.NewFlagSet("gateway run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var rawCaps capabilityFlag
	var matrixEnabled boolOverrideFlag
	var matrixRichTextEnabled boolOverrideFlag
	var matrixPresenceEnabled boolOverrideFlag
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

	client, err := fabricts.NewClient(fabricts.ClientOptions{
		URL:            cfg.NATSURL,
		Name:           "gopher-gateway-" + cfg.NodeID,
		ConnectTimeout: cfg.ConnectTimeout,
		ReconnectWait:  cfg.ReconnectWait,
	})
	if err != nil {
		return fmt.Errorf("create nats client: %w", err)
	}
	defer client.Close()

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve workspace directory: %w", err)
	}
	executor, err := loadGatewayRuntimeExecutor(workspace)
	if err != nil {
		return err
	}

	process, err := startGatewayProcess(ctx, cfg, client, executor, logger)
	if err != nil {
		return err
	}
	defer process.Stop()

	var matrixBridge *matrixDMBridge
	if cfg.Matrix.Enabled {
		matrixBridge, err = startMatrixDMBridge(ctx, cfg, logger)
		if err != nil {
			return err
		}
		defer matrixBridge.Stop()
	}

	logger.Printf("gateway running node_id=%s gateway_id=%s nats_url=%q heartbeat_interval=%s prune_interval=%s capabilities=%s config_sources=%s",
		cfg.NodeID,
		cfg.GatewayNodeID,
		cfg.NATSURL,
		cfg.HeartbeatInterval.String(),
		cfg.PruneInterval.String(),
		mustJSON(cfg.Capabilities),
		strings.Join(sources, ","),
	)
	<-ctx.Done()
	logger.Printf("gateway shutting down: %v", ctx.Err())
	return nil
}

func startGatewayProcess(
	ctx context.Context,
	cfg config.GatewayConfig,
	fabric fabricts.Fabric,
	executor sessionrt.AgentExecutor,
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

	runtime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            cfg.NodeID,
		IsGateway:         true,
		Capabilities:      cfg.Capabilities,
		Fabric:            fabric,
		Executor:          executor,
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
