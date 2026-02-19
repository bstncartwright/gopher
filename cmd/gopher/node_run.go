package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/bstncartwright/gopher/pkg/config"
	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
)

type nodeRunInputs struct {
	ConfigPath string
	Overrides  config.NodeOverrides
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
	fmt.Fprintln(out, "  gopher node config init [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "run flags:")
	fmt.Fprintln(out, "  --config <path>                path to toml config (default: ./node.toml)")
	fmt.Fprintln(out, "  --node-id <id>                 override node id")
	fmt.Fprintln(out, "  --nats-url <url>               override nats server url")
	fmt.Fprintln(out, "  --heartbeat-interval <dur>     override heartbeat interval")
	fmt.Fprintln(out, "  --capability <kind:name>       repeatable capability override")
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
	logger := log.New(stderr, "", log.LstdFlags)

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

	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve workspace directory: %w", err)
	}
	runtimeExecutor, err := loadAgentRuntime(workspace)
	if err != nil {
		return err
	}

	runtime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            cfg.NodeID,
		IsGateway:         false,
		Capabilities:      cfg.Capabilities,
		Fabric:            client,
		Executor:          runtimeExecutor.Executor,
		HeartbeatInterval: cfg.HeartbeatInterval,
	})
	if err != nil {
		return fmt.Errorf("create node runtime: %w", err)
	}
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
	<-ctx.Done()
	logger.Printf("node shutting down: %v", ctx.Err())
	return nil
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
		if err := os.WriteFile(target, []byte(config.DefaultNodeTOML()), 0o644); err != nil {
			return fmt.Errorf("write config file %s: %w", target, err)
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
