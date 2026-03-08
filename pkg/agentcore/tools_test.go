package agentcore

import (
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestBuildRegistryGroupWebEnablesWebTools(t *testing.T) {
	registry := buildRegistry([]string{"group:web"}, defaultPolicies())
	if _, ok := registry.Get("web_search"); !ok {
		t.Fatalf("expected web_search tool to be enabled")
	}
	if _, ok := registry.Get("web_fetch"); !ok {
		t.Fatalf("expected web_fetch tool to be enabled")
	}
}

func TestBuildRegistryWebSearchAliasesAreDeduped(t *testing.T) {
	registry := buildRegistry([]string{"search", "search_mcp", "web_search"}, defaultPolicies())
	schemas := registry.Schemas()
	if len(schemas) != 1 {
		t.Fatalf("schemas len = %d, want 1", len(schemas))
	}
	if schemas[0].Name != "web_search" {
		t.Fatalf("schema[0].Name = %q, want web_search", schemas[0].Name)
	}
}

func TestBuildRegistryWebFetchAliasesAreDeduped(t *testing.T) {
	registry := buildRegistry([]string{"fetch", "fetch_mcp", "fetch_content", "web_fetch"}, defaultPolicies())
	schemas := registry.Schemas()
	if len(schemas) != 1 {
		t.Fatalf("schemas len = %d, want 1", len(schemas))
	}
	if schemas[0].Name != "web_fetch" {
		t.Fatalf("schema[0].Name = %q, want web_fetch", schemas[0].Name)
	}
}

func TestBuildRegistryGroupCollaborationEnablesHeartbeat(t *testing.T) {
	registry := buildRegistry([]string{"group:collaboration"}, defaultPolicies())
	if _, ok := registry.Get("delegate_targets"); !ok {
		t.Fatalf("expected delegate_targets tool to be enabled")
	}
	if _, ok := registry.Get("heartbeat"); !ok {
		t.Fatalf("expected heartbeat tool to be enabled")
	}
	if _, ok := registry.Get("message"); !ok {
		t.Fatalf("expected message tool to be enabled")
	}
	if _, ok := registry.Get("reaction"); !ok {
		t.Fatalf("expected reaction tool to be enabled")
	}
}

func TestBuildRegistryGroupRuntimeEnablesGopherMeta(t *testing.T) {
	registry := buildRegistry([]string{"group:runtime"}, defaultPolicies())
	if _, ok := registry.Get("gopher_meta"); !ok {
		t.Fatalf("expected gopher_meta tool to be enabled")
	}
	if _, ok := registry.Get("gopher_update"); !ok {
		t.Fatalf("expected gopher_update tool to be enabled")
	}
}

func TestBuildProviderAIToolsPrefersHostedWebSearchForSupportedProviders(t *testing.T) {
	registry := buildRegistry([]string{"group:web"}, defaultPolicies())
	tools := buildProviderAITools(registry, ai.Model{
		API:      ai.APIOpenAIResponses,
		Provider: ai.ProviderOpenAI,
	}, defaultConfig(), defaultPolicies(), false)

	if len(tools) == 0 || !tools[0].IsHostedWebSearch() {
		t.Fatalf("expected hosted web_search tool, got %#v", tools)
	}
	if tools[0].ExternalWebAccess == nil || *tools[0].ExternalWebAccess {
		t.Fatalf("expected hosted web_search to default to cached mode")
	}
}

func TestBuildProviderAIToolsFallsBackToMCPWhenHostedSearchDisabled(t *testing.T) {
	config := defaultConfig()
	config.NativeWebSearchMode = string(NativeWebSearchModeDisabled)
	registry := buildRegistry([]string{"group:web"}, defaultPolicies())
	tools := buildProviderAITools(registry, ai.Model{
		API:      ai.APIOpenAIResponses,
		Provider: ai.ProviderOpenAI,
	}, config, defaultPolicies(), false)

	if len(tools) == 0 || tools[0].IsHostedWebSearch() {
		t.Fatalf("expected MCP web_search tool, got %#v", tools)
	}
}

func TestBuildProviderAIToolsOmitsWebSearchWhenNetworkDisabled(t *testing.T) {
	policies := defaultPolicies()
	policies.Network.Enabled = false
	registry := buildRegistry([]string{"group:web"}, policies)
	tools := buildProviderAITools(registry, ai.Model{
		API:      ai.APIOpenAIResponses,
		Provider: ai.ProviderOpenAI,
	}, defaultConfig(), policies, false)

	for _, tool := range tools {
		if tool.Name == "web_search" {
			t.Fatalf("expected web_search to be omitted when network is disabled")
		}
	}
}
