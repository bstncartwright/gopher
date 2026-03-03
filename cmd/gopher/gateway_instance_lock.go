package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	gatewayInstanceLockDirName      = "gateway.lock"
	gatewayInstanceOwnerFileName    = "owner.json"
	gatewayInstanceLockRelativePath = ".gopher"
)

type gatewayInstanceLock struct {
	lockDir string
}

type gatewayInstanceOwner struct {
	PID       int    `json:"pid"`
	Hostname  string `json:"hostname,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

func acquireGatewayInstanceLock(workspace string) (*gatewayInstanceLock, error) {
	cleanWorkspace := strings.TrimSpace(workspace)
	if cleanWorkspace == "" {
		return nil, fmt.Errorf("acquire gateway instance lock: workspace is required")
	}
	lockDir := filepath.Join(cleanWorkspace, gatewayInstanceLockRelativePath, gatewayInstanceLockDirName)
	slog.Debug("gateway_lock: acquiring instance lock", "workspace", cleanWorkspace, "lock_dir", lockDir)

	for attempt := 0; attempt < 2; attempt++ {
		lock, err := createGatewayInstanceLock(lockDir)
		if err == nil {
			slog.Info("gateway_lock: lock acquired", "lock_dir", lockDir, "attempt", attempt+1)
			return lock, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}

		owner, readErr := readGatewayInstanceOwner(lockDir)
		if readErr != nil {
			return nil, fmt.Errorf(
				"gateway already appears to be running (lock at %s and owner metadata is unreadable: %w)",
				lockDir,
				readErr,
			)
		}
		if processIsRunning(owner.PID) {
			slog.Warn("gateway_lock: active owner detected", "lock_dir", lockDir, "owner_pid", owner.PID)
			return nil, fmt.Errorf(
				"gateway is already running for workspace %s (pid=%d, lock=%s)",
				cleanWorkspace,
				owner.PID,
				lockDir,
			)
		}
		if removeErr := os.RemoveAll(lockDir); removeErr != nil {
			return nil, fmt.Errorf(
				"gateway lock at %s appears stale (pid=%d) but could not be removed: %w",
				lockDir,
				owner.PID,
				removeErr,
			)
		}
		slog.Warn("gateway_lock: removed stale lock directory", "lock_dir", lockDir, "stale_pid", owner.PID)
	}

	return nil, fmt.Errorf("gateway is already running for workspace %s (lock=%s)", cleanWorkspace, lockDir)
}

func createGatewayInstanceLock(lockDir string) (*gatewayInstanceLock, error) {
	if err := os.MkdirAll(filepath.Dir(lockDir), 0o755); err != nil {
		return nil, fmt.Errorf("create gateway lock parent directory: %w", err)
	}
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		return nil, err
	}

	hostname, _ := os.Hostname()
	owner := gatewayInstanceOwner{
		PID:       os.Getpid(),
		Hostname:  strings.TrimSpace(hostname),
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeGatewayInstanceOwner(lockDir, owner); err != nil {
		_ = os.RemoveAll(lockDir)
		return nil, err
	}

	return &gatewayInstanceLock{lockDir: lockDir}, nil
}

func writeGatewayInstanceOwner(lockDir string, owner gatewayInstanceOwner) error {
	blob, err := json.Marshal(owner)
	if err != nil {
		return fmt.Errorf("encode gateway lock owner: %w", err)
	}
	ownerPath := filepath.Join(lockDir, gatewayInstanceOwnerFileName)
	if err := os.WriteFile(ownerPath, blob, 0o600); err != nil {
		return fmt.Errorf("write gateway lock owner file: %w", err)
	}
	return nil
}

func readGatewayInstanceOwner(lockDir string) (gatewayInstanceOwner, error) {
	ownerPath := filepath.Join(lockDir, gatewayInstanceOwnerFileName)
	blob, err := os.ReadFile(ownerPath)
	if err != nil {
		return gatewayInstanceOwner{}, fmt.Errorf("read %s: %w", ownerPath, err)
	}

	var owner gatewayInstanceOwner
	if err := json.Unmarshal(blob, &owner); err != nil {
		return gatewayInstanceOwner{}, fmt.Errorf("decode %s: %w", ownerPath, err)
	}
	if owner.PID <= 0 {
		return gatewayInstanceOwner{}, fmt.Errorf("invalid pid in %s", ownerPath)
	}
	return owner, nil
}

func processIsRunning(pid int) bool {
	return gatewayProcessIsRunning(pid)
}

func (l *gatewayInstanceLock) Release() error {
	if l == nil {
		return nil
	}
	if strings.TrimSpace(l.lockDir) == "" {
		return nil
	}
	if err := os.RemoveAll(l.lockDir); err != nil {
		return fmt.Errorf("release gateway instance lock: %w", err)
	}
	slog.Debug("gateway_lock: lock released", "lock_dir", l.lockDir)
	l.lockDir = ""
	return nil
}
