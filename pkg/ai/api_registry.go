package ai

import "sync"

type StreamFunc func(model Model, context Context, options *StreamOptions) *AssistantMessageEventStream

type StreamSimpleFunc func(model Model, context Context, options *SimpleStreamOptions) *AssistantMessageEventStream

type APIProvider struct {
	API          API
	Stream       StreamFunc
	StreamSimple StreamSimpleFunc
}

type registeredAPIProvider struct {
	Provider APIProvider
	SourceID string
}

var (
	apiProviderRegistryMu sync.RWMutex
	apiProviderRegistry   = map[API]registeredAPIProvider{}
)

func RegisterAPIProvider(provider APIProvider, sourceID string) {
	apiProviderRegistryMu.Lock()
	defer apiProviderRegistryMu.Unlock()
	apiProviderRegistry[provider.API] = registeredAPIProvider{Provider: provider, SourceID: sourceID}
}

func GetAPIProvider(api API) (APIProvider, bool) {
	apiProviderRegistryMu.RLock()
	defer apiProviderRegistryMu.RUnlock()
	entry, ok := apiProviderRegistry[api]
	if !ok {
		return APIProvider{}, false
	}
	return entry.Provider, true
}

func GetAPIProviders() []APIProvider {
	apiProviderRegistryMu.RLock()
	defer apiProviderRegistryMu.RUnlock()
	out := make([]APIProvider, 0, len(apiProviderRegistry))
	for _, entry := range apiProviderRegistry {
		out = append(out, entry.Provider)
	}
	return out
}

func UnregisterAPIProviders(sourceID string) {
	if sourceID == "" {
		return
	}
	apiProviderRegistryMu.Lock()
	defer apiProviderRegistryMu.Unlock()
	for api, entry := range apiProviderRegistry {
		if entry.SourceID == sourceID {
			delete(apiProviderRegistry, api)
		}
	}
}

func ClearAPIProviders() {
	apiProviderRegistryMu.Lock()
	defer apiProviderRegistryMu.Unlock()
	apiProviderRegistry = map[API]registeredAPIProvider{}
}
