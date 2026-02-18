package agentcore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func LoadAgent(workspacePath string) (*Agent, error) {
	if strings.TrimSpace(workspacePath) == "" {
		return nil, fmt.Errorf("workspace path is required")
	}

	workspaceAbs, err := filepath.Abs(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace path: %w", err)
	}

	configPath := filepath.Join(workspaceAbs, "config.json")
	policiesPath := filepath.Join(workspaceAbs, "policies.json")

	for _, required := range []string{configPath, policiesPath} {
		if err := requireFile(required); err != nil {
			return nil, err
		}
	}

	config := AgentConfig{}
	if err := decodeJSONFile(configPath, &config); err != nil {
		return nil, fmt.Errorf("read config.json: %w", err)
	}
	if config.MaxContextMessages <= 0 {
		config.MaxContextMessages = DefaultContextWindow
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
	heartbeat, err := normalizeHeartbeatConfig(config.Heartbeat)
	if err != nil {
		return nil, err
	}

	policies := AgentPolicies{}
	if err := decodeJSONFile(policiesPath, &policies); err != nil {
		return nil, fmt.Errorf("read policies.json: %w", err)
	}

	providerName, modelID, err := parseModelPolicy(config.ModelPolicy)
	if err != nil {
		return nil, err
	}
	model, ok := ai.GetModel(providerName, modelID)
	if !ok {
		return nil, fmt.Errorf("model not found for model_policy %q", config.ModelPolicy)
	}

	allowedRoots, err := resolveAllowedFSRoots(workspaceAbs, policies.FSRoots, policies.AllowCrossAgentFS)
	if err != nil {
		return nil, err
	}
	contextWindow := model.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 12000
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

	skills, err := discoverSkills(workspaceAbs, config.SkillsPaths)
	if err != nil {
		return nil, fmt.Errorf("discover skills: %w", err)
	}
	agent.skills = skills

	if strings.TrimSpace(agent.ID) == "" {
		agent.ID = strings.TrimSpace(config.Name)
	}
	if strings.TrimSpace(agent.ID) == "" {
		return nil, fmt.Errorf("config.agent_id is required")
	}

	return agent, nil
}

func buildLongTermMemoryManager(workspaceAbs string) memory.MemoryManager {
	path := filepath.Join(workspaceAbs, "memory", "memory.db")
	store, err := memsqlite.NewStore(memsqlite.StoreOptions{Path: path})
	if err != nil {
		return nil
	}

	manager, err := memory.NewManager(memory.ManagerOptions{
		Store:     store,
		Retriever: retrieval.NewHybridRetriever(retrieval.HybridRetrieverOptions{}),
		Embedder:  memory.NewHashEmbedder(128),
		FailOpen:  true,
	})
	if err != nil {
		_ = store.Close()
		return nil
	}
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

func normalizeHeartbeatConfig(input HeartbeatConfig) (AgentHeartbeat, error) {
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

	return AgentHeartbeat{
		Enabled:     true,
		Every:       every,
		Prompt:      prompt,
		AckMaxChars: ackMaxChars,
	}, nil
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
