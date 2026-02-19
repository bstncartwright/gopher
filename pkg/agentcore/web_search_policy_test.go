package agentcore

import (
	"strings"
	"testing"
)

func TestWebSearchPolicyDeniedWhenNetworkDisabled(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_search"}
	policies := defaultPolicies()
	policies.Network.Enabled = false
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_search", map[string]any{"query": "latest ai news"})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected policy error when network disabled, got: %v", err)
	}
	if !strings.Contains(err.Error(), "network.enabled=false") {
		t.Fatalf("expected network.enabled error message, got: %v", err)
	}
}

func TestWebSearchPolicyDeniedWhenAllowDomainsExcludeAPIZAI(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_search"}
	policies := defaultPolicies()
	policies.Network.AllowDomains = []string{"example.com"}
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_search", map[string]any{"query": "latest ai news"})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected policy error when allow_domains excludes api.z.ai, got: %v", err)
	}
	if !strings.Contains(err.Error(), "allow_domains") {
		t.Fatalf("expected allow_domains error message, got: %v", err)
	}
}

func TestWebSearchPolicyAllowedWithWildcardDomain(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_search"}
	policies := defaultPolicies()
	policies.Network.AllowDomains = []string{"*"}
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_search", map[string]any{"query": "latest ai news"})
	if err != nil {
		t.Fatalf("expected web_search policy allow with wildcard domain, got: %v", err)
	}
}
