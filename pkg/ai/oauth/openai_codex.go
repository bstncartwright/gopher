package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	openAICodexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexAuthorize = "https://auth.openai.com/oauth/authorize"
	openAICodexToken     = "https://auth.openai.com/oauth/token"
	openAICodexRedirect  = "http://localhost:1455/auth/callback"
	openAICodexScope     = "openid profile email offline_access"
)

type OpenAICodexProvider struct{}

func (OpenAICodexProvider) ID() string                               { return "openai-codex" }
func (OpenAICodexProvider) Name() string                             { return "OpenAI Codex" }
func (OpenAICodexProvider) GetAPIKey(credentials Credentials) string { return credentials.Access }

func (p OpenAICodexProvider) Login(callbacks LoginCallbacks) (Credentials, error) {
	ctx := callbacks.Context
	if ctx == nil {
		ctx = context.Background()
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return Credentials{}, err
	}
	state, err := generateState()
	if err != nil {
		return Credentials{}, err
	}
	authURL := buildOpenAICodexAuthorizationURL(challenge, state)

	server := &oauthCodeServer{state: state}
	if err := server.Start(); err != nil && callbacks.OnProgress != nil {
		callbacks.OnProgress("Callback server unavailable, falling back to manual code input")
	}
	defer server.Close()

	if callbacks.OnAuth != nil {
		callbacks.OnAuth(AuthInfo{URL: authURL, Instructions: "Open this URL in your browser and complete login."})
	}

	code, err := waitForOpenAICodexCode(ctx, server, callbacks, state)
	if err != nil {
		return Credentials{}, err
	}
	return exchangeOpenAICodexCode(ctx, code, verifier)
}

func (OpenAICodexProvider) RefreshToken(credentials Credentials) (Credentials, error) {
	ctx := context.Background()
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", credentials.Refresh)
	form.Set("client_id", openAICodexClientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICodexToken, strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return Credentials{}, fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return Credentials{}, err
	}
	if tokenResponse.AccessToken == "" || tokenResponse.RefreshToken == "" {
		return Credentials{}, errors.New("refresh response missing access_token/refresh_token")
	}
	return Credentials{
		Access:  tokenResponse.AccessToken,
		Refresh: tokenResponse.RefreshToken,
		Expires: time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second).UnixMilli(),
	}, nil
}

func LoginOpenAICodex(callbacks LoginCallbacks) (Credentials, error) {
	return OpenAICodexProvider{}.Login(callbacks)
}

func RefreshOpenAICodexToken(credentials Credentials) (Credentials, error) {
	return OpenAICodexProvider{}.RefreshToken(credentials)
}

func buildOpenAICodexAuthorizationURL(challenge, state string) string {
	u, _ := url.Parse(openAICodexAuthorize)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", openAICodexClientID)
	q.Set("redirect_uri", openAICodexRedirect)
	q.Set("scope", openAICodexScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("id_token_add_organizations", "true")
	q.Set("codex_cli_simplified_flow", "true")
	q.Set("originator", "pi")
	u.RawQuery = q.Encode()
	return u.String()
}

func waitForOpenAICodexCode(ctx context.Context, server *oauthCodeServer, callbacks LoginCallbacks, state string) (string, error) {
	manualCh := make(chan string, 1)
	errCh := make(chan error, 1)
	if callbacks.OnManualCodeInput != nil {
		go func() {
			code, err := callbacks.OnManualCodeInput()
			if err != nil {
				errCh <- err
				return
			}
			manualCh <- code
		}()
	}

	serverCh := make(chan string, 1)
	go func() {
		if code := server.WaitForCode(ctx); code != "" {
			serverCh <- code
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err := <-errCh:
			return "", err
		case code := <-serverCh:
			return code, nil
		case input := <-manualCh:
			parsedCode, parsedState := parseAuthorizationInput(input)
			if parsedState != "" && parsedState != state {
				return "", errors.New("state mismatch")
			}
			if parsedCode == "" {
				return "", errors.New("missing authorization code")
			}
			return parsedCode, nil
		default:
			if callbacks.OnPrompt != nil {
				code, err := callbacks.OnPrompt(Prompt{Message: "Paste authorization code or callback URL", Placeholder: openAICodexRedirect})
				if err == nil && strings.TrimSpace(code) != "" {
					parsedCode, parsedState := parseAuthorizationInput(code)
					if parsedState != "" && parsedState != state {
						return "", errors.New("state mismatch")
					}
					if parsedCode != "" {
						return parsedCode, nil
					}
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
}

func exchangeOpenAICodexCode(ctx context.Context, code, verifier string) (Credentials, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", openAICodexClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", openAICodexRedirect)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICodexToken, strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Credentials{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return Credentials{}, fmt.Errorf("exchange failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var tokenResponse struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResponse); err != nil {
		return Credentials{}, err
	}
	if tokenResponse.AccessToken == "" || tokenResponse.RefreshToken == "" {
		return Credentials{}, errors.New("exchange response missing access_token/refresh_token")
	}
	return Credentials{
		Access:  tokenResponse.AccessToken,
		Refresh: tokenResponse.RefreshToken,
		Expires: time.Now().Add(time.Duration(tokenResponse.ExpiresIn) * time.Second).UnixMilli(),
	}, nil
}

func parseAuthorizationInput(input string) (code, state string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	if u, err := url.Parse(input); err == nil && u.Scheme != "" {
		q := u.Query()
		return q.Get("code"), q.Get("state")
	}
	if strings.Contains(input, "code=") {
		q, _ := url.ParseQuery(input)
		return q.Get("code"), q.Get("state")
	}
	if strings.Contains(input, "#") {
		parts := strings.SplitN(input, "#", 2)
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return input, ""
}

func generateState() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func init() {
	RegisterProvider(OpenAICodexProvider{})
}

// --- callback server helpers ---

type oauthCodeServer struct {
	state  string
	srv    *http.Server
	codeCh chan string
}

func (o *oauthCodeServer) Start() error {
	o.codeCh = make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != o.state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		select {
		case o.codeCh <- code:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><body><p>Authentication successful. Return to your terminal.</p></body></html>`))
	})
	o.srv = &http.Server{Addr: "127.0.0.1:1455", Handler: mux}
	go func() {
		_ = o.srv.ListenAndServe()
	}()
	return nil
}

func (o *oauthCodeServer) WaitForCode(ctx context.Context) string {
	if o == nil || o.codeCh == nil {
		return ""
	}
	select {
	case <-ctx.Done():
		return ""
	case code := <-o.codeCh:
		return code
	case <-time.After(60 * time.Second):
		return ""
	}
}

func (o *oauthCodeServer) Close() {
	if o == nil || o.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = o.srv.Shutdown(ctx)
}
