package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/config"
	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/gateway"
	"github.com/bstncartwright/gopher/pkg/scheduler"
)

const defaultGatewayNodesListWait = 3 * time.Second

type gatewayNodesListInputs struct {
	ConfigPath string
	Wait       time.Duration
	Overrides  config.GatewayOverrides
}

type gatewayNodesClient interface {
	fabricts.Fabric
	Close() error
}

var newGatewayNodesClient = func(opts fabricts.ClientOptions) (gatewayNodesClient, error) {
	return fabricts.NewClient(opts)
}

func runGatewayNodesSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printGatewayNodesUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "list":
		if wantsHelp(args[1:]) {
			printGatewayNodesUsage(stdout)
			return nil
		}
		return runGatewayNodesListCommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printGatewayNodesUsage(stdout)
		return nil
	default:
		printGatewayNodesUsage(stderr)
		return fmt.Errorf("unknown gateway nodes command %q", args[0])
	}
}

func runGatewayNodesListCommand(args []string, stdout, _ io.Writer) error {
	inputs, err := parseGatewayNodesListFlags(args)
	if err != nil {
		return err
	}

	workingDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	cfg, _, err := config.LoadGatewayConfig(config.GatewayLoadOptions{
		WorkingDir: workingDir,
		ConfigPath: inputs.ConfigPath,
		Overrides:  inputs.Overrides,
	})
	if err != nil {
		return err
	}

	client, err := newGatewayNodesClient(fabricts.ClientOptions{
		URL:            cfg.NATSURL,
		Name:           "gopher-gateway-nodes-list",
		ConnectTimeout: cfg.ConnectTimeout,
		ReconnectWait:  cfg.ReconnectWait,
	})
	if err != nil {
		return fmt.Errorf("create nats client: %w", err)
	}
	defer client.Close()

	nodes, err := observeGatewayNodes(context.Background(), client, cfg.PruneInterval, inputs.Wait)
	if err != nil {
		return err
	}
	printGatewayNodeList(stdout, nodes)
	return nil
}

func parseGatewayNodesListFlags(args []string) (gatewayNodesListInputs, error) {
	flags := flag.NewFlagSet("gateway nodes list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	configPath := flags.String("config", "", "config path")
	natsURL := flags.String("nats-url", "", "nats url override")
	wait := flags.Duration("wait", defaultGatewayNodesListWait, "observation window for node heartbeats")

	if err := flags.Parse(args); err != nil {
		return gatewayNodesListInputs{}, err
	}
	if len(flags.Args()) > 0 {
		return gatewayNodesListInputs{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *wait <= 0 {
		return gatewayNodesListInputs{}, fmt.Errorf("--wait must be greater than 0")
	}

	inputs := gatewayNodesListInputs{
		ConfigPath: strings.TrimSpace(*configPath),
		Wait:       *wait,
		Overrides:  config.GatewayOverrides{},
	}
	if strings.TrimSpace(*natsURL) != "" {
		value := strings.TrimSpace(*natsURL)
		inputs.Overrides.NATSURL = &value
	}
	return inputs, nil
}

func observeGatewayNodes(ctx context.Context, fabric fabricts.Fabric, pruneInterval, wait time.Duration) ([]scheduler.NodeInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if fabric == nil {
		return nil, fmt.Errorf("fabric is required")
	}
	if wait <= 0 {
		return nil, fmt.Errorf("wait duration must be greater than 0")
	}

	registry := scheduler.NewRegistry(0)
	syncer, err := gateway.NewNodeRegistrySync(gateway.NodeRegistrySyncOptions{
		Fabric:        fabric,
		Registry:      registry,
		PruneInterval: pruneInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("create node registry sync: %w", err)
	}
	if err := syncer.Start(ctx); err != nil {
		return nil, fmt.Errorf("start node registry sync: %w", err)
	}
	defer syncer.Stop()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}

	registry.PruneExpired(time.Now().UTC())
	return registry.Snapshot(), nil
}

func printGatewayNodeList(out io.Writer, nodes []scheduler.NodeInfo) {
	if len(nodes) == 0 {
		fmt.Fprintln(out, "no nodes observed")
		return
	}

	fmt.Fprintln(out, "node_id | role | last_heartbeat | capabilities")
	for _, node := range nodes {
		role := "node"
		if node.IsGateway {
			role = "gateway"
		}
		heartbeat := "-"
		if !node.LastHeartbeat.IsZero() {
			heartbeat = node.LastHeartbeat.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(
			out,
			"%s | %s | %s | %s\n",
			node.NodeID,
			role,
			heartbeat,
			formatNodeCapabilities(node.Capabilities),
		)
	}
}

func formatNodeCapabilities(capabilities []scheduler.Capability) string {
	if len(capabilities) == 0 {
		return "-"
	}

	parts := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		parts = append(parts, fmt.Sprintf("%s:%s", formatCapabilityKind(capability.Kind), capability.Name))
	}
	return strings.Join(parts, ",")
}

func formatCapabilityKind(kind scheduler.CapabilityKind) string {
	switch kind {
	case scheduler.CapabilityAgent:
		return "agent"
	case scheduler.CapabilityTool:
		return "tool"
	case scheduler.CapabilitySystem:
		return "system"
	default:
		return "unknown"
	}
}

func printGatewayNodesUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher gateway nodes list [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "flags:")
	fmt.Fprintln(out, "  --config <path>  path to toml config (default: ./gopher.toml)")
	fmt.Fprintln(out, "  --nats-url <url> override nats server url")
	fmt.Fprintln(out, "  --wait <dur>     observe heartbeats for this duration (default: 3s)")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "example:")
	fmt.Fprintln(out, "  gopher gateway nodes list --wait 5s")
}
