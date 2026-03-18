package ai

import "github.com/bstncartwright/gopher/pkg/ai/oauth"

type OAuthCredentials = oauth.Credentials
type OAuthPrompt = oauth.Prompt
type OAuthAuthInfo = oauth.AuthInfo
type OAuthLoginCallbacks = oauth.LoginCallbacks
type OAuthProvider = oauth.Provider

func LoginOpenAICodex(callbacks OAuthLoginCallbacks) (OAuthCredentials, error) {
	return oauth.LoginOpenAICodex(callbacks)
}

func RefreshOpenAICodexToken(credentials OAuthCredentials) (OAuthCredentials, error) {
	return oauth.RefreshOpenAICodexToken(credentials)
}

func LoginGitHubCopilot(callbacks OAuthLoginCallbacks) (OAuthCredentials, error) {
	return oauth.LoginGitHubCopilot(callbacks)
}

func RefreshGitHubCopilotToken(credentials OAuthCredentials) (OAuthCredentials, error) {
	return oauth.RefreshGitHubCopilotToken(credentials)
}

func RegisterOAuthProvider(provider OAuthProvider) {
	oauth.RegisterProvider(provider)
}

func GetOAuthProvider(id string) (OAuthProvider, bool) {
	return oauth.GetProvider(id)
}

func GetOAuthProviders() []OAuthProvider {
	return oauth.GetProviders()
}

func RefreshOAuthToken(providerID string, credentials OAuthCredentials) (OAuthCredentials, error) {
	return oauth.RefreshOAuthToken(providerID, credentials)
}

func GetOAuthAPIKey(providerID string, credentials map[string]OAuthCredentials) (OAuthCredentials, string, error) {
	return oauth.GetOAuthAPIKey(providerID, credentials)
}
