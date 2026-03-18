package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	githubCopilotOAuthClientID = "Iv1.b507a08c87ecfe98"
	githubDeviceCodeURL        = "https://github.com/login/device/code"
	githubAccessTokenURL       = "https://github.com/login/oauth/access_token"
	githubCopilotTokenURL      = "https://api.github.com/copilot_internal/v2/token"
	githubCopilotUserAgent     = "GitHubCopilotChat/0.35.0"
	githubCopilotEditorVersion = "vscode/1.107.0"
	githubCopilotPluginVersion = "copilot-chat/0.35.0"
	githubCopilotIntegrationID = "vscode-chat"
)

var githubCopilotOAuthHTTPClient = http.DefaultClient

type GitHubCopilotProvider struct{}

func (GitHubCopilotProvider) ID() string                               { return "github-copilot" }
func (GitHubCopilotProvider) Name() string                             { return "GitHub Copilot" }
func (GitHubCopilotProvider) GetAPIKey(credentials Credentials) string { return credentials.Access }

func (p GitHubCopilotProvider) Login(callbacks LoginCallbacks) (Credentials, error) {
	ctx := callbacks.Context
	if ctx == nil {
		ctx = context.Background()
	}
	slog.Info("github_copilot_oauth: starting login")

	device, err := startGitHubCopilotDeviceFlow(ctx)
	if err != nil {
		slog.Error("github_copilot_oauth: failed to start device flow", "error", err)
		return Credentials{}, err
	}
	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{
			URL:          device.VerificationURI,
			Instructions: fmt.Sprintf("Enter code: %s", device.UserCode),
		})
	}
	if callbacks.OnProgress != nil {
		callbacks.OnProgress("Waiting for GitHub Copilot authorization")
	}

	githubToken, err := pollGitHubCopilotAccessToken(ctx, device)
	if err != nil {
		slog.Error("github_copilot_oauth: failed to obtain GitHub access token", "error", err)
		return Credentials{}, err
	}
	slog.Info("github_copilot_oauth: GitHub access token received")

	credentials, err := p.RefreshToken(Credentials{Refresh: githubToken})
	if err != nil {
		slog.Error("github_copilot_oauth: failed to exchange Copilot token", "error", err)
		return Credentials{}, err
	}
	return credentials, nil
}

func (GitHubCopilotProvider) RefreshToken(credentials Credentials) (Credentials, error) {
	refresh := strings.TrimSpace(credentials.Refresh)
	if refresh == "" {
		return Credentials{}, errors.New("github copilot refresh token is required")
	}
	slog.Info("github_copilot_oauth: refreshing token")

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, githubCopilotTokenURL, nil)
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+refresh)
	for key, value := range copilotOAuthHeaders() {
		req.Header.Set(key, value)
	}

	resp, err := githubCopilotOAuthHTTPClient.Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return Credentials{}, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Credentials{}, err
	}
	if strings.TrimSpace(payload.Token) == "" || payload.ExpiresAt == 0 {
		return Credentials{}, errors.New("refresh response missing token/expires_at")
	}

	return Credentials{
		Access:  strings.TrimSpace(payload.Token),
		Refresh: refresh,
		Expires: (payload.ExpiresAt * 1000) - int64((5*time.Minute)/time.Millisecond),
	}, nil
}

func LoginGitHubCopilot(callbacks LoginCallbacks) (Credentials, error) {
	return GitHubCopilotProvider{}.Login(callbacks)
}

func RefreshGitHubCopilotToken(credentials Credentials) (Credentials, error) {
	return GitHubCopilotProvider{}.RefreshToken(credentials)
}

type githubCopilotDeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	ExpiresIn       int    `json:"expires_in"`
}

func startGitHubCopilotDeviceFlow(ctx context.Context) (githubCopilotDeviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", githubCopilotOAuthClientID)
	form.Set("scope", "read:user")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return githubCopilotDeviceCodeResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", githubCopilotUserAgent)

	resp, err := githubCopilotOAuthHTTPClient.Do(req)
	if err != nil {
		return githubCopilotDeviceCodeResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return githubCopilotDeviceCodeResponse{}, fmt.Errorf("device code request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var device githubCopilotDeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return githubCopilotDeviceCodeResponse{}, err
	}
	if strings.TrimSpace(device.DeviceCode) == "" || strings.TrimSpace(device.UserCode) == "" || strings.TrimSpace(device.VerificationURI) == "" {
		return githubCopilotDeviceCodeResponse{}, errors.New("device code response missing fields")
	}
	if device.Interval <= 0 {
		device.Interval = 5
	}
	return device, nil
}

func pollGitHubCopilotAccessToken(ctx context.Context, device githubCopilotDeviceCodeResponse) (string, error) {
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}

		token, nextInterval, done, err := requestGitHubCopilotAccessToken(ctx, device.DeviceCode)
		if err != nil {
			return "", err
		}
		if done {
			return token, nil
		}
		if time.Now().After(deadline) {
			return "", errors.New("device flow timed out")
		}
		if nextInterval > 0 {
			interval = nextInterval
		}
		timer.Reset(interval)
	}
}

func requestGitHubCopilotAccessToken(ctx context.Context, deviceCode string) (token string, nextInterval time.Duration, done bool, err error) {
	form := url.Values{}
	form.Set("client_id", githubCopilotOAuthClientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", githubCopilotUserAgent)

	resp, err := githubCopilotOAuthHTTPClient.Do(req)
	if err != nil {
		return "", 0, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return "", 0, false, fmt.Errorf("device token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var payload struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
		Interval         int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", 0, false, err
	}
	if strings.TrimSpace(payload.AccessToken) != "" {
		return strings.TrimSpace(payload.AccessToken), 0, true, nil
	}

	switch strings.TrimSpace(payload.Error) {
	case "authorization_pending":
		return "", 0, false, nil
	case "slow_down":
		wait := 10 * time.Second
		if payload.Interval > 0 {
			wait = time.Duration(payload.Interval) * time.Second
		}
		return "", wait, false, nil
	case "":
		return "", 0, false, errors.New("device token response missing access_token")
	default:
		detail := strings.TrimSpace(payload.ErrorDescription)
		if detail != "" {
			return "", 0, false, fmt.Errorf("device flow failed: %s: %s", payload.Error, detail)
		}
		return "", 0, false, fmt.Errorf("device flow failed: %s", payload.Error)
	}
}

func copilotOAuthHeaders() map[string]string {
	return map[string]string{
		"User-Agent":             githubCopilotUserAgent,
		"Editor-Version":         githubCopilotEditorVersion,
		"Editor-Plugin-Version":  githubCopilotPluginVersion,
		"Copilot-Integration-Id": githubCopilotIntegrationID,
	}
}

func init() {
	RegisterProvider(GitHubCopilotProvider{})
}
