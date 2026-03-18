package oauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRefreshGitHubCopilotToken(t *testing.T) {
	restore := githubCopilotOAuthHTTPClient
	t.Cleanup(func() {
		githubCopilotOAuthHTTPClient = restore
	})

	githubCopilotOAuthHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != githubCopilotTokenURL {
				t.Fatalf("unexpected url: %s", req.URL.String())
			}
			if got := req.Header.Get("Authorization"); got != "Bearer refresh-token" {
				t.Fatalf("authorization = %q", got)
			}
			return jsonResponse(http.StatusOK, `{"token":"tid=1;proxy-ep=proxy.individual.githubcopilot.com","expires_at":2000000000}`), nil
		}),
	}

	credentials, err := RefreshGitHubCopilotToken(Credentials{Refresh: "refresh-token"})
	if err != nil {
		t.Fatalf("RefreshGitHubCopilotToken() error: %v", err)
	}
	if credentials.Access == "" {
		t.Fatalf("expected access token")
	}
	if credentials.Refresh != "refresh-token" {
		t.Fatalf("refresh = %q", credentials.Refresh)
	}
	if credentials.Expires <= 0 {
		t.Fatalf("expected expiry timestamp")
	}
}

func TestLoginGitHubCopilot(t *testing.T) {
	restore := githubCopilotOAuthHTTPClient
	t.Cleanup(func() {
		githubCopilotOAuthHTTPClient = restore
	})

	polls := 0
	githubCopilotOAuthHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case githubDeviceCodeURL:
				return jsonResponse(http.StatusOK, `{"device_code":"device-code","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","interval":1,"expires_in":60}`), nil
			case githubAccessTokenURL:
				polls++
				if polls == 1 {
					return jsonResponse(http.StatusOK, `{"error":"authorization_pending"}`), nil
				}
				return jsonResponse(http.StatusOK, `{"access_token":"github-access-token"}`), nil
			case githubCopilotTokenURL:
				if got := req.Header.Get("Authorization"); got != "Bearer github-access-token" {
					t.Fatalf("authorization = %q", got)
				}
				return jsonResponse(http.StatusOK, `{"token":"tid=1;proxy-ep=proxy.individual.githubcopilot.com","expires_at":2000000000}`), nil
			default:
				t.Fatalf("unexpected url: %s", req.URL.String())
				return nil, nil
			}
		}),
	}

	authCalled := false
	progressCalled := false
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	credentials, err := LoginGitHubCopilot(LoginCallbacks{
		Context: ctx,
		OnAuth: func(info AuthInfo) {
			authCalled = true
			if info.URL != "https://github.com/login/device" {
				t.Fatalf("auth url = %q", info.URL)
			}
			if !strings.Contains(info.Instructions, "ABCD-EFGH") {
				t.Fatalf("instructions = %q", info.Instructions)
			}
		},
		OnProgress: func(message string) {
			if strings.TrimSpace(message) != "" {
				progressCalled = true
			}
		},
	})
	if err != nil {
		t.Fatalf("LoginGitHubCopilot() error: %v", err)
	}
	if !authCalled {
		t.Fatalf("expected auth callback")
	}
	if !progressCalled {
		t.Fatalf("expected progress callback")
	}
	if credentials.Refresh != "github-access-token" {
		t.Fatalf("refresh = %q, want github-access-token", credentials.Refresh)
	}
	if credentials.Access == "" {
		t.Fatalf("expected access token")
	}
}

func TestGitHubCopilotProviderRegistered(t *testing.T) {
	provider, ok := GetProvider("github-copilot")
	if !ok {
		t.Fatalf("expected github-copilot provider registration")
	}
	if provider.Name() != "GitHub Copilot" {
		t.Fatalf("name = %q", provider.Name())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}
