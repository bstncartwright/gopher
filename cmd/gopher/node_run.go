package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bstncartwright/gopher/pkg/config"
	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
)

const (
	defaultGatewayConfigPath = "/etc/gopher/gopher.toml"
	defaultNodeRejoinTimeout = 12 * time.Second
	defaultNodeAdminTimeout  = 10 * time.Second
)

var errNodeRestartRequested = errors.New("node restart requested")

var loadGatewayConfigForNodeAdmin = func(path string) (config.GatewayConfig, error) {
	cfg, _, err := config.LoadGatewayConfig(config.GatewayLoadOptions{ConfigPath: path})
	if err != nil {
		return config.GatewayConfig{}, err
	}
	return cfg, nil
}

var newNodeAdminClient = func(opts fabricts.ClientOptions) (fabricts.Fabric, func() error, error) {
	client, err := fabricts.NewClient(opts)
	if err != nil {
		return nil, nil, err
	}
	return client, client.Close, nil
}

var (
	runNodeSelfUpdate = func(ctx context.Context, stdout, stderr io.Writer) error {
		_ = ctx
		return runUpdateSubcommand([]string{"--no-service-restart"}, stdout, stderr)
	}
	nodeBinaryVersionForAdmin = currentBinaryVersion
)

type nodeRunInputs struct {
	ConfigPath string
	Overrides  config.NodeOverrides
}

type nodeConfigureInputs struct {
	TargetNode        string
	GatewayConfigPath string
	GatewayNATSURL    string
	Request           node.AdminConfigureRequest
	Restart           bool
	RestartTimeout    time.Duration
}

type nodeRestartInputs struct {
	TargetNode        string
	GatewayConfigPath string
	GatewayNATSURL    string
	VerifyTimeout     time.Duration
}

type nodeAdminService struct {
	mu             sync.Mutex
	cfg            config.NodeConfig
	primaryPath    string
	runtime        *node.Runtime
	requestRestart func(string)
}

func runNodeSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printNodeUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "run":
		if wantsHelp(args[1:]) {
			printNodeUsage(stdout)
			return nil
		}
		inputs, err := parseNodeRunFlags(args[1:])
		if err != nil {
			return err
		}
		workingDir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		cfg, sources, err := config.LoadNodeConfig(config.NodeLoadOptions{
			WorkingDir: workingDir,
			ConfigPath: inputs.ConfigPath,
			Overrides:  inputs.Overrides,
		})
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return runNodeWithContext(ctx, cfg, sources, stderr)
	case "configure":
		if wantsHelp(args[1:]) {
			printNodeUsage(stdout)
			return nil
		}
		return runNodeConfigureSubcommand(args[1:], stdout, stderr)
	case "restart":
		if wantsHelp(args[1:]) {
			printNodeUsage(stdout)
			return nil
		}
		return runNodeRestartSubcommand(args[1:], stdout, stderr)
	case "config":
		return runNodeConfigSubcommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printNodeUsage(stdout)
		return nil
	default:
		printNodeUsage(stderr)
		return fmt.Errorf("unknown node command %q", args[0])
	}
}

func printNodeUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher node run [flags]")
	fmt.Fprintln(out, "  gopher node configure [flags]")
	fmt.Fprintln(out, "  gopher node restart [flags]")
	fmt.Fprintln(out, "  gopher node config init [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "run flags:")
	fmt.Fprintln(out, "  --config <path>                path to toml config (default: ./node.toml)")
	fmt.Fprintln(out, "  --node-id <id>                 override node id")
	fmt.Fprintln(out, "  --nats-url <url>               override nats server url")
	fmt.Fprintln(out, "  --heartbeat-interval <dur>     override heartbeat interval")
	fmt.Fprintln(out, "  --capability <kind:name>       repeatable capability override")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "configure flags:")
	fmt.Fprintln(out, "  --target-node <id>             remote node id to configure (required)")
	fmt.Fprintln(out, "  --gateway-config <path>        gateway config used to resolve NATS (default: /etc/gopher/gopher.toml)")
	fmt.Fprintln(out, "  --gateway-nats-url <url>       explicit gateway control-plane NATS URL override")
	fmt.Fprintln(out, "  --node-id <id>                 persist new node.node_id")
	fmt.Fprintln(out, "  --node-nats-url <url>          persist node.nats.url")
	fmt.Fprintln(out, "  --node-connect-timeout <dur>   persist node.nats.connect_timeout")
	fmt.Fprintln(out, "  --node-reconnect-wait <dur>    persist node.nats.reconnect_wait")
	fmt.Fprintln(out, "  --node-heartbeat-interval <dur> persist node.runtime.heartbeat_interval")
	fmt.Fprintln(out, "  --capability <kind:name>       replace node capabilities (repeatable)")
	fmt.Fprintln(out, "  --clear-capabilities           replace capabilities with an empty list")
	fmt.Fprintln(out, "  --restart <bool>               request restart after configure (default: true)")
	fmt.Fprintln(out, "  --restart-timeout <dur>        best-effort re-register wait timeout (default: 12s)")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "restart flags:")
	fmt.Fprintln(out, "  --target-node <id>             remote node id to restart (required)")
	fmt.Fprintln(out, "  --gateway-config <path>        gateway config used to resolve NATS (default: /etc/gopher/gopher.toml)")
	fmt.Fprintln(out, "  --gateway-nats-url <url>       explicit gateway control-plane NATS URL override")
	fmt.Fprintln(out, "  --timeout <dur>                best-effort re-register wait timeout (default: 12s)")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "config init flags:")
	fmt.Fprintln(out, "  --path <path>                  output path (default: ./node.toml)")
	fmt.Fprintln(out, "  --force                        overwrite if file exists")
}

func parseNodeRunFlags(args []string) (nodeRunInputs, error) {
	flags := flag.NewFlagSet("node run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var rawCaps capabilityFlag
	configPath := flags.String("config", "", "config path")
	nodeID := flags.String("node-id", "", "node id override")
	natsURL := flags.String("nats-url", "", "nats url override")
	heartbeat := flags.Duration("heartbeat-interval", 0, "heartbeat interval override")
	flags.Var(&rawCaps, "capability", "repeatable capability kind:name")

	if err := flags.Parse(args); err != nil {
		return nodeRunInputs{}, err
	}
	if len(flags.Args()) > 0 {
		return nodeRunInputs{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	inputs := nodeRunInputs{
		ConfigPath: strings.TrimSpace(*configPath),
		Overrides:  config.NodeOverrides{},
	}
	if strings.TrimSpace(*nodeID) != "" {
		value := strings.TrimSpace(*nodeID)
		inputs.Overrides.NodeID = &value
	}
	if strings.TrimSpace(*natsURL) != "" {
		value := strings.TrimSpace(*natsURL)
		inputs.Overrides.NATSURL = &value
	}
	if *heartbeat != 0 {
		value := *heartbeat
		inputs.Overrides.HeartbeatInterval = &value
	}
	if len(rawCaps.values) > 0 {
		caps := make([]scheduler.Capability, 0, len(rawCaps.values))
		for _, raw := range rawCaps.values {
			capability, err := config.ParseCapability(raw)
			if err != nil {
				return nodeRunInputs{}, err
			}
			caps = append(caps, capability)
		}
		inputs.Overrides.Capabilities = &caps
	}
	return inputs, nil
}

func runNodeWithContext(ctx context.Context, cfg config.NodeConfig, sources []string, stderr io.Writer) error {
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve workspace directory: %w", err)
	}
	logger, cleanupLogs, err := setupProcessLogging(workspace, "node", stderr)
	if err != nil {
		return err
	}
	defer cleanupLogs()

	client, err := fabricts.NewClient(fabricts.ClientOptions{
		URL:            cfg.NATSURL,
		Name:           "gopher-node-" + cfg.NodeID,
		ConnectTimeout: cfg.ConnectTimeout,
		ReconnectWait:  cfg.ReconnectWait,
	})
	if err != nil {
		return fmt.Errorf("create nats client: %w", err)
	}
	defer client.Close()

	runtimeExecutor, err := loadAgentRuntime(workspace)
	if err != nil {
		return err
	}

	restartRequests := make(chan string, 1)
	adminService, err := newNodeAdminService(cfg, workspace, func(reason string) {
		select {
		case restartRequests <- strings.TrimSpace(reason):
		default:
		}
	})
	if err != nil {
		return err
	}

	runtime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            cfg.NodeID,
		IsGateway:         false,
		Version:           currentBinaryVersion(),
		Capabilities:      cfg.Capabilities,
		Fabric:            client,
		Executor:          runtimeExecutor.Executor,
		AdminHandler:      adminService,
		HeartbeatInterval: cfg.HeartbeatInterval,
	})
	if err != nil {
		return fmt.Errorf("create node runtime: %w", err)
	}
	adminService.BindRuntime(runtime)
	if err := runtime.Start(ctx); err != nil {
		return fmt.Errorf("start node runtime: %w", err)
	}
	defer runtime.Stop()

	logger.Printf("node running node_id=%s nats_url=%q heartbeat_interval=%s capabilities=%s config_sources=%s",
		cfg.NodeID,
		cfg.NATSURL,
		cfg.HeartbeatInterval.String(),
		mustJSON(cfg.Capabilities),
		strings.Join(sources, ","),
	)

	select {
	case <-ctx.Done():
		logger.Printf("node shutting down: %v", ctx.Err())
		return nil
	case reason := <-restartRequests:
		if reason == "" {
			reason = "remote admin request"
		}
		logger.Printf("node restart requested: %s", reason)
		return fmt.Errorf("%w: %s", errNodeRestartRequested, reason)
	}
}

func (s *nodeAdminService) BindRuntime(runtime *node.Runtime) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runtime = runtime
}

func (s *nodeAdminService) HandleAdmin(req node.AdminRequest) node.AdminResponse {
	switch strings.TrimSpace(string(req.Action)) {
	case string(node.AdminActionConfigure):
		return s.handleConfigure(req.Configure)
	case string(node.AdminActionRestart):
		return s.handleRestart()
	case string(node.AdminActionUpdate):
		return s.handleUpdate(req.Update)
	default:
		return node.AdminResponse{OK: false, Error: fmt.Sprintf("unsupported action %q", req.Action)}
	}
}

func (s *nodeAdminService) handleConfigure(req *node.AdminConfigureRequest) node.AdminResponse {
	if req == nil {
		return node.AdminResponse{OK: false, Error: "configure payload is required"}
	}

	s.mu.Lock()
	current := s.cfg
	next := s.cfg
	if err := applyAdminConfigureRequest(&next, req); err != nil {
		s.mu.Unlock()
		return node.AdminResponse{OK: false, Error: err.Error()}
	}
	if err := config.ValidateNodeConfig(&next); err != nil {
		s.mu.Unlock()
		return node.AdminResponse{OK: false, Error: err.Error()}
	}
	if err := config.WriteNodeConfigFile(s.primaryPath, next); err != nil {
		s.mu.Unlock()
		return node.AdminResponse{OK: false, Error: fmt.Sprintf("persist node config: %v", err)}
	}
	runtime := s.runtime
	s.cfg = next
	s.cfg.PrimaryConfigPath = s.primaryPath
	s.mu.Unlock()

	warnings := []string{}
	if runtime != nil {
		heartbeat := next.HeartbeatInterval
		caps := append([]scheduler.Capability(nil), next.Capabilities...)
		if err := runtime.ApplyRuntimeConfig(node.RuntimeConfigUpdate{
			Capabilities:      &caps,
			HeartbeatInterval: &heartbeat,
		}); err != nil {
			warnings = append(warnings, fmt.Sprintf("runtime apply warning: %v", err))
		}
	}
	if nodeConfigRequiresRestart(current, next) {
		warnings = append(warnings, "restart required to apply node_id and/or node.nats changes")
	}

	return node.AdminResponse{
		OK:            true,
		Warnings:      warnings,
		PersistedPath: s.primaryPath,
	}
}

func (s *nodeAdminService) handleRestart() node.AdminResponse {
	if s.requestRestart == nil {
		return node.AdminResponse{OK: false, Error: "restart handler is unavailable"}
	}
	go func(trigger func(string)) {
		time.Sleep(150 * time.Millisecond)
		trigger("remote admin restart request")
	}(s.requestRestart)
	return node.AdminResponse{OK: true, RestartRequested: true}
}

func (s *nodeAdminService) handleUpdate(req *node.AdminUpdateRequest) node.AdminResponse {
	targetVersion := ""
	if req != nil && req.TargetVersion != nil {
		targetVersion = strings.TrimSpace(*req.TargetVersion)
	}
	currentVersion := strings.TrimSpace(nodeBinaryVersionForAdmin())
	if targetVersion != "" && currentVersion == targetVersion {
		return node.AdminResponse{OK: true}
	}

	go func() {
		var out bytes.Buffer
		var errOut bytes.Buffer
		if err := runNodeSelfUpdate(context.Background(), &out, &errOut); err != nil {
			slog.Warn("node_admin: update request failed",
				"target_version", targetVersion,
				"current_version", currentVersion,
				"error", err,
				"stderr", strings.TrimSpace(errOut.String()),
			)
			return
		}
		if s.requestRestart != nil {
			s.requestRestart("remote admin update request")
		}
	}()

	return node.AdminResponse{OK: true, UpdateRequested: true}
}

func newNodeAdminService(cfg config.NodeConfig, workingDir string, requestRestart func(string)) (*nodeAdminService, error) {
	primaryPath, err := resolvePrimaryNodeConfigPath(cfg, workingDir)
	if err != nil {
		return nil, err
	}
	cfg.PrimaryConfigPath = primaryPath
	return &nodeAdminService{
		cfg:            cfg,
		primaryPath:    primaryPath,
		requestRestart: requestRestart,
	}, nil
}

func resolvePrimaryNodeConfigPath(cfg config.NodeConfig, workingDir string) (string, error) {
	if strings.TrimSpace(cfg.PrimaryConfigPath) != "" {
		return filepath.Abs(strings.TrimSpace(cfg.PrimaryConfigPath))
	}
	if strings.TrimSpace(workingDir) == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = cwd
	}
	base, err := filepath.Abs(strings.TrimSpace(workingDir))
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return filepath.Join(base, "node.toml"), nil
}

func applyAdminConfigureRequest(cfg *config.NodeConfig, req *node.AdminConfigureRequest) error {
	if cfg == nil {
		return fmt.Errorf("node config is required")
	}
	if req == nil {
		return fmt.Errorf("configure payload is required")
	}
	changed := false
	if req.NodeID != nil {
		cfg.NodeID = strings.TrimSpace(*req.NodeID)
		changed = true
	}
	if req.NATS != nil {
		if req.NATS.URL != nil {
			cfg.NATSURL = strings.TrimSpace(*req.NATS.URL)
			changed = true
		}
		if req.NATS.ConnectTimeout != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*req.NATS.ConnectTimeout))
			if err != nil {
				return fmt.Errorf("invalid node.nats.connect_timeout: %w", err)
			}
			cfg.ConnectTimeout = duration
			changed = true
		}
		if req.NATS.ReconnectWait != nil {
			duration, err := time.ParseDuration(strings.TrimSpace(*req.NATS.ReconnectWait))
			if err != nil {
				return fmt.Errorf("invalid node.nats.reconnect_wait: %w", err)
			}
			cfg.ReconnectWait = duration
			changed = true
		}
	}
	if req.Runtime != nil && req.Runtime.HeartbeatInterval != nil {
		duration, err := time.ParseDuration(strings.TrimSpace(*req.Runtime.HeartbeatInterval))
		if err != nil {
			return fmt.Errorf("invalid node.runtime.heartbeat_interval: %w", err)
		}
		cfg.HeartbeatInterval = duration
		changed = true
	}
	if req.Capabilities != nil {
		caps := make([]scheduler.Capability, 0, len(*req.Capabilities))
		for _, raw := range *req.Capabilities {
			capability, err := config.ParseCapability(strings.TrimSpace(raw))
			if err != nil {
				return fmt.Errorf("invalid node.capabilities entry: %w", err)
			}
			caps = append(caps, capability)
		}
		cfg.Capabilities = caps
		changed = true
	}
	if !changed {
		return fmt.Errorf("configure request did not include any updates")
	}
	return nil
}

func nodeConfigRequiresRestart(previous config.NodeConfig, next config.NodeConfig) bool {
	if strings.TrimSpace(previous.NodeID) != strings.TrimSpace(next.NodeID) {
		return true
	}
	if strings.TrimSpace(previous.NATSURL) != strings.TrimSpace(next.NATSURL) {
		return true
	}
	if previous.ConnectTimeout != next.ConnectTimeout {
		return true
	}
	if previous.ReconnectWait != next.ReconnectWait {
		return true
	}
	return false
}

func runNodeConfigureSubcommand(args []string, stdout, stderr io.Writer) error {
	inputs, err := parseNodeConfigureFlags(args)
	if err != nil {
		return err
	}
	clientOpts, err := resolveNodeAdminClientOptions(inputs.GatewayConfigPath, inputs.GatewayNATSURL)
	if err != nil {
		return err
	}
	fabric, closeClient, err := newNodeAdminClient(clientOpts)
	if err != nil {
		return fmt.Errorf("connect control plane nats: %w", err)
	}
	defer closeClient()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	response, err := sendNodeAdminRequest(ctx, fabric, inputs.TargetNode, node.AdminRequest{
		Action:    node.AdminActionConfigure,
		Configure: &inputs.Request,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "configured node %s\n", inputs.TargetNode)
	if strings.TrimSpace(response.PersistedPath) != "" {
		fmt.Fprintf(stdout, "persisted_path=%s\n", strings.TrimSpace(response.PersistedPath))
	}
	for _, warning := range response.Warnings {
		trimmed := strings.TrimSpace(warning)
		if trimmed == "" {
			continue
		}
		fmt.Fprintf(stderr, "warning: %s\n", trimmed)
	}

	if !inputs.Restart {
		return nil
	}
	expectedNodeID := inputs.TargetNode
	if inputs.Request.NodeID != nil && strings.TrimSpace(*inputs.Request.NodeID) != "" {
		expectedNodeID = strings.TrimSpace(*inputs.Request.NodeID)
	}
	return requestRemoteNodeRestart(context.Background(), fabric, inputs.TargetNode, expectedNodeID, inputs.RestartTimeout, stdout, stderr)
}

func runNodeRestartSubcommand(args []string, stdout, stderr io.Writer) error {
	inputs, err := parseNodeRestartFlags(args)
	if err != nil {
		return err
	}
	clientOpts, err := resolveNodeAdminClientOptions(inputs.GatewayConfigPath, inputs.GatewayNATSURL)
	if err != nil {
		return err
	}
	fabric, closeClient, err := newNodeAdminClient(clientOpts)
	if err != nil {
		return fmt.Errorf("connect control plane nats: %w", err)
	}
	defer closeClient()

	return requestRemoteNodeRestart(context.Background(), fabric, inputs.TargetNode, inputs.TargetNode, inputs.VerifyTimeout, stdout, stderr)
}

func parseNodeConfigureFlags(args []string) (nodeConfigureInputs, error) {
	flags := flag.NewFlagSet("node configure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	var rawCaps capabilityFlag
	targetNode := flags.String("target-node", "", "target node id")
	gatewayConfig := flags.String("gateway-config", defaultGatewayConfigPath, "gateway config path")
	gatewayNATSURL := flags.String("gateway-nats-url", "", "gateway nats url")
	nodeID := flags.String("node-id", "", "node id")
	nodeNATSURL := flags.String("node-nats-url", "", "node nats url")
	nodeConnectTimeout := flags.String("node-connect-timeout", "", "node nats connect timeout")
	nodeReconnectWait := flags.String("node-reconnect-wait", "", "node nats reconnect wait")
	nodeHeartbeatInterval := flags.String("node-heartbeat-interval", "", "node heartbeat interval")
	clearCapabilities := flags.Bool("clear-capabilities", false, "clear capabilities")
	restart := flags.Bool("restart", true, "request restart after configure")
	restartTimeout := flags.Duration("restart-timeout", defaultNodeRejoinTimeout, "restart rejoin timeout")
	flags.Var(&rawCaps, "capability", "repeatable capability kind:name")

	if err := flags.Parse(args); err != nil {
		return nodeConfigureInputs{}, err
	}
	if len(flags.Args()) > 0 {
		return nodeConfigureInputs{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*targetNode) == "" {
		return nodeConfigureInputs{}, fmt.Errorf("target node is required")
	}
	if *clearCapabilities && len(rawCaps.values) > 0 {
		return nodeConfigureInputs{}, fmt.Errorf("cannot combine --clear-capabilities with --capability")
	}

	request := node.AdminConfigureRequest{}
	updated := false
	if strings.TrimSpace(*nodeID) != "" {
		value := strings.TrimSpace(*nodeID)
		request.NodeID = &value
		updated = true
	}
	if strings.TrimSpace(*nodeNATSURL) != "" {
		value := strings.TrimSpace(*nodeNATSURL)
		request.NATS = ensureAdminNATS(request.NATS)
		request.NATS.URL = &value
		updated = true
	}
	if strings.TrimSpace(*nodeConnectTimeout) != "" {
		value := strings.TrimSpace(*nodeConnectTimeout)
		if _, err := time.ParseDuration(value); err != nil {
			return nodeConfigureInputs{}, fmt.Errorf("invalid --node-connect-timeout: %w", err)
		}
		request.NATS = ensureAdminNATS(request.NATS)
		request.NATS.ConnectTimeout = &value
		updated = true
	}
	if strings.TrimSpace(*nodeReconnectWait) != "" {
		value := strings.TrimSpace(*nodeReconnectWait)
		if _, err := time.ParseDuration(value); err != nil {
			return nodeConfigureInputs{}, fmt.Errorf("invalid --node-reconnect-wait: %w", err)
		}
		request.NATS = ensureAdminNATS(request.NATS)
		request.NATS.ReconnectWait = &value
		updated = true
	}
	if strings.TrimSpace(*nodeHeartbeatInterval) != "" {
		value := strings.TrimSpace(*nodeHeartbeatInterval)
		if _, err := time.ParseDuration(value); err != nil {
			return nodeConfigureInputs{}, fmt.Errorf("invalid --node-heartbeat-interval: %w", err)
		}
		request.Runtime = ensureAdminRuntime(request.Runtime)
		request.Runtime.HeartbeatInterval = &value
		updated = true
	}
	if *clearCapabilities {
		caps := []string{}
		request.Capabilities = &caps
		updated = true
	}
	if len(rawCaps.values) > 0 {
		caps := make([]string, 0, len(rawCaps.values))
		for _, raw := range rawCaps.values {
			raw = strings.TrimSpace(raw)
			if _, err := config.ParseCapability(raw); err != nil {
				return nodeConfigureInputs{}, err
			}
			caps = append(caps, raw)
		}
		request.Capabilities = &caps
		updated = true
	}
	if !updated {
		return nodeConfigureInputs{}, fmt.Errorf("at least one node config field is required")
	}
	if *restartTimeout <= 0 {
		return nodeConfigureInputs{}, fmt.Errorf("restart timeout must be > 0")
	}

	return nodeConfigureInputs{
		TargetNode:        strings.TrimSpace(*targetNode),
		GatewayConfigPath: strings.TrimSpace(*gatewayConfig),
		GatewayNATSURL:    strings.TrimSpace(*gatewayNATSURL),
		Request:           request,
		Restart:           *restart,
		RestartTimeout:    *restartTimeout,
	}, nil
}

func parseNodeRestartFlags(args []string) (nodeRestartInputs, error) {
	flags := flag.NewFlagSet("node restart", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	targetNode := flags.String("target-node", "", "target node id")
	gatewayConfig := flags.String("gateway-config", defaultGatewayConfigPath, "gateway config path")
	gatewayNATSURL := flags.String("gateway-nats-url", "", "gateway nats url")
	verifyTimeout := flags.Duration("timeout", defaultNodeRejoinTimeout, "re-register timeout")
	if err := flags.Parse(args); err != nil {
		return nodeRestartInputs{}, err
	}
	if len(flags.Args()) > 0 {
		return nodeRestartInputs{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*targetNode) == "" {
		return nodeRestartInputs{}, fmt.Errorf("target node is required")
	}
	if *verifyTimeout <= 0 {
		return nodeRestartInputs{}, fmt.Errorf("timeout must be > 0")
	}
	return nodeRestartInputs{
		TargetNode:        strings.TrimSpace(*targetNode),
		GatewayConfigPath: strings.TrimSpace(*gatewayConfig),
		GatewayNATSURL:    strings.TrimSpace(*gatewayNATSURL),
		VerifyTimeout:     *verifyTimeout,
	}, nil
}

func ensureAdminNATS(value *node.AdminConfigureNATS) *node.AdminConfigureNATS {
	if value != nil {
		return value
	}
	return &node.AdminConfigureNATS{}
}

func ensureAdminRuntime(value *node.AdminConfigureRuntime) *node.AdminConfigureRuntime {
	if value != nil {
		return value
	}
	return &node.AdminConfigureRuntime{}
}

func resolveNodeAdminClientOptions(gatewayConfigPath string, gatewayNATSURL string) (fabricts.ClientOptions, error) {
	if strings.TrimSpace(gatewayNATSURL) != "" {
		return fabricts.ClientOptions{
			URL:  strings.TrimSpace(gatewayNATSURL),
			Name: "gopher-node-admin-cli",
		}, nil
	}
	configPath := strings.TrimSpace(gatewayConfigPath)
	if configPath == "" {
		configPath = defaultGatewayConfigPath
	}
	cfg, err := loadGatewayConfigForNodeAdmin(configPath)
	if err != nil {
		return fabricts.ClientOptions{}, fmt.Errorf("load gateway config %s: %w", configPath, err)
	}
	return fabricts.ClientOptions{
		URL:            cfg.NATSURL,
		Name:           "gopher-node-admin-cli",
		ConnectTimeout: cfg.ConnectTimeout,
		ReconnectWait:  cfg.ReconnectWait,
	}, nil
}

func sendNodeAdminRequest(ctx context.Context, fabric fabricts.Fabric, targetNode string, request node.AdminRequest) (node.AdminResponse, error) {
	targetNode = strings.TrimSpace(targetNode)
	if targetNode == "" {
		return node.AdminResponse{}, fmt.Errorf("target node is required")
	}
	blob, err := json.Marshal(request)
	if err != nil {
		return node.AdminResponse{}, fmt.Errorf("marshal admin request: %w", err)
	}
	responseBlob, err := fabric.Request(ctx, fabricts.NodeAdminSubject(targetNode), blob)
	if err != nil {
		return node.AdminResponse{}, fmt.Errorf("request node %s admin action %q: %w", targetNode, request.Action, err)
	}
	var response node.AdminResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		return node.AdminResponse{}, fmt.Errorf("decode node %s admin response: %w", targetNode, err)
	}
	if !response.OK {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = "node admin request failed"
		}
		return response, fmt.Errorf("node %s admin %q failed: %s", targetNode, request.Action, message)
	}
	return response, nil
}

func requestRemoteNodeRestart(
	ctx context.Context,
	fabric fabricts.Fabric,
	controlNodeID string,
	expectedNodeID string,
	timeout time.Duration,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if timeout <= 0 {
		timeout = defaultNodeRejoinTimeout
	}
	rejoinEvents, cleanup, err := subscribeNodeRejoinEvents(fabric, expectedNodeID)
	if err != nil {
		return err
	}
	defer cleanup()

	requestCtx, requestCancel := context.WithTimeout(ctx, defaultNodeAdminTimeout)
	defer requestCancel()
	if _, err := sendNodeAdminRequest(requestCtx, fabric, controlNodeID, node.AdminRequest{Action: node.AdminActionRestart}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "restart requested for node %s\n", controlNodeID)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-rejoinEvents:
		fmt.Fprintf(stdout, "node %s re-registered\n", expectedNodeID)
		return nil
	case <-timer.C:
		fmt.Fprintf(stderr, "warning: node %s did not re-register within %s\n", expectedNodeID, timeout)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func subscribeNodeRejoinEvents(fabric fabricts.Fabric, nodeID string) (<-chan struct{}, func(), error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil, nil, fmt.Errorf("node id is required")
	}
	events := make(chan struct{}, 1)
	handler := func(context.Context, fabricts.Message) {
		select {
		case events <- struct{}{}:
		default:
		}
	}
	statusSub, err := fabric.Subscribe(fabricts.NodeStatusSubject(nodeID), handler)
	if err != nil {
		return nil, nil, fmt.Errorf("subscribe node status for %s: %w", nodeID, err)
	}
	capSub, err := fabric.Subscribe(fabricts.NodeCapabilitiesSubject(nodeID), handler)
	if err != nil {
		statusSub.Unsubscribe()
		return nil, nil, fmt.Errorf("subscribe node capabilities for %s: %w", nodeID, err)
	}
	cleanup := func() {
		statusSub.Unsubscribe()
		capSub.Unsubscribe()
	}
	return events, cleanup, nil
}

func runNodeConfigSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printNodeUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "init":
		flags := flag.NewFlagSet("node config init", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		path := flags.String("path", "node.toml", "output path")
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
		if err := writeConfigFileWithBackup(target, []byte(config.DefaultNodeTOML())); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s\n", target)
		return nil
	case "help", "-h", "--help":
		printNodeUsage(stdout)
		return nil
	default:
		printNodeUsage(stderr)
		return fmt.Errorf("unknown node config command %q", args[0])
	}
}
