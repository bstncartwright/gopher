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
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "try:")
	fmt.Fprintln(out, "  gopher gateway run --help")
}
