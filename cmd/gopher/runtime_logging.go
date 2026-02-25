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

	prevSlog := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(dest, nil)))
	cleanup := func() {
		slog.SetDefault(prevSlog)
		_ = file.Close()
	}
	logger := log.New(dest, "", log.LstdFlags)
	logger.Printf("logging configured component=%s path=%s", name, logPath)
	return logger, cleanup, nil
}
