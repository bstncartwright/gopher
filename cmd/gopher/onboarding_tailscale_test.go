package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestResolveTelegramWebhookViaTailscaleUsesExistingFunnel(t *testing.T) {
	originalLookPath := onboardingExecLookPath
	originalCommandOutput := onboardingExecCommandOutput
	originalRandomRead := onboardingRandomRead
	t.Cleanup(func() {
		onboardingExecLookPath = originalLookPath
		onboardingExecCommandOutput = originalCommandOutput
		onboardingRandomRead = originalRandomRead
	})

	onboardingExecLookPath = func(file string) (string, error) {
		return "/usr/local/bin/tailscale", nil
	}
	onboardingExecCommandOutput = func(name string, args ...string) (string, error) {
		if name != "tailscale" {
			return "", fmt.Errorf("unexpected command %q", name)
		}
		if strings.Join(args, " ") != "funnel status" {
			return "", fmt.Errorf("unexpected args %v", args)
		}
		return "https://demo.ts.net", nil
	}
	onboardingRandomRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = byte(i)
		}
		return len(b), nil
	}

	var out bytes.Buffer
	webhookURL, secret, err := resolveTelegramWebhookViaTailscale(&out, "127.0.0.1:29330", "/_gopher/telegram/webhook")
	if err != nil {
		t.Fatalf("resolveTelegramWebhookViaTailscale() error: %v", err)
	}
	if webhookURL != "https://demo.ts.net/_gopher/telegram/webhook" {
		t.Fatalf("webhook url = %q", webhookURL)
	}
	if len(secret) != 64 {
		t.Fatalf("secret len = %d, want 64", len(secret))
	}
}

func TestResolveTelegramWebhookViaTailscaleCreatesFunnelWhenMissing(t *testing.T) {
	originalLookPath := onboardingExecLookPath
	originalCommandOutput := onboardingExecCommandOutput
	originalRandomRead := onboardingRandomRead
	t.Cleanup(func() {
		onboardingExecLookPath = originalLookPath
		onboardingExecCommandOutput = originalCommandOutput
		onboardingRandomRead = originalRandomRead
	})

	onboardingExecLookPath = func(file string) (string, error) {
		return "/usr/local/bin/tailscale", nil
	}
	var statusCalls int
	var createCalls int
	onboardingExecCommandOutput = func(name string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		switch call {
		case "funnel status":
			statusCalls++
			if statusCalls == 1 {
				return "No serve config", nil
			}
			return "https://newfunnel.ts.net", nil
		case "funnel --bg 127.0.0.1:29330":
			createCalls++
			return "created", nil
		default:
			return "", fmt.Errorf("unexpected args %v", args)
		}
	}
	onboardingRandomRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0xAA
		}
		return len(b), nil
	}

	var out bytes.Buffer
	webhookURL, _, err := resolveTelegramWebhookViaTailscale(&out, "127.0.0.1:29330", "/_gopher/telegram/webhook")
	if err != nil {
		t.Fatalf("resolveTelegramWebhookViaTailscale() error: %v", err)
	}
	if createCalls != 1 {
		t.Fatalf("create funnel calls = %d, want 1", createCalls)
	}
	if statusCalls != 2 {
		t.Fatalf("status calls = %d, want 2", statusCalls)
	}
	if webhookURL != "https://newfunnel.ts.net/_gopher/telegram/webhook" {
		t.Fatalf("webhook url = %q", webhookURL)
	}
}

func TestResolveTelegramWebhookViaTailscaleCreatesFunnelWhenStatusErrors(t *testing.T) {
	originalLookPath := onboardingExecLookPath
	originalCommandOutput := onboardingExecCommandOutput
	originalRandomRead := onboardingRandomRead
	t.Cleanup(func() {
		onboardingExecLookPath = originalLookPath
		onboardingExecCommandOutput = originalCommandOutput
		onboardingRandomRead = originalRandomRead
	})

	onboardingExecLookPath = func(file string) (string, error) {
		return "/usr/local/bin/tailscale", nil
	}
	var statusCalls int
	onboardingExecCommandOutput = func(name string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		switch call {
		case "funnel status":
			statusCalls++
			if statusCalls == 1 {
				return "no funnel configured", errors.New("exit status 1")
			}
			return "https://fallback.ts.net", nil
		case "funnel --bg 127.0.0.1:29330":
			return "created", nil
		default:
			return "", fmt.Errorf("unexpected args %v", args)
		}
	}
	onboardingRandomRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = byte(0x11)
		}
		return len(b), nil
	}

	var out bytes.Buffer
	webhookURL, _, err := resolveTelegramWebhookViaTailscale(&out, "127.0.0.1:29330", "/_gopher/telegram/webhook")
	if err != nil {
		t.Fatalf("resolveTelegramWebhookViaTailscale() error: %v", err)
	}
	if webhookURL != "https://fallback.ts.net/_gopher/telegram/webhook" {
		t.Fatalf("webhook url = %q", webhookURL)
	}
}

func TestResolveTelegramWebhookViaTailscaleFailsWhenTailscaleMissing(t *testing.T) {
	originalLookPath := onboardingExecLookPath
	t.Cleanup(func() {
		onboardingExecLookPath = originalLookPath
	})

	onboardingExecLookPath = func(file string) (string, error) {
		return "", errors.New("not found")
	}

	var out bytes.Buffer
	_, _, err := resolveTelegramWebhookViaTailscale(&out, "127.0.0.1:29330", "/_gopher/telegram/webhook")
	if err == nil {
		t.Fatalf("expected error when tailscale is missing")
	}
	if !strings.Contains(err.Error(), "tailscale is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseFirstHTTPSURL(t *testing.T) {
	t.Parallel()

	got := parseFirstHTTPSURL("funnel: https://demo.ts.net (ok)")
	if got != "https://demo.ts.net" {
		t.Fatalf("parseFirstHTTPSURL() = %q", got)
	}
}
