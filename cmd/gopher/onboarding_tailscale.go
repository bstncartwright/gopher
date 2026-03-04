package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
)

var (
	onboardingExecLookPath      = exec.LookPath
	onboardingExecCommandOutput = commandOutput
	onboardingRandomRead        = rand.Read
	tailscaleHTTPSURLPattern    = regexp.MustCompile(`https://[^\s]+`)
)

func commandOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	blob, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(blob)), err
}

func resolveTelegramWebhookViaTailscale(out io.Writer, listenAddr, webhookPath string) (string, string, error) {
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = defaultTelegramWebhookListenAddr
	}
	webhookPath = normalizeWebhookPath(webhookPath)

	if _, err := onboardingExecLookPath("tailscale"); err != nil {
		return "", "", fmt.Errorf("tailscale is required for automatic webhook setup: %w", err)
	}

	statusOutput, err := onboardingExecCommandOutput("tailscale", "funnel", "status")
	baseURL := parseFirstHTTPSURL(statusOutput)
	if baseURL == "" {
		if err != nil {
			fmt.Fprintf(out, "tailscale funnel status returned an error, attempting automatic funnel creation: %s\n", strings.TrimSpace(statusOutput))
		}
		fmt.Fprintf(out, "no active tailscale funnel found; creating funnel on %s\n", listenAddr)
		createOutput, createErr := onboardingExecCommandOutput("tailscale", "funnel", "--bg", listenAddr)
		if createErr != nil {
			return "", "", formatCommandError("create tailscale funnel", createErr, createOutput)
		}
		statusOutput, err = onboardingExecCommandOutput("tailscale", "funnel", "status")
		if err != nil {
			return "", "", formatCommandError("inspect tailscale funnel status after creation", err, statusOutput)
		}
		baseURL = parseFirstHTTPSURL(statusOutput)
		if baseURL == "" {
			return "", "", fmt.Errorf("tailscale funnel created, but no public https url was found in `tailscale funnel status` output")
		}
	} else if err != nil {
		// Keep the discovered URL; status warnings should not block onboarding when URL is available.
		fmt.Fprintf(out, "tailscale funnel status warning: %s\n", strings.TrimSpace(statusOutput))
	}

	webhookURL, err := joinWebhookURL(baseURL, webhookPath)
	if err != nil {
		return "", "", err
	}
	secret, err := generateWebhookSecret()
	if err != nil {
		return "", "", fmt.Errorf("generate telegram webhook secret: %w", err)
	}

	fmt.Fprintf(out, "tailscale funnel webhook url: %s\n", webhookURL)
	fmt.Fprintln(out, "generated telegram webhook secret")
	return webhookURL, secret, nil
}

func normalizeWebhookPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return defaultTelegramWebhookPath
	}
	if !strings.HasPrefix(path, "/") {
		return "/" + path
	}
	return path
}

func parseFirstHTTPSURL(text string) string {
	candidates := tailscaleHTTPSURLPattern.FindAllString(text, -1)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(strings.TrimRight(candidate, ".,);]"))
		parsed, err := url.Parse(candidate)
		if err != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(parsed.Scheme), "https") {
			continue
		}
		if strings.TrimSpace(parsed.Host) == "" {
			continue
		}
		return strings.TrimRight(parsed.String(), "/")
	}
	return ""
}

func joinWebhookURL(baseURL, webhookPath string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("tailscale funnel url is invalid: %q", baseURL)
	}
	if !strings.EqualFold(strings.TrimSpace(parsed.Scheme), "https") {
		return "", fmt.Errorf("tailscale funnel url must use https: %q", baseURL)
	}
	return strings.TrimRight(baseURL, "/") + normalizeWebhookPath(webhookPath), nil
}

func generateWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := onboardingRandomRead(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func formatCommandError(action string, err error, output string) error {
	output = strings.TrimSpace(output)
	if output == "" {
		return fmt.Errorf("%s: %w", action, err)
	}
	return fmt.Errorf("%s: %w (%s)", action, err, output)
}
