package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type CommandRunner interface {
	Run(ctx context.Context, command string, args ...string) error
}

type ApplyOptions struct {
	BinaryPath   string
	ServiceName  string
	Token        string
	AssetURL     string
	AssetName    string
	ChecksumsURL string
	HTTPClient   *http.Client
	Runner       CommandRunner
}

func ApplyRelease(ctx context.Context, opts ApplyOptions) error {
	binaryPath := strings.TrimSpace(opts.BinaryPath)
	if binaryPath == "" {
		return fmt.Errorf("binary path is required")
	}
	if strings.TrimSpace(opts.Token) == "" {
		return fmt.Errorf("github token is required")
	}
	if strings.TrimSpace(opts.AssetURL) == "" {
		return fmt.Errorf("asset url is required")
	}
	assetName := strings.TrimSpace(opts.AssetName)
	if assetName == "" {
		assetName = filepath.Base(strings.TrimSpace(opts.AssetURL))
	}
	runner := opts.Runner
	if runner == nil {
		return fmt.Errorf("command runner is required")
	}

	blob, err := DownloadWithToken(ctx, opts.HTTPClient, opts.AssetURL, opts.Token)
	if err != nil {
		return err
	}
	if len(blob) == 0 {
		return fmt.Errorf("downloaded asset is empty")
	}
	if strings.TrimSpace(opts.ChecksumsURL) != "" {
		checksumBlob, err := DownloadWithToken(ctx, opts.HTTPClient, opts.ChecksumsURL, opts.Token)
		if err != nil {
			return err
		}
		if err := verifyChecksums(checksumBlob, assetName, blob); err != nil {
			return err
		}
	}

	dir := filepath.Dir(binaryPath)
	tmpPath := filepath.Join(dir, ".gopher.update.tmp")
	backupPath := binaryPath + ".bak"
	if err := os.WriteFile(tmpPath, blob, 0o755); err != nil {
		return fmt.Errorf("write temporary update binary: %w", err)
	}

	if _, err := os.Stat(binaryPath); err == nil {
		if err := os.Rename(binaryPath, backupPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("backup existing binary: %w", err)
		}
	}
	if err := os.Rename(tmpPath, binaryPath); err != nil {
		_ = os.Rename(backupPath, binaryPath)
		_ = os.Remove(tmpPath)
		return fmt.Errorf("swap updated binary: %w", err)
	}

	serviceName := strings.TrimSpace(opts.ServiceName)
	if serviceName != "" {
		if err := runner.Run(ctx, "systemctl", "restart", serviceName); err != nil {
			_ = rollbackBinary(binaryPath, backupPath)
			return fmt.Errorf("restart service after update: %w", err)
		}
		if err := runner.Run(ctx, "systemctl", "is-active", "--quiet", serviceName); err != nil {
			_ = rollbackBinary(binaryPath, backupPath)
			return fmt.Errorf("service health check failed after update: %w", err)
		}
	}
	return nil
}

func verifyChecksums(checksumBlob []byte, assetName string, blob []byte) error {
	expected := ""
	for _, line := range strings.Split(string(checksumBlob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.Contains(line, assetName) {
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				expected = parts[0]
				break
			}
		}
	}
	if expected == "" {
		return fmt.Errorf("checksum for %q not found", assetName)
	}
	hash := sha256.Sum256(blob)
	actual := hex.EncodeToString(hash[:])
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("checksum mismatch for %q", assetName)
	}
	return nil
}

func rollbackBinary(binaryPath, backupPath string) error {
	_ = os.Remove(binaryPath)
	if _, err := os.Stat(backupPath); err == nil {
		if err := os.Rename(backupPath, binaryPath); err != nil {
			return err
		}
	}
	return nil
}
