package main

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

const redactedArgValue = "[REDACTED]"

var sensitiveCLIFlags = map[string]struct{}{
	"--api-key":                 {},
	"--auth-api-key":            {},
	"--github-token":            {},
	"--telegram-bot-token":      {},
	"--telegram-webhook-secret": {},
	"--token":                   {},
	"--secret":                  {},
	"--value":                   {},
}

func setupCLIProcessLogging(_ io.Writer) func() {
	workingDir, err := os.Getwd()
	if err != nil {
		return nil
	}
	_, cleanup, err := setupProcessLogging(workingDir, "cli", nil)
	if err != nil {
		return nil
	}
	return cleanup
}

func startCommandLog(command string, args []string) func(error) {
	started := time.Now()
	sanitized := sanitizeCLIArgs(args)
	slog.Info(
		"cli: command started",
		"command", strings.TrimSpace(command),
		"args", strings.Join(sanitized, " "),
		"args_count", len(sanitized),
	)
	return func(err error) {
		duration := time.Since(started)
		if err != nil {
			slog.Error(
				"cli: command failed",
				"command", strings.TrimSpace(command),
				"duration", duration.String(),
				"error", err,
			)
			return
		}
		slog.Info(
			"cli: command completed",
			"command", strings.TrimSpace(command),
			"duration", duration.String(),
		)
	}
}

func sanitizeCLIArgs(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, len(args))
	maskNext := false
	for i := 0; i < len(args); i++ {
		raw := strings.TrimSpace(args[i])
		if maskNext {
			out[i] = redactedArgValue
			maskNext = false
			continue
		}
		flagName, _, hasValue := strings.Cut(raw, "=")
		flagName = strings.TrimSpace(flagName)
		if _, sensitive := sensitiveCLIFlags[flagName]; sensitive {
			if hasValue {
				out[i] = flagName + "=" + redactedArgValue
			} else {
				out[i] = raw
				maskNext = true
			}
			continue
		}
		out[i] = raw
	}
	return out
}
