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
	policies.Network.BlockDomains = []string{"example.com"}
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

func TestWebSearchPolicyDeniedWhenRequiredHostsAreBlocked(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_search"}
	policies := defaultPolicies()
	policies.Network.BlockDomains = []string{"mcp.exa.ai", "mcp.tavily.com"}
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_search", map[string]any{"query": "latest ai news"})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected policy error when block_domains contains required hosts, got: %v", err)
	}
	if !strings.Contains(err.Error(), "block_domains") {
		t.Fatalf("expected block_domains error message, got: %v", err)
	}
}

func TestWebSearchPolicyAllowedWhenNoRequiredHostsAreBlocked(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_search"}
	policies := defaultPolicies()
	policies.Network.AllowDomains = []string{"example.com"}
	policies.Network.BlockDomains = []string{"blocked.example.com"}
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_search", map[string]any{"query": "latest ai news"})
	if err != nil {
		t.Fatalf("expected web_search policy allow when required hosts are not blocked, got: %v", err)
	}
}

func TestWebFetchPolicyDeniedWhenNetworkDisabled(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_fetch"}
	policies := defaultPolicies()
	policies.Network.Enabled = false
	policies.Network.BlockDomains = []string{"example.com"}
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_fetch", map[string]any{"url": "https://example.com"})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected policy error when network disabled, got: %v", err)
	}
	if !strings.Contains(err.Error(), "network.enabled=false") {
		t.Fatalf("expected network.enabled error message, got: %v", err)
	}
}

func TestWebFetchPolicyDeniedWhenRequiredHostsAreBlocked(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_fetch"}
	policies := defaultPolicies()
	policies.Network.BlockDomains = []string{"mcp.exa.ai", "mcp.tavily.com"}
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_fetch", map[string]any{"url": "https://example.com"})
	if err == nil || !IsPolicyError(err) {
		t.Fatalf("expected policy error when block_domains contains required hosts, got: %v", err)
	}
	if !strings.Contains(err.Error(), "block_domains") {
		t.Fatalf("expected block_domains error message, got: %v", err)
	}
}

func TestWebFetchPolicyAllowedWhenNoRequiredHostsAreBlocked(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"web_fetch"}
	policies := defaultPolicies()
	policies.Network.AllowDomains = []string{"example.com"}
	policies.Network.BlockDomains = []string{"blocked.example.com"}
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	_, err = NewToolRunner(agent).enforcePolicy("web_fetch", map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatalf("expected web_fetch policy allow when required hosts are not blocked, got: %v", err)
	}
}
