package agentcore

import "testing"

func TestBuildRegistryGroupWebEnablesWebSearch(t *testing.T) {
	registry := buildRegistry([]string{"group:web"}, defaultPolicies())
	if _, ok := registry.Get("web_search"); !ok {
		t.Fatalf("expected web_search tool to be enabled")
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

func TestBuildRegistryGroupCollaborationEnablesHeartbeat(t *testing.T) {
	registry := buildRegistry([]string{"group:collaboration"}, defaultPolicies())
	if _, ok := registry.Get("heartbeat"); !ok {
		t.Fatalf("expected heartbeat tool to be enabled")
	}
}

func TestBuildRegistryGroupRuntimeEnablesGopherMeta(t *testing.T) {
	registry := buildRegistry([]string{"group:runtime"}, defaultPolicies())
	if _, ok := registry.Get("gopher_meta"); !ok {
		t.Fatalf("expected gopher_meta tool to be enabled")
	}
}
