package agentcore

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestLoadAgentMissingRequiredFiles(t *testing.T) {
	required := []struct {
		name   string
		needle string
	}{
		{name: "config.json", needle: "config"},
	}
	for _, tc := range required {
		t.Run(tc.name, func(t *testing.T) {
			workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
			if err := os.Remove(filepath.Join(workspace, tc.name)); err != nil {
				t.Fatalf("remove %s: %v", tc.name, err)
			}

			_, err := LoadAgent(workspace)
			if err == nil {
				t.Fatalf("expected error for missing %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.needle) {
				t.Fatalf("expected error to mention %s, got: %v", tc.needle, err)
			}
		})
	}
}

func TestLoadAgentSupportsPoliciesEmbeddedInConfigWithoutLegacyPoliciesFile(t *testing.T) {
	config := defaultConfig()
	policies := defaultPolicies()
	config.Policies = &policies
	workspace := createTestWorkspace(t, config, defaultPolicies())
	if err := os.Remove(filepath.Join(workspace, "policies.json")); err != nil {
		t.Fatalf("remove policies.json: %v", err)
	}

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !reflect.DeepEqual(agent.Policies.FSRoots, []string{"./"}) {
		t.Fatalf("fs_roots = %#v, want [\"./\"]", agent.Policies.FSRoots)
	}
	if !agent.Policies.CanShell {
		t.Fatalf("can_shell = false, want true")
	}
}

func TestLoadAgentEmbeddedPoliciesAllowCrossAgentWithoutFSRootsDefaultsOpen(t *testing.T) {
	config := defaultConfig()
	policies := defaultPolicies()
	policies.FSRoots = nil
	policies.AllowCrossAgentFS = true
	config.Policies = &policies
	workspace := createTestWorkspace(t, config, defaultPolicies())
	if err := os.Remove(filepath.Join(workspace, "policies.json")); err != nil {
		t.Fatalf("remove policies.json: %v", err)
	}

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if len(agent.allowedFSRoots) == 0 {
		t.Fatalf("expected default allowed fs roots")
	}
	root := filesystemRootForWorkspace(workspace)
	if !reflect.DeepEqual(agent.allowedFSRoots, []string{root}) {
		t.Fatalf("allowed fs roots = %#v, want [%q]", agent.allowedFSRoots, root)
	}
}

func TestLoadAgentMissingPoliciesInConfigAndLegacyFileUsesOpenDefaults(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	if err := os.Remove(filepath.Join(workspace, "policies.json")); err != nil {
		t.Fatalf("remove policies.json: %v", err)
	}

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !agent.Policies.AllowCrossAgentFS {
		t.Fatalf("allow_cross_agent_fs = false, want true")
	}
	if !agent.Policies.CanShell {
		t.Fatalf("can_shell = false, want true")
	}
	if !agent.Policies.Network.Enabled {
		t.Fatalf("network.enabled = false, want true")
	}
	if len(agent.Policies.FSRoots) == 0 {
		t.Fatalf("expected default open fs root")
	}
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(workspace); volume != "" {
		root = volume + string(filepath.Separator)
	}
	if !reflect.DeepEqual(agent.Policies.FSRoots, []string{root}) {
		t.Fatalf("fs_roots = %#v, want [%q]", agent.Policies.FSRoots, root)
	}
}

func TestLoadAgentPrefersEmbeddedPoliciesOverLegacyPoliciesFile(t *testing.T) {
	config := defaultConfig()
	policies := defaultPolicies()
	policies.CanShell = false
	policies.ShellAllowlist = []string{"echo"}
	config.Policies = &policies
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Policies.CanShell {
		t.Fatalf("expected embedded config policies to override legacy policies file")
	}
	if !reflect.DeepEqual(agent.Policies.ShellAllowlist, []string{"echo"}) {
		t.Fatalf("shell_allowlist = %#v, want [\"echo\"]", agent.Policies.ShellAllowlist)
	}
}

func TestLoadAgentDefaultsLegacyMissingNetworkPolicy(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "policies.json"), `{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  "can_shell": true,
  "shell_allowlist": ["echo", "git", "go", "bun", "node", "bash", "gopher"],
  "budget": { "max_tokens_per_session": 200000 }
}`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !agent.Policies.Network.Enabled {
		t.Fatalf("expected network to default enabled for legacy policies without network stanza")
	}
	if len(agent.Policies.Network.AllowDomains) != 0 {
		t.Fatalf("allow_domains = %#v, want empty/unset allowlist", agent.Policies.Network.AllowDomains)
	}
}

func TestLoadAgentAppliesSafeguardContextManagementDefaults(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	cm := agent.Config.ContextManagement
	if cm.ModeValue() != "safeguard" {
		t.Fatalf("context_management.mode = %q, want safeguard", cm.ModeValue())
	}
	if cm.OverflowRetryLimitValue() != 3 {
		t.Fatalf("overflow_retry_limit = %d, want 3", cm.OverflowRetryLimitValue())
	}
	if cm.ReserveMinTokensValue() != 20000 {
		t.Fatalf("reserve_min_tokens = %d, want 20000", cm.ReserveMinTokensValue())
	}
	if !cm.ModelCompactionSummaryEnabled() {
		t.Fatalf("expected model_compaction_summary default enabled")
	}
}

func TestLoadAgentRejectsRemovedContextManagementKeysJSON(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "config.json"), `{
  "agent_id": "agent-test",
  "name": "Test Agent",
  "role": "coder",
  "model_policy": "openai:gpt-4o-mini",
  "enabled_tools": ["group:fs", "group:runtime"],
  "context_management": {
    "mode": "safeguard",
    "tool_result_context_max_chars": 12000,
    "recent_tool_result_chars": 2400
  }
}`)

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected config validation error for removed context_management keys")
	}
	if !strings.Contains(err.Error(), "tool_result_context_max_chars") {
		t.Fatalf("expected error to list removed key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "recent_tool_result_chars") {
		t.Fatalf("expected error to list removed key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "token-budget compaction") {
		t.Fatalf("expected migration guidance in error, got: %v", err)
	}
}

func TestLoadAgentRejectsRemovedContextManagementKeysTOML(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	if err := os.Remove(filepath.Join(workspace, "config.json")); err != nil {
		t.Fatalf("remove config.json: %v", err)
	}
	mustWriteFile(t, filepath.Join(workspace, "config.toml"), `agent_id = "agent-test"
name = "Test Agent"
role = "coder"
model_policy = "openai:gpt-4o-mini"
enabled_tools = ["group:fs", "group:runtime"]

[context_management]
mode = "safeguard"
tool_result_context_head_chars = 8000
historical_tool_result_chars = 240
`)

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected config validation error for removed context_management keys")
	}
	if !strings.Contains(err.Error(), "tool_result_context_head_chars") {
		t.Fatalf("expected error to list removed key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "historical_tool_result_chars") {
		t.Fatalf("expected error to list removed key, got: %v", err)
	}
}

func TestLoadAgentRejectsInvalidNativeWebSearchMode(t *testing.T) {
	config := defaultConfig()
	config.NativeWebSearchMode = "always"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected invalid native_web_search_mode error")
	}
	if !strings.Contains(err.Error(), "native_web_search_mode") {
		t.Fatalf("expected native_web_search_mode in error, got: %v", err)
	}
}

func TestAgentConfigNativeWebSearchModeDefaultsToCachedForSupportedProviders(t *testing.T) {
	mode := defaultConfig().NativeWebSearchModeValue(ai.Model{
		API:      ai.APIOpenAIResponses,
		Provider: ai.ProviderOpenAI,
	})
	if mode != NativeWebSearchModeCached {
		t.Fatalf("mode = %q, want cached", mode)
	}
}

func TestLoadAgentParsesProviderOptionsFromTOML(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	if err := os.Remove(filepath.Join(workspace, "config.json")); err != nil {
		t.Fatalf("remove config.json: %v", err)
	}
	mustWriteFile(t, filepath.Join(workspace, "config.toml"), `agent_id = "agent-test"
name = "Test Agent"
role = "coder"
model_policy = "openai-codex:gpt-5.3-codex"
reasoning_level = "medium"
enabled_tools = ["group:fs", "group:runtime"]

[provider_options]
service_tier = "fast"
`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if got := agent.Config.ProviderOptions["service_tier"]; got != "fast" {
		t.Fatalf("provider_options.service_tier = %#v, want %q", got, "fast")
	}
}

func TestLoadAgentDefaultsLegacyMissingShellPolicy(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "policies.json"), `{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  "network": { "enabled": true, "allow_domains": ["*"] },
  "budget": { "max_tokens_per_session": 200000 }
}`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !agent.Policies.CanShell {
		t.Fatalf("expected shell to default enabled when can_shell is omitted")
	}
	if len(agent.Policies.ShellAllowlist) != 0 {
		t.Fatalf("shell_allowlist = %#v, want empty/unset allowlist", agent.Policies.ShellAllowlist)
	}
}

func TestLoadAgentDefaultsNetworkAllowDomainsWhenEnabledOmitted(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "policies.json"), `{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  "can_shell": true,
  "shell_allowlist": ["echo", "git", "go", "bun", "node", "bash", "gopher"],
  "network": { "enabled": true },
  "budget": { "max_tokens_per_session": 200000 }
}`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if len(agent.Policies.Network.AllowDomains) != 0 {
		t.Fatalf("allow_domains = %#v, want empty/unset allowlist", agent.Policies.Network.AllowDomains)
	}
}

func TestLoadAgentDefaultsLegacyDisabledNetworkWithoutDomainRestrictions(t *testing.T) {
	tests := []struct {
		name          string
		networkPolicy string
	}{
		{
			name:          "enabled false with no allow_domains",
			networkPolicy: `"network": { "enabled": false }`,
		},
		{
			name:          "enabled false with wildcard allow_domains",
			networkPolicy: `"network": { "enabled": false, "allow_domains": ["*"] }`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
			mustWriteFile(t, filepath.Join(workspace, "policies.json"), `{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  "can_shell": true,
  "shell_allowlist": ["echo", "git", "go", "bun", "node", "bash", "gopher"],
  `+tc.networkPolicy+`,
  "budget": { "max_tokens_per_session": 200000 }
}`)

			agent, err := LoadAgent(workspace)
			if err != nil {
				t.Fatalf("LoadAgent() error: %v", err)
			}
			if !agent.Policies.Network.Enabled {
				t.Fatalf("expected legacy unrestricted network policy to default enabled")
			}
			if len(agent.Policies.Network.AllowDomains) != 0 {
				t.Fatalf("allow_domains = %#v, want empty/unset allowlist", agent.Policies.Network.AllowDomains)
			}
		})
	}
}

func TestLoadAgentKeepsExplicitDisabledNetworkWithRestrictedDomains(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "policies.json"), `{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  "can_shell": true,
  "shell_allowlist": ["echo", "git", "go", "bun", "node", "bash", "gopher"],
  "network": { "enabled": false, "allow_domains": ["example.com"] },
  "budget": { "max_tokens_per_session": 200000 }
}`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Policies.Network.Enabled {
		t.Fatalf("expected explicit restricted network policy to remain disabled")
	}
	if !reflect.DeepEqual(agent.Policies.Network.AllowDomains, []string{"example.com"}) {
		t.Fatalf("allow_domains = %#v, want [\"example.com\"]", agent.Policies.Network.AllowDomains)
	}
}

func TestLoadAgentDefaultsLegacyDisabledShellWithoutRestrictions(t *testing.T) {
	tests := []struct {
		name        string
		shellPolicy string
	}{
		{
			name:        "can_shell false with no shell_allowlist",
			shellPolicy: `"can_shell": false`,
		},
		{
			name:        "can_shell false with default shell_allowlist",
			shellPolicy: `"can_shell": false, "shell_allowlist": []`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
			mustWriteFile(t, filepath.Join(workspace, "policies.json"), `{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  `+tc.shellPolicy+`,
  "network": { "enabled": true, "allow_domains": ["*"] },
  "budget": { "max_tokens_per_session": 200000 }
}`)

			agent, err := LoadAgent(workspace)
			if err != nil {
				t.Fatalf("LoadAgent() error: %v", err)
			}
			if !agent.Policies.CanShell {
				t.Fatalf("expected legacy unrestricted shell policy to default enabled")
			}
			if len(agent.Policies.ShellAllowlist) != 0 {
				t.Fatalf("shell_allowlist = %#v, want empty/unset allowlist", agent.Policies.ShellAllowlist)
			}
		})
	}
}

func TestLoadAgentKeepsExplicitDisabledShellWithRestrictedAllowlist(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "policies.json"), `{
  "fs_roots": ["./"],
  "allow_cross_agent_fs": false,
  "can_shell": false,
  "shell_allowlist": ["echo"],
  "network": { "enabled": true, "allow_domains": ["*"] },
  "budget": { "max_tokens_per_session": 200000 }
}`)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Policies.CanShell {
		t.Fatalf("expected explicit restricted shell policy to remain disabled")
	}
	if !reflect.DeepEqual(agent.Policies.ShellAllowlist, []string{"echo"}) {
		t.Fatalf("shell_allowlist = %#v, want [\"echo\"]", agent.Policies.ShellAllowlist)
	}
}

func TestLoadAgentInvalidModelPolicyFormat(t *testing.T) {
	config := defaultConfig()
	config.ModelPolicy = "gpt-4o-mini"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected invalid model_policy error")
	}
	if !strings.Contains(err.Error(), "provider:model") {
		t.Fatalf("expected provider:model guidance, got: %v", err)
	}
}

func TestLoadAgentModelPolicyAllowsColonInModelID(t *testing.T) {
	config := defaultConfig()
	config.ModelPolicy = "ollama:qwen3:0.6b"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	original := modelsToMap(ai.GetModels("ollama"))
	updated := modelsToMap(ai.GetModels("ollama"))
	updated["qwen3:0.6b"] = ai.Model{
		ID:            "qwen3:0.6b",
		Name:          "Qwen3 0.6B",
		API:           ai.APIOpenAICompletions,
		Provider:      ai.ProviderOllama,
		BaseURL:       "http://localhost:11434/v1",
		Reasoning:     true,
		Input:         []string{"text"},
		Cost:          ai.ModelCost{},
		ContextWindow: 32768,
		MaxTokens:     8192,
	}
	ai.SetModels(ai.ProviderOllama, updated)
	defer ai.SetModels(ai.ProviderOllama, original)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.model.ID != "qwen3:0.6b" {
		t.Fatalf("expected qwen3 model, got %q", agent.model.ID)
	}
}

func TestLoadAgentRejectsUnknownModelPolicy(t *testing.T) {
	config := defaultConfig()
	config.ModelPolicy = "openai:not-a-real-model"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected missing model_policy error")
	}
	if !strings.Contains(err.Error(), `model not found for model_policy "openai:not-a-real-model"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAgentRejectsFSRootsOutsideWorkspace(t *testing.T) {
	policies := defaultPolicies()
	policies.FSRoots = []string{"../outside"}
	workspace := createTestWorkspace(t, defaultConfig(), policies)

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected fs root escape error")
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("expected escape error, got: %v", err)
	}
}

func TestLoadAgentAllowsFSRootsOutsideWorkspaceWhenEnabled(t *testing.T) {
	otherWorkspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())

	policies := defaultPolicies()
	policies.FSRoots = []string{"./", otherWorkspace}
	policies.AllowCrossAgentFS = true
	workspace := createTestWorkspace(t, defaultConfig(), policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}

	otherWorkspace = evalSymlinksOrAncestor(filepath.Clean(otherWorkspace))
	found := false
	for _, root := range agent.allowedFSRoots {
		if root == otherWorkspace {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected cross-agent fs root %q in allowed roots: %#v", otherWorkspace, agent.allowedFSRoots)
	}
}

func TestLoadAgentHeartbeatDefaultsDisabled(t *testing.T) {
	workspace := createTestWorkspace(t, defaultConfig(), defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Heartbeat.Enabled {
		t.Fatalf("heartbeat should be disabled by default")
	}
}

func TestLoadAgentHeartbeatConfigParsesEveryPromptAndAckLimit(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every:       "10m",
		Prompt:      "check now",
		AckMaxChars: 144,
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !agent.Heartbeat.Enabled {
		t.Fatalf("heartbeat should be enabled")
	}
	if agent.Heartbeat.Every != 10*time.Minute {
		t.Fatalf("heartbeat every = %s, want 10m", agent.Heartbeat.Every)
	}
	if agent.Heartbeat.Prompt != "check now" {
		t.Fatalf("heartbeat prompt = %q, want check now", agent.Heartbeat.Prompt)
	}
	if agent.Heartbeat.AckMaxChars != 144 {
		t.Fatalf("heartbeat ack max = %d, want 144", agent.Heartbeat.AckMaxChars)
	}
}

func TestLoadAgentHeartbeatConfigUsesDefaultPromptWhenUnset(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every: "10m",
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !strings.Contains(agent.Heartbeat.Prompt, "HEARTBEAT_OK is internal status only") {
		t.Fatalf("heartbeat default prompt missing internal-status guidance: %q", agent.Heartbeat.Prompt)
	}
	if !strings.Contains(agent.Heartbeat.Prompt, "reply exactly HEARTBEAT_OK") {
		t.Fatalf("heartbeat default prompt missing HEARTBEAT_OK directive: %q", agent.Heartbeat.Prompt)
	}
}

func TestLoadAgentHeartbeatConfigParsesSessionAndActiveHours(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every:   "10m",
		Session: "sess-123",
		ActiveHours: &HeartbeatActiveHoursConfig{
			Start:    "09:00",
			End:      "18:30",
			Timezone: "America/New_York",
		},
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Heartbeat.SessionID != "sess-123" {
		t.Fatalf("heartbeat session = %q, want sess-123", agent.Heartbeat.SessionID)
	}
	if !agent.Heartbeat.ActiveHours.Enabled {
		t.Fatalf("expected active hours enabled")
	}
	if agent.Heartbeat.ActiveHours.StartMinute != 9*60 {
		t.Fatalf("active hours start minute = %d, want 540", agent.Heartbeat.ActiveHours.StartMinute)
	}
	if agent.Heartbeat.ActiveHours.EndMinute != 18*60+30 {
		t.Fatalf("active hours end minute = %d, want 1110", agent.Heartbeat.ActiveHours.EndMinute)
	}
	if agent.Heartbeat.ActiveHours.Timezone != "America/New_York" {
		t.Fatalf("active hours timezone = %q, want America/New_York", agent.Heartbeat.ActiveHours.Timezone)
	}
	if agent.Heartbeat.ActiveHours.Location == nil {
		t.Fatalf("expected active hours location to be loaded")
	}
}

func TestLoadAgentHeartbeatActiveHoursAllowsEndAt2400(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every: "10m",
		ActiveHours: &HeartbeatActiveHoursConfig{
			Start: "18:00",
			End:   "24:00",
		},
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Heartbeat.ActiveHours.EndMinute != 24*60 {
		t.Fatalf("active hours end minute = %d, want 1440", agent.Heartbeat.ActiveHours.EndMinute)
	}
}

func TestLoadAgentHeartbeatEveryBareNumberTreatsMinutes(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{Every: "15"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if agent.Heartbeat.Every != 15*time.Minute {
		t.Fatalf("heartbeat every = %s, want 15m", agent.Heartbeat.Every)
	}
}

func TestLoadAgentHeartbeatInvalidEveryReturnsError(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{Every: "not-a-duration"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected heartbeat duration error")
	}
	if !strings.Contains(err.Error(), "config.heartbeat.every") {
		t.Fatalf("expected heartbeat path in error, got: %v", err)
	}
}

func TestLoadAgentHeartbeatInvalidActiveHoursReturnsError(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every: "10m",
		ActiveHours: &HeartbeatActiveHoursConfig{
			Start: "9:00",
			End:   "18:00",
		},
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected heartbeat active_hours validation error")
	}
	if !strings.Contains(err.Error(), "config.heartbeat.active_hours") {
		t.Fatalf("expected active_hours path in error, got: %v", err)
	}
}

func TestLoadAgentHeartbeatInvalidActiveHoursStart2400ReturnsError(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every: "10m",
		ActiveHours: &HeartbeatActiveHoursConfig{
			Start: "24:00",
			End:   "18:00",
		},
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected heartbeat active_hours validation error for start=24:00")
	}
	if !strings.Contains(err.Error(), "config.heartbeat.active_hours") {
		t.Fatalf("expected active_hours path in error, got: %v", err)
	}
}

func TestLoadAgentHeartbeatInvalidActiveHoursTimezoneReturnsError(t *testing.T) {
	config := defaultConfig()
	config.Heartbeat = HeartbeatConfig{
		Every: "10m",
		ActiveHours: &HeartbeatActiveHoursConfig{
			Start:    "09:00",
			End:      "18:00",
			Timezone: "Mars/Olympus",
		},
	}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected heartbeat active_hours timezone validation error")
	}
	if !strings.Contains(err.Error(), "config.heartbeat.active_hours") {
		t.Fatalf("expected active_hours path in error, got: %v", err)
	}
}

func TestLoadAgentRejectsInvalidRequiredCapability(t *testing.T) {
	config := defaultConfig()
	config.Execution.RequiredCapabilities = []string{"invalid"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected execution.required_capabilities validation error")
	}
	if !strings.Contains(err.Error(), "expected kind:name") {
		t.Fatalf("expected kind:name guidance, got: %v", err)
	}
}

func TestLoadAgentImplicitlyEnablesDefaultTools(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if _, ok := agent.Tools.Get("web_search"); !ok {
		t.Fatalf("expected implicit web_search tool to be enabled")
	}
	if _, ok := agent.Tools.Get("web_fetch"); !ok {
		t.Fatalf("expected implicit web_fetch tool to be enabled")
	}
	if _, ok := agent.Tools.Get("delegate"); !ok {
		t.Fatalf("expected implicit delegate tool to be enabled")
	}
	if _, ok := agent.Tools.Get("heartbeat"); !ok {
		t.Fatalf("expected implicit heartbeat tool to be enabled")
	}
	if _, ok := agent.Tools.Get("message"); !ok {
		t.Fatalf("expected implicit message tool to be enabled")
	}
	if !containsTool(agent.Config.EnabledTools, "web_search") {
		t.Fatalf("expected web_search in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
	if !containsTool(agent.Config.EnabledTools, "web_fetch") {
		t.Fatalf("expected web_fetch in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
	if !containsTool(agent.Config.EnabledTools, "group:collaboration") {
		t.Fatalf("expected group:collaboration in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
}

func TestLoadAgentDisableDefaultSearchMCPSkipsImplicitTool(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs"}
	config.DisableDefaultSearchMCP = true
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if _, ok := agent.Tools.Get("web_search"); ok {
		t.Fatalf("did not expect implicit web_search tool when disable_default_search_mcp=true")
	}
	if _, ok := agent.Tools.Get("web_fetch"); ok {
		t.Fatalf("did not expect implicit web_fetch tool when disable_default_search_mcp=true")
	}
	if _, ok := agent.Tools.Get("delegate"); !ok {
		t.Fatalf("expected implicit delegate tool to remain enabled")
	}
	if _, ok := agent.Tools.Get("heartbeat"); !ok {
		t.Fatalf("expected implicit heartbeat tool to remain enabled")
	}
	if _, ok := agent.Tools.Get("message"); !ok {
		t.Fatalf("expected implicit message tool to remain enabled")
	}
}

func TestLoadAgentOmittedEnabledToolsEnablesAllBuiltInTools(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = nil
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if _, ok := agent.Tools.Get("exec"); !ok {
		t.Fatalf("expected exec tool to be enabled for omitted enabled_tools")
	}
	if _, ok := agent.Tools.Get("read"); !ok {
		t.Fatalf("expected fs tools to be enabled for omitted enabled_tools")
	}
	if _, ok := agent.Tools.Get("delegate"); !ok {
		t.Fatalf("expected collaboration tools to be enabled for omitted enabled_tools")
	}
	if _, ok := agent.Tools.Get("cron"); !ok {
		t.Fatalf("expected cron tool to be enabled for omitted enabled_tools")
	}
	if _, ok := agent.Tools.Get("web_search"); !ok {
		t.Fatalf("expected web_search tool to be enabled for omitted enabled_tools")
	}
	if _, ok := agent.Tools.Get("web_fetch"); !ok {
		t.Fatalf("expected web_fetch tool to be enabled for omitted enabled_tools")
	}
	if !containsTool(agent.Config.EnabledTools, "group:runtime") {
		t.Fatalf("expected group:runtime in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
	if !containsTool(agent.Config.EnabledTools, "group:fs") {
		t.Fatalf("expected group:fs in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
	if !containsTool(agent.Config.EnabledTools, "group:collaboration") {
		t.Fatalf("expected group:collaboration in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
	if !containsTool(agent.Config.EnabledTools, "group:web") {
		t.Fatalf("expected group:web in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
	if !containsTool(agent.Config.EnabledTools, "cron") {
		t.Fatalf("expected cron in agent config enabled_tools, got: %#v", agent.Config.EnabledTools)
	}
}

func TestLoadAgentOmittedEnabledToolsHonorsDisableDefaultSearchMCP(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = nil
	config.DisableDefaultSearchMCP = true
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if _, ok := agent.Tools.Get("web_search"); ok {
		t.Fatalf("did not expect web_search tool when disable_default_search_mcp=true and enabled_tools omitted")
	}
	if _, ok := agent.Tools.Get("web_fetch"); ok {
		t.Fatalf("did not expect web_fetch tool when disable_default_search_mcp=true and enabled_tools omitted")
	}
	if containsTool(agent.Config.EnabledTools, "group:web") {
		t.Fatalf("did not expect group:web selector when disable_default_search_mcp=true and enabled_tools omitted")
	}
}

func TestLoadAgentExplicitWebSearchStillWorksWhenDefaultDisabled(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"group:fs", "web_search", "web_fetch"}
	config.DisableDefaultSearchMCP = true
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if _, ok := agent.Tools.Get("web_search"); !ok {
		t.Fatalf("expected explicit web_search tool to remain enabled")
	}
	if _, ok := agent.Tools.Get("web_fetch"); !ok {
		t.Fatalf("expected explicit web_fetch tool to remain enabled")
	}
}

func TestLoadAgentImplicitlyEnablesApplyPatchForOpenAICodexModels(t *testing.T) {
	config := defaultConfig()
	config.ModelPolicy = "openai:gpt-5.3-codex"
	config.EnabledTools = []string{"group:fs"}
	policies := defaultPolicies()
	policies.ApplyPatchEnabled = false
	workspace := createTestWorkspace(t, config, policies)

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if !agent.Policies.ApplyPatchEnabled {
		t.Fatalf("expected apply_patch to be implicitly enabled for openai+codex model_policy")
	}
	if _, ok := agent.Tools.Get("apply_patch"); !ok {
		t.Fatalf("expected apply_patch tool to be enabled for openai+codex model_policy")
	}
}

func TestLoadAgentACPBuiltinCodex(t *testing.T) {
	config := defaultConfig()
	config.Runtime.Type = "acp"
	config.Runtime.ACP.Builtin = "codex"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if got := strings.TrimSpace(agent.Config.Runtime.ACP.Agent); got != "codex" {
		t.Fatalf("runtime.acp.agent = %q, want codex", got)
	}
}

func TestLoadAgentACPBuiltinOpenCode(t *testing.T) {
	config := defaultConfig()
	config.Runtime.Type = "acp"
	config.Runtime.ACP.Builtin = "opencode"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if got := strings.TrimSpace(agent.Config.Runtime.ACP.Agent); got != "opencode" {
		t.Fatalf("runtime.acp.agent = %q, want opencode", got)
	}
}

func TestLoadAgentRejectsUnknownACPBuiltin(t *testing.T) {
	config := defaultConfig()
	config.Runtime.Type = "acp"
	config.Runtime.ACP.Builtin = "claude"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected invalid runtime.acp.builtin error")
	}
	if !strings.Contains(err.Error(), "runtime.acp.builtin") {
		t.Fatalf("expected runtime.acp.builtin error, got: %v", err)
	}
}

func modelsToMap(models []ai.Model) map[string]ai.Model {
	out := make(map[string]ai.Model, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out
}

func containsTool(tools []string, target string) bool {
	for _, item := range tools {
		if strings.TrimSpace(item) == target {
			return true
		}
	}
	return false
}

func TestLoadAgentDefaultsRuntimeToNative(t *testing.T) {
	config := defaultConfig()
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if got := strings.TrimSpace(agent.Config.Runtime.Type); got != "native" {
		t.Fatalf("runtime.type = %q, want native", got)
	}
}

func TestLoadAgentAppliesACPDefaults(t *testing.T) {
	config := defaultConfig()
	config.Runtime.Type = "acp"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	if got := strings.TrimSpace(agent.Config.Runtime.ACP.Command); got != "acpx" {
		t.Fatalf("runtime.acp.command = %q, want acpx", got)
	}
	if got := strings.TrimSpace(agent.Config.Runtime.ACP.Agent); got != "codex" {
		t.Fatalf("runtime.acp.agent = %q, want codex", got)
	}
	if len(agent.Config.Runtime.ACP.Args) == 0 {
		t.Fatalf("runtime.acp.args should be defaulted")
	}
}

func TestLoadAgentRejectsUnknownRuntimeType(t *testing.T) {
	config := defaultConfig()
	config.Runtime.Type = "custom"
	workspace := createTestWorkspace(t, config, defaultPolicies())

	_, err := LoadAgent(workspace)
	if err == nil {
		t.Fatalf("expected invalid runtime type error")
	}
	if !strings.Contains(err.Error(), "runtime.type") {
		t.Fatalf("expected runtime.type error, got: %v", err)
	}
}
