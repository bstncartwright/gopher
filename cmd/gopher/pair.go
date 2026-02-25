package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func runPairSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printPairUsage(stdout)
		return nil
	}
	switch strings.TrimSpace(args[0]) {
	case "status":
		return runPairStatusSubcommand(args[1:], stdout, stderr)
	case "approve":
		return runPairApproveSubcommand(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printPairUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown pair command %q", args[0])
	}
}

func printPairUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher pair status  [flags]")
	fmt.Fprintln(out, "  gopher pair approve [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "commands:")
	fmt.Fprintln(out, "  status   show pending/active telegram pairing state")
	fmt.Fprintln(out, "  approve  approve the latest pending pairing request")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "flags:")
	fmt.Fprintln(out, "  --workspace <path>  gateway workspace or data directory parent (default: $HOME)")
}

func runPairStatusSubcommand(args []string, stdout, stderr io.Writer) error {
	dataDir, err := pairWorkspaceDataDir(args, stdout, stderr)
	if err != nil {
		return err
	}
	state, err := readTelegramPairingState(dataDir)
	if err != nil {
		return err
	}

	if state.PairedChatID != "" {
		fmt.Fprintf(stdout, "paired chat id: %s\n", state.PairedChatID)
		if state.PairedUserID != "" {
			fmt.Fprintf(stdout, "paired user id: %s\n", state.PairedUserID)
		}
	}
	if state.Pending != nil {
		fmt.Fprintln(stdout, "pending pair request:")
		fmt.Fprintf(stdout, "  chat id:   %s\n", state.Pending.ChatID)
		fmt.Fprintf(stdout, "  user id:   %s\n", state.Pending.UserID)
		if state.Pending.Conversation != "" {
			fmt.Fprintf(stdout, "  title:     %s\n", state.Pending.Conversation)
		}
		if state.Pending.SenderUsername != "" {
			fmt.Fprintf(stdout, "  username:  %s\n", state.Pending.SenderUsername)
		}
		if state.Pending.RequestedAt != "" {
			fmt.Fprintf(stdout, "  requested: %s\n", state.Pending.RequestedAt)
		}
		if state.Pending.LastSeenAt != "" {
			fmt.Fprintf(stdout, "  last seen: %s\n", state.Pending.LastSeenAt)
		}
	}
	if state.PairedChatID == "" && state.Pending == nil {
		fmt.Fprintln(stdout, "no pending pairing request")
	}
	return nil
}

func runPairApproveSubcommand(args []string, stdout, stderr io.Writer) error {
	dataDir, err := pairWorkspaceDataDir(args, stdout, stderr)
	if err != nil {
		return err
	}
	state, err := readTelegramPairingState(dataDir)
	if err != nil {
		return err
	}
	if state.Pending == nil || state.Pending.ChatID == "" || state.Pending.UserID == "" {
		return fmt.Errorf("no pending pairing request to approve")
	}

	state.PairedChatID = state.Pending.ChatID
	state.PairedUserID = state.Pending.UserID
	state.Pending = nil
	if err := writeTelegramPairingState(dataDir, state); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "approved telegram pairing to chat id %s\n", state.PairedChatID)
	return nil
}

func pairWorkspaceDataDir(args []string, _ io.Writer, _ io.Writer) (string, error) {
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspace := flags.String("workspace", "", "gateway workspace or data directory parent")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if len(flags.Args()) > 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	workspacePath := strings.TrimSpace(*workspace)
	if workspacePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if home = strings.TrimSpace(home); home == "" {
			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				return "", fmt.Errorf("resolve working directory: %w", cwdErr)
			}
			workspacePath = cwd
		} else {
			workspacePath = home
		}
	}
	if workspacePath = filepath.Clean(workspacePath); workspacePath == "" {
		return "", fmt.Errorf("invalid workspace path")
	}
	return resolveGatewayDataDir(workspacePath), nil
}
