package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/memory"
)

func runMemorySubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printMemoryUsage(stdout)
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "status":
		return runMemoryStatusSubcommand(args[1:], stdout, stderr)
	case "index":
		return runMemoryIndexSubcommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printMemoryUsage(stdout)
		return nil
	default:
		printMemoryUsage(stderr)
		return fmt.Errorf("unknown memory command %q", args[0])
	}
}

func runDoctorSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(stdout, "usage: gopher doctor memory [--workspace PATH]")
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "memory":
		return runDoctorMemorySubcommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(stdout, "usage: gopher doctor memory [--workspace PATH]")
		return nil
	default:
		return fmt.Errorf("unknown doctor command %q", args[0])
	}
}

func runMemoryStatusSubcommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("memory status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", ".", "agent workspace path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	agent, err := loadAgentForMemoryCommand(*workspace)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if agent.MemorySearch == nil {
		fmt.Fprintln(stdout, "memory search: disabled")
		return nil
	}
	status, err := agent.MemorySearch.Status(ctx)
	if err != nil {
		return err
	}
	printMemoryStatus(stdout, status)
	return nil
}

func runMemoryIndexSubcommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("memory index", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", ".", "agent workspace path")
	force := flags.Bool("force", false, "force full index rebuild")
	if err := flags.Parse(args); err != nil {
		return err
	}
	agent, err := loadAgentForMemoryCommand(*workspace)
	if err != nil {
		return err
	}
	if agent.MemorySearch == nil {
		return errors.New("memory search is disabled in this workspace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := agent.MemorySearch.Sync(ctx, *force); err != nil {
		return err
	}
	status, err := agent.MemorySearch.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "memory index complete")
	printMemoryStatus(stdout, status)
	return nil
}

func runDoctorMemorySubcommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("doctor memory", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", ".", "agent workspace path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	agent, err := loadAgentForMemoryCommand(*workspace)
	if err != nil {
		return err
	}
	if agent.MemorySearch == nil {
		fmt.Fprintln(stdout, "memory doctor")
		fmt.Fprintln(stdout, "  status: disabled")
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status, err := agent.MemorySearch.Status(ctx)
	if err != nil {
		return err
	}
	embedErr := agent.MemorySearch.ProbeEmbedding(ctx)

	memoryDir := filepath.Join(agent.Workspace, "memory")
	permState := "ok"
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		permState = "error: " + err.Error()
	}

	fmt.Fprintln(stdout, "memory doctor")
	printMemoryStatus(stdout, status)
	if embedErr != nil {
		fmt.Fprintln(stdout, "embedding probe: unavailable")
		fmt.Fprintf(stdout, "embedding error: %s\n", strings.TrimSpace(embedErr.Error()))
	} else {
		fmt.Fprintln(stdout, "embedding probe: ok")
	}
	fmt.Fprintf(stdout, "permissions: %s\n", permState)

	if !status.FTSAvailable {
		return fmt.Errorf("memory doctor failed: fts unavailable")
	}
	if embedErr != nil && !status.FTSAvailable {
		return fmt.Errorf("memory doctor failed: both embeddings and fts are unavailable")
	}
	return nil
}

func printMemoryUsage(out io.Writer) {
	fmt.Fprintln(out, "gopher memory")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher memory status [--workspace PATH]")
	fmt.Fprintln(out, "  gopher memory index [--workspace PATH] [--force]")
}

func printMemoryStatus(out io.Writer, status memory.MemorySearchStatus) {
	fmt.Fprintln(out, "memory status")
	fmt.Fprintf(out, "  enabled: %t\n", status.Enabled)
	fmt.Fprintf(out, "  mode: %s\n", strings.TrimSpace(status.RetrievalMode()))
	fmt.Fprintf(out, "  provider: %s\n", strings.TrimSpace(status.Provider))
	fmt.Fprintf(out, "  model: %s\n", strings.TrimSpace(status.Model))
	fmt.Fprintf(out, "  files: %d\n", status.Files)
	fmt.Fprintf(out, "  chunks: %d\n", status.Chunks)
	fmt.Fprintf(out, "  fts_available: %t\n", status.FTSAvailable)
	fmt.Fprintf(out, "  vector_available: %t\n", status.VectorAvailable)
	fmt.Fprintf(out, "  dirty: %t\n", status.Dirty)
	if strings.TrimSpace(status.FallbackReason) != "" {
		fmt.Fprintf(out, "  fallback_reason: %s\n", status.FallbackReason)
	}
	if strings.TrimSpace(status.UnavailableReason) != "" {
		fmt.Fprintf(out, "  unavailable_reason: %s\n", status.UnavailableReason)
	}
}

func loadAgentForMemoryCommand(workspace string) (*agentcore.Agent, error) {
	if strings.TrimSpace(workspace) == "" {
		workspace = "."
	}
	resolved, err := filepath.Abs(workspace)
	if err != nil {
		return nil, err
	}
	return agentcore.LoadAgent(resolved)
}
