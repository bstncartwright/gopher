package main

import (
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const processLogLevelEnv = "GOPHER_LOG_LEVEL"

func setupProcessLogging(workingDir string, component string, stderr io.Writer) (*log.Logger, func(), error) {
	base := strings.TrimSpace(workingDir)
	if base == "" {
		return nil, nil, fmt.Errorf("working directory is required for logging")
	}
	name := strings.TrimSpace(component)
	if name == "" {
		return nil, nil, fmt.Errorf("component name is required for logging")
	}
	logDir := filepath.Join(base, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create log directory: %w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", name))
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file %s: %w", logPath, err)
	}

	dest := io.Writer(file)
	if stderr != nil {
		dest = io.MultiWriter(stderr, file)
	}
	level, invalidLevel, rawLevel := resolveProcessLogLevel()

	prevSlog := slog.Default()
	handler := slog.NewTextHandler(dest, &slog.HandlerOptions{
		Level:     level,
		AddSource: true,
	})
	slog.SetDefault(slog.New(handler).With("component", name))
	cleanup := func() {
		slog.SetDefault(prevSlog)
		_ = file.Close()
	}
	logger := log.New(dest, "", log.LstdFlags)
	logger.Printf("logging configured component=%s path=%s level=%s", name, logPath, level.String())
	if invalidLevel {
		logger.Printf("invalid %s=%q; using level=%s", processLogLevelEnv, rawLevel, level.String())
	}
	return logger, cleanup, nil
}

func resolveProcessLogLevel() (level slog.Level, invalid bool, raw string) {
	raw = strings.TrimSpace(os.Getenv(processLogLevelEnv))
	switch strings.ToLower(raw) {
	case "":
		return slog.LevelInfo, false, raw
	case "debug":
		return slog.LevelDebug, false, raw
	case "info":
		return slog.LevelInfo, false, raw
	case "warn", "warning":
		return slog.LevelWarn, false, raw
	case "error":
		return slog.LevelError, false, raw
	default:
		return slog.LevelInfo, true, raw
	}
}
