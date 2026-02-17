package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	failRestart bool
	calls       []string
}

func (r *fakeRunner) Run(ctx context.Context, command string, args ...string) error {
	_ = ctx
	call := command + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if r.failRestart && strings.Contains(call, "restart") {
		return fmt.Errorf("restart failed")
	}
	return nil
}

func TestApplyReleaseSuccess(t *testing.T) {
	assetBlob := []byte("new-binary-content")
	hash := sha256.Sum256(assetBlob)
	checksums := fmt.Sprintf("%s  gopher-linux-amd64.tar.gz\n", hex.EncodeToString(hash[:]))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/asset":
			_, _ = w.Write(assetBlob)
		case "/checksums":
			_, _ = w.Write([]byte(checksums))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "gopher")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write initial binary: %v", err)
	}
	runner := &fakeRunner{}
	err := ApplyRelease(context.Background(), ApplyOptions{
		BinaryPath:   binaryPath,
		ServiceName:  "gopher-gateway.service",
		Token:        "token",
		AssetURL:     server.URL + "/asset",
		AssetName:    "gopher-linux-amd64.tar.gz",
		ChecksumsURL: server.URL + "/checksums",
		Runner:       runner,
	})
	if err != nil {
		t.Fatalf("ApplyRelease() error: %v", err)
	}
	blob, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read updated binary: %v", err)
	}
	if string(blob) != "new-binary-content" {
		t.Fatalf("updated binary mismatch: %q", string(blob))
	}
}

func TestApplyReleaseRollbackOnRestartFailure(t *testing.T) {
	assetBlob := []byte("new-binary-content")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(assetBlob)
	}))
	defer server.Close()

	dir := t.TempDir()
	binaryPath := filepath.Join(dir, "gopher")
	if err := os.WriteFile(binaryPath, []byte("old"), 0o755); err != nil {
		t.Fatalf("write initial binary: %v", err)
	}
	runner := &fakeRunner{failRestart: true}
	err := ApplyRelease(context.Background(), ApplyOptions{
		BinaryPath:  binaryPath,
		ServiceName: "gopher-gateway.service",
		Token:       "token",
		AssetURL:    server.URL,
		AssetName:   "gopher-linux-amd64.tar.gz",
		Runner:      runner,
	})
	if err == nil {
		t.Fatalf("expected restart failure")
	}
	blob, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read binary after rollback: %v", err)
	}
	if string(blob) != "old" {
		t.Fatalf("rollback failed, binary = %q", string(blob))
	}
}
