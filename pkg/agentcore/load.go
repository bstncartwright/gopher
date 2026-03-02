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
)

const (
	defaultHeartbeatPrompt      = "Run heartbeat checks using HEARTBEAT.md when available. If no user-facing action is required, reply exactly HEARTBEAT_OK."
	defaultHeartbeatAckMaxChars = 300
)

func defaultShellAllowlistValues() []string {
	return []string{"echo", "git", "go", "bun", "node", "bash", "gopher"}
}

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

	configPath := filepath.Join(workspaceAbs, "config.json")
	policiesPath := filepath.Join(workspaceAbs, "policies.json")

	for _, required := range []string{configPath, policiesPath} {
		if err := requireFile(required); err != nil {
			slog.Error("load_agent: required file missing", "file", required, "error", err)
			return nil, err
		}
	}
	slog.Debug("load_agent: required files verified", "config_path", configPath, "policies_path", policiesPath)

	config := AgentConfig{}
	if err := decodeJSONFile(configPath, &config); err != nil {
		slog.Error("load_agent: failed to read config.json", "config_path", configPath, "error", err)
		return nil, fmt.Errorf("read config.json: %w", err)
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

	policies := AgentPolicies{}
	if err := decodePoliciesFile(policiesPath, &policies); err != nil {
		slog.Error("load_agent: failed to read policies.json", "policies_path", policiesPath, "error", err)
		return nil, fmt.Errorf("read policies.json: %w", err)
	}
	slog.Info("load_agent: loaded policies",
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

func decodeJSONFile(path string, out any) error {
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(blob, out); err != nil {
		return err
	}
	return nil
}

func decodePoliciesFile(path string, out *AgentPolicies) error {
	if out == nil {
		return fmt.Errorf("policies output is required")
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(blob, out); err != nil {
		return err
	}
	applyDefaultPolicies(blob, out)
	return nil
}

func applyDefaultPolicies(raw []byte, policies *AgentPolicies) {
	if policies == nil {
		return
	}

	var rawPolicies struct {
		CanShell       *bool           `json:"can_shell"`
		ShellAllowlist *[]string       `json:"shell_allowlist"`
		Network        json.RawMessage `json:"network"`
	}
	if err := json.Unmarshal(raw, &rawPolicies); err != nil {
		return
	}
	if rawPolicies.CanShell != nil && !*rawPolicies.CanShell && shellAllowlistIsUnspecifiedOrDefault(rawPolicies.ShellAllowlist) {
		policies.CanShell = true
	}
	if rawPolicies.CanShell == nil {
		policies.CanShell = true
	}
	if rawPolicies.ShellAllowlist == nil && policies.CanShell {
		policies.ShellAllowlist = defaultShellAllowlistValues()
	}

	if len(rawPolicies.Network) == 0 {
		policies.Network.Enabled = true
		policies.Network.AllowDomains = []string{"*"}
		return
	}

	var rawNetwork struct {
		Enabled      *bool     `json:"enabled"`
		AllowDomains *[]string `json:"allow_domains"`
		BlockDomains *[]string `json:"block_domains"`
	}
	if err := json.Unmarshal(rawPolicies.Network, &rawNetwork); err != nil {
		return
	}
	if rawNetwork.Enabled != nil && !*rawNetwork.Enabled && networkDomainsAreUnrestricted(rawNetwork.AllowDomains, rawNetwork.BlockDomains) {
		policies.Network.Enabled = true
		policies.Network.AllowDomains = []string{"*"}
		return
	}
	if rawNetwork.Enabled == nil {
		policies.Network.Enabled = true
	}
	if rawNetwork.AllowDomains == nil && policies.Network.Enabled {
		policies.Network.AllowDomains = []string{"*"}
	}
	if rawNetwork.BlockDomains == nil && policies.Network.Enabled {
		policies.Network.BlockDomains = nil
	}
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
	if len(normalizedAllowlist) == 0 {
		return true
	}
	defaultAllowlist := normalizeUniqueStrings(defaultShellAllowlistValues())
	if len(normalizedAllowlist) != len(defaultAllowlist) {
		return false
	}
	for i := range normalizedAllowlist {
		if normalizedAllowlist[i] != defaultAllowlist[i] {
			return false
		}
	}
	return true
}

func applyDefaultEnabledTools(cfg *AgentConfig) {
	if cfg == nil {
		return
	}
	// Backfill baseline tools for legacy configs that omit enabled_tools entirely.
	if len(cfg.EnabledTools) == 0 {
		cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:fs")
		cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:runtime")
	}
	cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "group:collaboration")
	if cfg.DisableDefaultSearchMCP {
		return
	}
	cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "web_search")
	cfg.EnabledTools = appendUniqueTool(cfg.EnabledTools, "web_fetch")
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

func resolveAllowedFSRoots(workspace string, roots []string, allowCrossAgentFS bool) ([]string, error) {
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root %q: %w", workspace, err)
	}
	workspaceRoot := evalSymlinksOrAncestor(filepath.Clean(workspaceAbs))

	if len(roots) == 0 {
		roots = []string{"./"}
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
