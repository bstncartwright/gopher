package agentcore

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	"github.com/bstncartwright/gopher/pkg/memory"
	"github.com/bstncartwright/gopher/pkg/memory/retrieval"
	memsqlite "github.com/bstncartwright/gopher/pkg/memory/store/sqlite"
	"github.com/pelletier/go-toml/v2"
)

const (
	defaultHeartbeatPrompt      = "Run heartbeat checks using HEARTBEAT.md when available. If no user-facing action is required, reply exactly HEARTBEAT_OK."
	defaultHeartbeatAckMaxChars = 300
)

func LoadAgent(workspacePath string) (*Agent, error) {
	slog.Info("load_agent: starting agent load", "workspace_path", workspacePath)
	if strings.TrimSpace(workspacePath) == "" {
		slog.Error("load_agent: workspace path is required")
		return nil, fmt.Errorf("workspace path is required")
	}

	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		slog.Error("load_agent: failed to resolve workspace path", "workspace_path", workspacePath, "error", err)
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}
	slog.Debug("load_agent: resolved workspace path", "workspace_path", workspacePath, "workspace_abs", workspaceAbs)

	configPath, err := resolveAgentFile(workspaceAbs, "config")
	if err != nil {
		slog.Error("load_agent: required config file missing", "workspace", workspaceAbs, "error", err)
		return nil, err
	}
	legacyPoliciesPath, err := resolveOptionalAgentFile(workspaceAbs, "policies")
	if err != nil {
		slog.Error("load_agent: failed to resolve optional legacy policies file", "workspace", workspaceAbs, "error", err)
		return nil, fmt.Errorf("resolve optional policies file: %w", err)
	}
	slog.Debug("load_agent: config file verified", "config_path", configPath, "legacy_policies_path", legacyPoliciesPath)

	configBlob, err := os.ReadFile(configPath)
	if err != nil {
		slog.Error("load_agent: failed to read config file", "config_path", configPath, "error", err)
		return nil, fmt.Errorf("read %s: %w", filepath.Base(configPath), err)
	}
	config := AgentConfig{}
	if err := decodeRawWithFormat(configPath, configBlob, &config); err != nil {
		slog.Error("load_agent: failed to read config", "config_path", configPath, "error", err)
		return nil, fmt.Errorf("read %s: %w", filepath.Base(configPath), err)
	}
	applyDefaultEnabledTools(&config)
	slog.Info("load_agent: loaded config",
		"agent_id", config.AgentID,
		"name", config.Name,
		"role", config.Role,
		"model_policy", config.ModelPolicy,
		"enabled_tools", config.EnabledTools,
		"max_context_messages", config.MaxContextMessages,
	)
	if config.MaxContextMessages <= 0 {
		config.MaxContextMessages = DefaultContextWindow
		slog.Debug("load_agent: using default max_context_messages", "value", config.MaxContextMessages)
	}
	if config.BootstrapMaxChars <= 0 {
		config.BootstrapMaxChars = DefaultBootstrapMaxChars
	}
	if config.BootstrapTotalMaxChars <= 0 {
		config.BootstrapTotalMaxChars = DefaultBootstrapTotalMaxChars
	}
	if normalized, ok := normalizeTimeFormat(config.TimeFormat); ok {
		config.TimeFormat = normalized
	} else {
		config.TimeFormat = "auto"
	}
	applyDefaultContextManagement(&config)
	if err := validateRequiredCapabilities(config.Execution.RequiredCapabilities); err != nil {
		slog.Error("load_agent: invalid required_capabilities", "required_capabilities", config.Execution.RequiredCapabilities, "error", err)
		return nil, err
	}
	heartbeat, err := NormalizeHeartbeatConfig(config.Heartbeat)
	if err != nil {
		slog.Error("load_agent: invalid heartbeat config", "error", err)
		return nil, err
	}
	slog.Debug("load_agent: heartbeat config normalized", "enabled", heartbeat.Enabled, "every", heartbeat.Every)

	rawConfig := map[string]any{}
	if err := decodeRawWithFormat(configPath, configBlob, &rawConfig); err != nil {
		slog.Error("load_agent: failed to parse config source", "config_path", configPath, "error", err)
		return nil, fmt.Errorf("read %s: %w", filepath.Base(configPath), err)
	}

	policies := AgentPolicies{}
	policiesSource := ""
	if rawPolicies, ok := readMapField(rawConfig, "policies"); ok {
		if config.Policies != nil {
			policies = *config.Policies
		}
		applyDefaultPoliciesFromRawMap(rawPolicies, &policies)
		policiesSource = filepath.Base(configPath) + " [policies]"
	} else if legacyPoliciesPath != "" {
		if err := decodePoliciesFile(legacyPoliciesPath, &policies); err != nil {
			slog.Error("load_agent: failed to read legacy policies", "policies_path", legacyPoliciesPath, "error", err)
			return nil, fmt.Errorf("read %s: %w", filepath.Base(legacyPoliciesPath), err)
		}
		policiesSource = filepath.Base(legacyPoliciesPath)
	} else {
		policies = defaultOpenPolicies(workspaceAbs)
		policiesSource = "built-in defaults"
	}
	slog.Info("load_agent: loaded policies",
		"source", policiesSource,
		"can_shell", policies.CanShell,
		"shell_allowlist", policies.ShellAllowlist,
		"fs_roots", policies.FSRoots,
		"allow_cross_agent_fs", policies.AllowCrossAgentFS,
		"apply_patch_enabled", policies.ApplyPatchEnabled,
	)

	providerName, modelID, err := parseModelPolicy(config.ModelPolicy)
	if err != nil {
		slog.Error("load_agent: failed to parse model_policy", "model_policy", config.ModelPolicy, "error", err)
		return nil, err
	}
	if shouldEnableApplyPatchForModelPolicy(config.ModelPolicy) {
		policies.ApplyPatchEnabled = true
	}
	slog.Debug("load_agent: parsed model policy", "provider_name", providerName, "model_id", modelID)
	model, ok := ai.GetModel(providerName, modelID)
	if !ok {
		slog.Error("load_agent: model not found", "provider_name", providerName, "model_id", modelID, "model_policy", config.ModelPolicy)
		return nil, fmt.Errorf("model not found for model_policy %q", config.ModelPolicy)
	}
	slog.Info("load_agent: resolved model",
		"provider", model.Provider,
		"model_id", model.ID,
		"context_window", model.ContextWindow,
		"reasoning", model.Reasoning,
	)

	allowedRoots, err := resolveAllowedFSRoots(workspaceAbs, policies.FSRoots, policies.AllowCrossAgentFS)
	if err != nil {
		slog.Error("load_agent: failed to resolve fs roots", "error", err)
		return nil, err
	}
	slog.Debug("load_agent: resolved allowed fs roots", "roots", allowedRoots)
	contextWindow := model.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 12000
		slog.Debug("load_agent: using default context window", "context_window", contextWindow)
	}

	agent := &Agent{
		ID:             config.AgentID,
		Name:           config.Name,
		Role:           config.Role,
		Workspace:      workspaceAbs,
		Config:         config,
		Policies:       policies,
		Tools:          buildRegistry(config.EnabledTools, policies),
		Memory:         NewFileMemoryStore(filepath.Join(workspaceAbs, "memory", "working.json")),
		LongTermMemory: buildLongTermMemoryManager(workspaceAbs),
		Assembler:      ctxbundle.NewAssembler(ctxbundle.AssemblerOptions{DefaultMaxTokens: contextWindow}),
		Logger:         NewJSONLEventLogger(filepath.Join(workspaceAbs, "logs", "events.jsonl")),
		Provider:       AIStreamProvider{},
		Processes:      NewProcessManager(),
		Heartbeat:      heartbeat,
		model:          model,
		allowedFSRoots: allowedRoots,
	}
	slog.Debug("load_agent: agent struct created", "agent_id", agent.ID, "tools_count", len(agent.Tools.Schemas()))

	skills, err := discoverSkills(workspaceAbs, config.SkillsPaths)
	if err != nil {
		slog.Error("load_agent: failed to discover skills", "skills_paths", config.SkillsPaths, "error", err)
		return nil, fmt.Errorf("discover skills: %w", err)
	}
	agent.skills = skills
	slog.Debug("load_agent: skills discovered", "skills_count", len(skills))

	if strings.TrimSpace(agent.ID) == "" {
		agent.ID = strings.TrimSpace(config.Name)
		slog.Debug("load_agent: using name as agent_id", "agent_id", agent.ID)
	}
	if strings.TrimSpace(agent.ID) == "" {
		slog.Error("load_agent: agent_id is required")
		return nil, fmt.Errorf("config.agent_id is required")
	}
	agent.KnownAgents = []string{strings.TrimSpace(agent.ID)}

	slog.Info("load_agent: agent loaded successfully",
		"agent_id", agent.ID,
		"name", agent.Name,
		"workspace", workspaceAbs,
		"model", model.ID,
		"provider", model.Provider,
	)
	return agent, nil
}

func buildLongTermMemoryManager(workspaceAbs string) memory.MemoryManager {
	path := filepath.Join(workspaceAbs, "memory", "memory.db")
	slog.Debug("build_long_term_memory_manager: initializing", "path", path)
	store, err := memsqlite.NewStore(memsqlite.StoreOptions{Path: path})
	if err != nil {
		slog.Warn("build_long_term_memory_manager: failed to create store, disabling long-term memory", "path", path, "error", err)
		return nil
	}

	manager, err := memory.NewManager(memory.ManagerOptions{
		Store:            store,
		Retriever:        retrieval.NewHybridRetriever(retrieval.HybridRetrieverOptions{}),
		Embedder:         memory.NewHashEmbedder(128),
		FailOpenRetrieve: true,
		FailOpenStore:    false,
	})
	if err != nil {
		slog.Warn("build_long_term_memory_manager: failed to create manager, disabling long-term memory", "path", path, "error", err)
		_ = store.Close()
		return nil
	}
	slog.Debug("build_long_term_memory_manager: initialized successfully", "path", path)
	return manager
}

func parseModelPolicy(raw string) (providerName string, modelID string, err error) {
	parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid model_policy %q: expected provider:model", raw)
	}
	providerName = strings.TrimSpace(parts[0])
	modelID = strings.TrimSpace(parts[1])
	if providerName == "" || modelID == "" {
		return "", "", fmt.Errorf("invalid model_policy %q: provider and model are required", raw)
	}
	return providerName, modelID, nil
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("required file missing: %s", filepath.Base(path))
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("required file is a directory: %s", filepath.Base(path))
	}
	return nil
}

func resolveAgentFile(workspace, baseName string) (string, error) {
	candidates := []string{
		filepath.Join(workspace, baseName+".toml"),
		filepath.Join(workspace, baseName+".json"),
	}
	for _, path := range candidates {
		if err := requireFile(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("required file missing: %s.{toml,json}", baseName)
}

func resolveOptionalAgentFile(workspace, baseName string) (string, error) {
	candidates := []string{
		filepath.Join(workspace, baseName+".toml"),
		filepath.Join(workspace, baseName+".json"),
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		return path, nil
	}
	return "", nil
}

func decodeAgentDocument(path string, out any) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return toml.Unmarshal(blob, out)
	case ".json":
		return json.Unmarshal(blob, out)
	default:
		return fmt.Errorf("unsupported config format %q", filepath.Ext(path))
	}
}

func decodePoliciesFile(path string, out *AgentPolicies) error {
	if out == nil {
		return fmt.Errorf("policies output is required")
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := decodeAgentDocument(path, out); err != nil {
		return err
	}
	applyDefaultPolicies(path, blob, out)
	return nil
}

func applyDefaultPolicies(path string, raw []byte, policies *AgentPolicies) {
	if policies == nil {
		return
	}

	rawPolicies := map[string]any{}
	if err := decodeRawWithFormat(path, raw, &rawPolicies); err != nil {
		return
	}
	applyDefaultPoliciesFromRawMap(rawPolicies, policies)
}

func applyDefaultPoliciesFromRawMap(rawPolicies map[string]any, policies *AgentPolicies) {
	if policies == nil {
		return
	}
	canShell := readBoolField(rawPolicies, "can_shell")
	shellAllowlist := readStringSliceField(rawPolicies, "shell_allowlist")
	if canShell != nil && !*canShell && shellAllowlistIsUnspecifiedOrDefault(shellAllowlist) {
		policies.CanShell = true
	}
	if canShell == nil {
		policies.CanShell = true
	}

	rawNetworkAny, hasNetwork := readFieldValue(rawPolicies, "network")
	rawNetwork, ok := rawNetworkAny.(map[string]any)
	if !hasNetwork || !ok {
		policies.Network.Enabled = true
		policies.Network.AllowDomains = nil
		policies.Network.BlockDomains = nil
		return
	}

	rawNetworkEnabled := readBoolField(rawNetwork, "enabled")
	rawNetworkAllowDomains := readStringSliceField(rawNetwork, "allow_domains")
	rawNetworkBlockDomains := readStringSliceField(rawNetwork, "block_domains")

	if rawNetworkEnabled != nil && !*rawNetworkEnabled && networkDomainsAreUnrestricted(rawNetworkAllowDomains, rawNetworkBlockDomains) {
		policies.Network.Enabled = true
		policies.Network.AllowDomains = nil
		policies.Network.BlockDomains = nil
		return
	}
	if rawNetworkEnabled == nil {
		policies.Network.Enabled = true
	}
	if rawNetworkBlockDomains == nil && policies.Network.Enabled {
		policies.Network.BlockDomains = nil
	}
}

func decodeRawWithFormat(path string, raw []byte, out any) error {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		return toml.Unmarshal(raw, out)
	case ".json":
		return json.Unmarshal(raw, out)
	default:
		return fmt.Errorf("unsupported config format %q", filepath.Ext(path))
	}
}

func readBoolField(raw map[string]any, key string) *bool {
	value, ok := readFieldValue(raw, key)
	if !ok {
		return nil
	}
	b, ok := value.(bool)
	if !ok {
		return nil
	}
	return &b
}

func readStringSliceField(raw map[string]any, key string) *[]string {
	value, ok := readFieldValue(raw, key)
	if !ok {
		return nil
	}
	entries, ok := value.([]any)
	if !ok {
		stringsSlice, ok := value.([]string)
		if !ok {
			return nil
		}
		out := append([]string(nil), stringsSlice...)
		return &out
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		text, ok := entry.(string)
		if !ok {
			continue
		}
		out = append(out, text)
	}
	return &out
}

func readMapField(raw map[string]any, key string) (map[string]any, bool) {
	value, ok := readFieldValue(raw, key)
	if !ok || value == nil {
		return nil, false
	}
	parsed, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	return parsed, true
}

func readFieldValue(raw map[string]any, key string) (any, bool) {
	if raw == nil {
		return nil, false
	}
	if value, ok := raw[key]; ok {
		return value, true
	}
	for candidateKey, value := range raw {
		if strings.EqualFold(strings.TrimSpace(candidateKey), key) {
			return value, true
		}
	}
	return nil, false
}

func networkDomainsAreUnrestricted(allowDomains *[]string, blockDomains *[]string) bool {
	if blockDomains != nil && len(*blockDomains) > 0 {
		return false
	}
	if allowDomains == nil {
		return true
	}
	for _, candidate := range *allowDomains {
		if normalizeDomainHost(candidate) == "*" {
			return true
		}
	}
	return false
}

func shellAllowlistIsUnspecifiedOrDefault(allowlist *[]string) bool {
	if allowlist == nil {
		return true
	}
	normalizedAllowlist := normalizeUniqueStrings(*allowlist)
	return len(normalizedAllowlist) == 0
}

func applyDefaultEnabledTools(cfg *AgentConfig) {
	if cfg == nil {
		return
	}
	if len(cfg.EnabledTools) == 0 {
		// Unset enabled_tools means "all built-in tools".
		cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:fs")
		cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:runtime")
		cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:collaboration")
		cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "cron")
		if !cfg.DisableDefaultSearchMCP {
			cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:web")
		}
		return
	}
	cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:collaboration")
	if cfg.DisableDefaultSearchMCP {
		return
	}
	cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "web_search")
	cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "web_fetch")
}

func shouldEnableApplyPatchForModelPolicy(modelPolicy string) bool {
	policy := strings.ToLower(strings.TrimSpace(modelPolicy))
	return strings.Contains(policy, "openai") && strings.Contains(policy, "codex")
}

func applyDefaultContextManagement(cfg *AgentConfig) {
	if cfg == nil {
		return
	}
	if cfg.ContextManagement.EnablePruning == nil {
		cfg.ContextManagement.EnablePruning = boolPtr(true)
	}
	if cfg.ContextManagement.EnableCompaction == nil {
		cfg.ContextManagement.EnableCompaction = boolPtr(true)
	}
	if cfg.ContextManagement.EnableOverflowRetry == nil {
		cfg.ContextManagement.EnableOverflowRetry = boolPtr(true)
	}
	if cfg.ContextManagement.ModelCompactionSummary == nil {
		cfg.ContextManagement.ModelCompactionSummary = boolPtr(true)
	}
	cfg.ContextManagement.Mode = cfg.ContextManagement.ModeValue()
	cfg.ContextManagement.OverflowRetryLimit = cfg.ContextManagement.OverflowRetryLimitValue()
	cfg.ContextManagement.ReserveMinTokens = cfg.ContextManagement.ReserveMinTokensValue()
	cfg.ContextManagement.CompactionSummaryTimeoutMS = cfg.ContextManagement.CompactionSummaryTimeoutMSValue()
	cfg.ContextManagement.CompactionChunkTokenTarget = cfg.ContextManagement.CompactionChunkTokenTargetValue()
	cfg.ContextManagement.ToolResultContextMaxChars = cfg.ContextManagement.ToolResultContextMaxCharsValue()
	cfg.ContextManagement.ToolResultContextHeadChars = cfg.ContextManagement.ToolResultContextHeadCharsValue()
	cfg.ContextManagement.ToolResultContextTailChars = cfg.ContextManagement.ToolResultContextTailCharsValue()
	cfg.ContextManagement.RecentToolResultChars = cfg.ContextManagement.RecentToolResultCharsValue()
	cfg.ContextManagement.HistoricalToolResultChars = cfg.ContextManagement.HistoricalToolResultCharsValue()
}

func boolPtr(value bool) *bool {
	v := value
	return &v
}

func appendUniqueTool(tools []string, target string) []string {
	target = strings.TrimSpace(target)
	if target == "" {
		return tools
	}
	for _, tool := range tools {
		if strings.TrimSpace(tool) == target {
			return tools
		}
	}
	return append(tools, target)
}

func defaultOpenPolicies(workspaceAbs string) AgentPolicies {
	root := filesystemRootForWorkspace(workspaceAbs)
	return AgentPolicies{
		FSRoots:           []string{root},
		AllowCrossAgentFS: true,
		CanShell:          true,
		ShellAllowlist:    nil,
		Network: NetworkPolicy{
			Enabled:      true,
			AllowDomains: nil,
			BlockDomains: nil,
		},
	}
}

func filesystemRootForWorkspace(workspaceAbs string) string {
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(workspaceAbs); volume != "" {
		root = volume + string(filepath.Separator)
	}
	return root
}

func resolveAllowedFSRoots(workspace string, roots []string, allowCrossAgentFS bool) ([]string, error) {
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %q: %w", workspace, err)
	}
	workspaceRoot := evalSymlinksOrAncestor(filepath.Clean(workspaceAbs))

	if len(roots) == 0 {
		if allowCrossAgentFS {
			roots = []string{filesystemRootForWorkspace(workspaceAbs)}
		} else {
			roots = []string{"./"}
		}
	}

	out := make([]string, 0, len(roots))
	seen := map[string]struct{}{}
	for _, root := range roots {
		candidate := strings.TrimSpace(root)
		if candidate == "" {
			continue
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(workspace, candidate)
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve fs root %q: %w", root, err)
		}
		clean := filepath.Clean(abs)
		// Resolve symlinks so roots match paths resolved in resolvePathInAllowedRoots.
		// Use the same ancestor-walking strategy so non-existent roots with
		// symlinked ancestors resolve identically on both sides.
		clean = evalSymlinksOrAncestor(clean)
		if !allowCrossAgentFS && !isWithinRoot(clean, workspaceRoot) {
			return nil, fmt.Errorf("fs root %q escapes workspace %q", root, workspace)
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("policies.fs_roots resolves to empty set")
	}
	return out, nil
}

func normalizeTimeFormat(raw string) (string, bool) {
	value := strings.TrimSpace(strings.ToLower(raw))
	switch value {
	case "", "auto":
		return "auto", true
	case "12", "24":
		return value, true
	default:
		return "", false
	}
}

func validateRequiredCapabilities(required []string) error {
	for _, raw := range required {
		parts := strings.SplitN(strings.TrimSpace(raw), ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid execution.required_capabilities entry %q: expected kind:name", raw)
		}
		kind := strings.ToLower(strings.TrimSpace(parts[0]))
		name := strings.TrimSpace(parts[1])
		if name == "" {
			return fmt.Errorf("invalid execution.required_capabilities entry %q: name is required", raw)
		}
		switch kind {
		case "agent", "tool", "system":
		default:
			return fmt.Errorf("invalid execution.required_capabilities entry %q: unknown kind %q", raw, strings.TrimSpace(parts[0]))
		}
	}
	return nil
}

func NormalizeHeartbeatConfig(input HeartbeatConfig) (AgentHeartbeat, error) {
	everyRaw := strings.TrimSpace(input.Every)
	if everyRaw == "" {
		return AgentHeartbeat{}, nil
	}

	every, err := parseHeartbeatEvery(everyRaw)
	if err != nil {
		return AgentHeartbeat{}, fmt.Errorf("invalid config.heartbeat.every: %w", err)
	}
	if every == 0 {
		return AgentHeartbeat{}, nil
	}
	if every < 0 {
		return AgentHeartbeat{}, fmt.Errorf("invalid config.heartbeat.every: duration must be >= 0")
	}

	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		prompt = defaultHeartbeatPrompt
	}

	ackMaxChars := input.AckMaxChars
	if ackMaxChars <= 0 {
		ackMaxChars = defaultHeartbeatAckMaxChars
	}
	sessionID := strings.TrimSpace(input.Session)
	activeHours, err := normalizeHeartbeatActiveHours(input.ActiveHours)
	if err != nil {
		return AgentHeartbeat{}, fmt.Errorf("invalid config.heartbeat.active_hours: %w", err)
	}

	return AgentHeartbeat{
		Enabled:     true,
		Every:       every,
		Prompt:      prompt,
		AckMaxChars: ackMaxChars,
		SessionID:   sessionID,
		ActiveHours: activeHours,
	}, nil
}

func normalizeHeartbeatActiveHours(input *HeartbeatActiveHoursConfig) (AgentHeartbeatActiveHours, error) {
	if input == nil {
		return AgentHeartbeatActiveHours{}, nil
	}

	startMinute, start, err := parseHeartbeatClock(input.Start, false, "start")
	if err != nil {
		return AgentHeartbeatActiveHours{}, err
	}
	endMinute, end, err := parseHeartbeatClock(input.End, true, "end")
	if err != nil {
		return AgentHeartbeatActiveHours{}, err
	}
	timezone := strings.TrimSpace(input.Timezone)
	var location *time.Location
	if timezone != "" {
		loaded, loadErr := time.LoadLocation(timezone)
		if loadErr != nil {
			return AgentHeartbeatActiveHours{}, fmt.Errorf("timezone %q: %w", timezone, loadErr)
		}
		location = loaded
	}
	return AgentHeartbeatActiveHours{
		Enabled:     true,
		Start:       start,
		End:         end,
		StartMinute: startMinute,
		EndMinute:   endMinute,
		Timezone:    timezone,
		Location:    location,
	}, nil
}

func parseHeartbeatClock(raw string, allow24 bool, field string) (int, string, error) {
	value := strings.TrimSpace(raw)
	if len(value) != 5 || value[2] != ':' {
		return 0, "", fmt.Errorf("%s must be HH:MM", field)
	}
	hourPart := value[:2]
	minutePart := value[3:]
	if !isDigits(hourPart) || !isDigits(minutePart) {
		return 0, "", fmt.Errorf("%s must be HH:MM", field)
	}
	hour, err := strconv.Atoi(hourPart)
	if err != nil {
		return 0, "", fmt.Errorf("%s hour must be numeric", field)
	}
	minute, err := strconv.Atoi(minutePart)
	if err != nil {
		return 0, "", fmt.Errorf("%s minute must be numeric", field)
	}
	if minute < 0 || minute > 59 {
		return 0, "", fmt.Errorf("%s minute must be between 00 and 59", field)
	}
	if hour == 24 {
		if !allow24 || minute != 0 {
			return 0, "", fmt.Errorf("%s hour must be between 00 and 23", field)
		}
		return 24 * 60, "24:00", nil
	}
	if hour < 0 || hour > 23 {
		return 0, "", fmt.Errorf("%s hour must be between 00 and 23", field)
	}
	return hour*60 + minute, fmt.Sprintf("%02d:%02d", hour, minute), nil
}

func parseHeartbeatEvery(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	if isDigits(value) {
		value += "m"
	}
	return time.ParseDuration(value)
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}
