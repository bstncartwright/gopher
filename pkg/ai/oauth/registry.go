package oauth

import (
	"errors"
	"sync"
	"time"
)

var (
	registryMu sync.RWMutex
	registry   = map[string]Provider{}
)

func RegisterProvider(provider Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[provider.ID()] = provider
}

func GetProvider(id string) (Provider, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	provider, ok := registry[id]
	return provider, ok
}

func GetProviders() []Provider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Provider, 0, len(registry))
	for _, provider := range registry {
		out = append(out, provider)
	}
	return out
}

func RefreshOAuthToken(providerID string, credentials Credentials) (Credentials, error) {
	provider, ok := GetProvider(providerID)
	if !ok {
		return Credentials{}, errors.New("unknown OAuth provider: " + providerID)
	}
	return provider.RefreshToken(credentials)
}

func GetOAuthAPIKey(providerID string, credentials map[string]Credentials) (Credentials, string, error) {
	provider, ok := GetProvider(providerID)
	if !ok {
		return Credentials{}, "", errors.New("unknown OAuth provider: " + providerID)
	}
	creds, ok := credentials[providerID]
	if !ok {
		return Credentials{}, "", nil
	}
	if time.Now().UnixMilli() >= creds.Expires {
		refreshed, err := provider.RefreshToken(creds)
		if err != nil {
			return Credentials{}, "", errors.New("failed to refresh OAuth token for " + providerID)
		}
		creds = refreshed
	}
	return creds, provider.GetAPIKey(creds), nil
}
