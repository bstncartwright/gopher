package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printRootUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "help", "-h", "--help":
		printRootUsage(stdout)
		return nil
	case "version", "-v", "--version":
		printBinaryVersion(stdout)
		return nil
	case "gateway":
		return runGatewaySubcommand(args[1:], stdout, stderr)
	case "node":
		return runNodeSubcommand(args[1:], stdout, stderr)
	case "status":
		serviceArgs := append([]string{"status"}, args[1:]...)
		return runServiceSubcommand(serviceArgs, stdout, stderr)
	case "restart":
		serviceArgs := append([]string{"restart"}, args[1:]...)
		return runServiceSubcommand(serviceArgs, stdout, stderr)
	case "logs":
		serviceArgs := append([]string{"logs"}, args[1:]...)
		return runServiceSubcommand(serviceArgs, stdout, stderr)
	case "service":
		return runServiceSubcommand(args[1:], stdout, stderr)
	case "agent":
		return runAgentSubcommand(args[1:], stdout, stderr)
	case "auth":
		return runAuthSubcommand(args[1:], stdout, stderr)
	case "reset":
		return runFactoryResetSubcommand(args[1:], stdout, stderr)
	case "onboard":
		return runOnboardingSubcommand(args[1:], os.Stdin, stdout, stderr)
	case "update":
		return runUpdateSubcommand(args[1:], stdout, stderr)
	default:
		printRootUsage(stderr)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printRootUsage(out io.Writer) {
	fmt.Fprintln(out, "gopher cli")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher <command> [args]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "commands:")
	fmt.Fprintln(out, "  gateway run           start gateway node runtime")
	fmt.Fprintln(out, "  gateway config init   write starter gopher.toml")
	fmt.Fprintln(out, "  node run              start worker node runtime")
	fmt.Fprintln(out, "  node configure        configure a remote node over nats")
	fmt.Fprintln(out, "  node restart          request remote node restart over nats")
	fmt.Fprintln(out, "  node config init      write starter node.toml")
	fmt.Fprintln(out, "  status                show gopher service status")
	fmt.Fprintln(out, "  restart               restart gopher service")
	fmt.Fprintln(out, "  logs                  show gopher service logs")
	fmt.Fprintln(out, "  agent ...             manage agent registry and workspaces")
	fmt.Fprintln(out, "  auth ...              manage provider auth env settings")
	fmt.Fprintln(out, "  onboard               write defaults and configure auth/telegram")
	fmt.Fprintln(out, "  reset                 delete config and memory while preserving auth")
	fmt.Fprintln(out, "  update                check and apply binary updates")
	fmt.Fprintln(out, "  version               print binary version")
	fmt.Fprintln(out, "  service ...           install and manage linux systemd service")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "try:")
	fmt.Fprintln(out, "  gopher gateway run --help")
	fmt.Fprintln(out, "  gopher node run --help")
	fmt.Fprintln(out, "  gopher agent --help")
}
