package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthSetListUnsetProvider(t *testing.T) {
	t.Parallel()

	envPath := filepath.Join(t.TempDir(), "gopher.env")
	var out bytes.Buffer

	if err := runAuthSubcommand([]string{
		"set",
		"--env-file", envPath,
		"--provider", "zai",
		"--api-key", "secret-123",
	}, &out, &out); err != nil {
		t.Fatalf("set provider failed: %v", err)
	}

	var listed bytes.Buffer
	if err := runAuthSubcommand([]string{
		"list",
		"--env-file", envPath,
	}, &listed, &listed); err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if !strings.Contains(listed.String(), "zai: configured") {
		t.Fatalf("expected zai configured in list output, got: %s", listed.String())
	}

	if err := runAuthSubcommand([]string{
		"unset",
		"--env-file", envPath,
		"--provider", "zai",
	}, &out, &out); err != nil {
		t.Fatalf("unset provider failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	if strings.Contains(string(data), "ZAI_API_KEY=") {
		t.Fatalf("expected ZAI_API_KEY to be removed, got: %s", string(data))
	}
}

func TestAuthSetRawKey(t *testing.T) {
	t.Parallel()

	envPath := filepath.Join(t.TempDir(), "gopher.env")
	var out bytes.Buffer
	if err := runAuthSubcommand([]string{
		"set",
		"--env-file", envPath,
		"--key", "CUSTOM_TOKEN",
		"--value", "abc",
	}, &out, &out); err != nil {
		t.Fatalf("set raw key failed: %v", err)
	}

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file failed: %v", err)
	}
	if !strings.Contains(string(data), "CUSTOM_TOKEN=abc") {
		t.Fatalf("expected CUSTOM_TOKEN to be set, got: %s", string(data))
	}
}
