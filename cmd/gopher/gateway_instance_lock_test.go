package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireGatewayInstanceLockBlocksSecondStart(t *testing.T) {
	workspace := t.TempDir()

	lock, err := acquireGatewayInstanceLock(workspace)
	if err != nil {
		t.Fatalf("acquireGatewayInstanceLock() error: %v", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Fatalf("Release() error: %v", releaseErr)
		}
	}()

	second, err := acquireGatewayInstanceLock(workspace)
	if err == nil {
		_ = second.Release()
		t.Fatalf("expected second lock acquisition to fail")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("unexpected lock error: %v", err)
	}
}

func TestAcquireGatewayInstanceLockRecoversFromStaleLock(t *testing.T) {
	workspace := t.TempDir()
	lockDir := filepath.Join(workspace, gatewayInstanceLockRelativePath, gatewayInstanceLockDirName)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatalf("create lock directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, gatewayInstanceOwnerFileName), []byte(`{"pid":9999999}`), 0o600); err != nil {
		t.Fatalf("write stale owner file: %v", err)
	}

	lock, err := acquireGatewayInstanceLock(workspace)
	if err != nil {
		t.Fatalf("acquireGatewayInstanceLock() error: %v", err)
	}
	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			t.Fatalf("Release() error: %v", releaseErr)
		}
	}()

	owner, err := readGatewayInstanceOwner(lockDir)
	if err != nil {
		t.Fatalf("readGatewayInstanceOwner() error: %v", err)
	}
	if owner.PID != os.Getpid() {
		t.Fatalf("owner pid = %d, want %d", owner.PID, os.Getpid())
	}
}

func TestAcquireGatewayInstanceLockRejectsCorruptOwnerFile(t *testing.T) {
	workspace := t.TempDir()
	lockDir := filepath.Join(workspace, gatewayInstanceLockRelativePath, gatewayInstanceLockDirName)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatalf("create lock directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, gatewayInstanceOwnerFileName), []byte("{"), 0o600); err != nil {
		t.Fatalf("write owner file: %v", err)
	}

	_, err := acquireGatewayInstanceLock(workspace)
	if err == nil {
		t.Fatalf("expected lock acquisition to fail for corrupt owner file")
	}
	if !strings.Contains(err.Error(), "owner metadata is unreadable") {
		t.Fatalf("unexpected error: %v", err)
	}
}
