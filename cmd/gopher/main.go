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
	case "gateway":
		return runGatewaySubcommand(args[1:], stdout, stderr)
	case "status":
		return runServiceSubcommand([]string{"status"}, stdout, stderr)
	case "restart":
		return runServiceSubcommand([]string{"restart"}, stdout, stderr)
	case "logs":
		serviceArgs := append([]string{"logs"}, args[1:]...)
		return runServiceSubcommand(serviceArgs, stdout, stderr)
	case "service":
		return runServiceSubcommand(args[1:], stdout, stderr)
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
	fmt.Fprintln(out, "  status                show gopher service status")
	fmt.Fprintln(out, "  restart               restart gopher service")
	fmt.Fprintln(out, "  logs                  show gopher service logs")
	fmt.Fprintln(out, "  service ...           install and manage linux systemd service")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "try:")
	fmt.Fprintln(out, "  gopher gateway run --help")
}
