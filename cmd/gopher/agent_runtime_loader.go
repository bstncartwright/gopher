package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/pelletier/go-toml/v2"
)

const defaultAgentWorkspaceID = "main"

type agentRuntime struct {
	Executor       sessionrt.AgentExecutor
	Router         *agentcore.ActorExecutorRouter
	DefaultActorID sessionrt.ActorID
	Agents         map[sessionrt.ActorID]*agentcore.Agent
}

type agentRuntimeOptions struct {
	CaptureDeltas   bool
	CaptureThinking bool
}

func loadAgentRuntime(workspace string) (*agentRuntime, error) {
	return loadAgentRuntimeWithOptions(workspace, agentRuntimeOptions{})
}

func loadAgentRuntimeWithOptions(workspace string, opts agentRuntimeOptions) (*agentRuntime, error) {
	slog.Debug(
		"agent_runtime_loader: loading runtime",
		"workspace", strings.TrimSpace(workspace),
		"capture_deltas", opts.CaptureDeltas,
		"capture_thinking", opts.CaptureThinking,
	)
	workspaces, err := discoverAgentWorkspaces(workspace)
	if err != nil {
		return nil, err
	}
	slog.Info("agent_runtime_loader: discovered workspaces", "count", len(workspaces), "workspaces", strings.Join(workspaces, ","))

	agents := make(map[sessionrt.ActorID]*agentcore.Agent, len(workspaces))
	executors := make(map[sessionrt.ActorID]sessionrt.AgentExecutor, len(workspaces))

	for _, candidate := range workspaces {
		slog.Debug("agent_runtime_loader: loading workspace", "workspace", candidate)
		agent, err := agentcore.LoadAgent(candidate)
		if err != nil {
			recovered, recoverErr := tryRecoverDefaultMainAgentWorkspace(workspace, workspaces, candidate, err)
			if recoverErr != nil {
				return nil, recoverErr
			}
			if recovered {
				slog.Warn("agent_runtime_loader: recovered default main workspace after load failure", "workspace", candidate)
				agent, err = agentcore.LoadAgent(candidate)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("load agent workspace %s: %w", candidate, err)
		}
		actorID := sessionrt.ActorID(strings.TrimSpace(agent.ID))
		if actorID == "" {
			return nil, fmt.Errorf("agent in workspace %s has empty id", candidate)
		}
		if existing, exists := agents[actorID]; exists {
			return nil, fmt.Errorf("duplicate agent id %q in workspaces %s and %s", actorID, existing.Workspace, candidate)
		}

		agent.CaptureThinkingDeltas = opts.CaptureThinking
		agents[actorID] = agent
		executors[actorID] = agentcore.NewSessionRuntimeAdapterWithOptions(agent, agentcore.SessionRuntimeAdapterOptions{
			CaptureDeltas:   opts.CaptureDeltas,
			CaptureThinking: opts.CaptureThinking,
		})
		slog.Debug("agent_runtime_loader: registered agent executor", "actor_id", actorID, "workspace", candidate)
	}

	actorIDs := make([]string, 0, len(agents))
	for actorID := range agents {
		actorIDs = append(actorIDs, string(actorID))
	}
	sort.Strings(actorIDs)

	knownAgents := append([]string(nil), actorIDs...)
	for actorID, agent := range agents {
		if agent == nil {
			continue
		}
		agent.KnownAgents = append([]string(nil), knownAgents...)
		if strings.TrimSpace(agent.ID) == "" {
			agent.ID = strings.TrimSpace(string(actorID))
		}
	}

	defaultActorID := sessionrt.ActorID(actorIDs[0])

	router, err := agentcore.NewActorExecutorRouter(defaultActorID, executors)
	if err != nil {
		return nil, err
	}
	slog.Info("agent_runtime_loader: runtime loaded", "default_actor_id", defaultActorID, "agents_count", len(agents))
	return &agentRuntime{
		Executor:       router,
		Router:         router,
		DefaultActorID: defaultActorID,
		Agents:         agents,
	}, nil
}

type runtimeConfigProbe struct {
	AgentID     string `json:"agent_id"`
	ModelPolicy string `json:"model_policy"`
}

func tryRecoverDefaultMainAgentWorkspace(root string, workspaces []string, candidate string, loadErr error) (bool, error) {
	if len(workspaces) != 1 {
		return false, nil
	}
	workspaceAbs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return false, fmt.Errorf("resolve workspace for recovery: %w", err)
	}
	expected := filepath.Join(filepath.Clean(workspaceAbs), "agents", defaultAgentWorkspaceID)
	if filepath.Clean(candidate) != expected {
		return false, nil
	}

	probe, probeErr := readRuntimeConfigProbe(candidate)
	if probeErr != nil {
		return false, nil
	}
	if strings.TrimSpace(probe.AgentID) != "" || strings.TrimSpace(probe.ModelPolicy) != "" {
		return false, nil
	}

	if err := ensureAgentWorkspace(defaultAgentWorkspaceID, candidate); err != nil {
		return false, fmt.Errorf("recover main agent workspace %s after load error %v: %w", candidate, loadErr, err)
	}
	configPath := filepath.Join(candidate, "config.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfigTemplate(defaultAgentWorkspaceID)), 0o644); err != nil {
		return false, fmt.Errorf("recover main agent config %s after load error %v: %w", configPath, loadErr, err)
	}
	slog.Warn("agent_runtime_loader: rewrote empty default workspace config during recovery", "workspace", candidate, "config_path", configPath)
	return true, nil
}

func readRuntimeConfigProbe(workspace string) (runtimeConfigProbe, error) {
	var probe runtimeConfigProbe
	path, err := resolveRuntimeConfigPath(workspace)
	if err != nil {
		return probe, err
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return probe, err
	}

	raw := map[string]any{}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		if err := toml.Unmarshal(blob, &raw); err != nil {
			return probe, err
		}
	case ".json":
		if err := json.Unmarshal(blob, &raw); err != nil {
			return probe, err
		}
	default:
		return probe, fmt.Errorf("unsupported config format %q", filepath.Ext(path))
	}
	probe.AgentID = readRuntimeStringField(raw, "agent_id")
	probe.ModelPolicy = readRuntimeStringField(raw, "model_policy")
	return probe, nil
}

func readRuntimeStringField(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	if value, ok := raw[key]; ok {
		if text, ok := value.(string); ok {
			return text
		}
	}
	normalizedTarget := normalizeRuntimeKey(key)
	for candidateKey, value := range raw {
		if normalizeRuntimeKey(candidateKey) != normalizedTarget {
			continue
		}
		if text, ok := value.(string); ok {
			return text
		}
	}
	return ""
}

func normalizeRuntimeKey(key string) string {
	trimmed := strings.TrimSpace(key)
	var b strings.Builder
	b.Grow(len(trimmed))
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if r >= 'A' && r <= 'Z' {
			b.WriteRune(r + ('a' - 'A'))
		}
	}
	return b.String()
}

func resolveRuntimeConfigPath(workspace string) (string, error) {
	for _, name := range []string{"config.toml", "config.json"} {
		path := filepath.Join(workspace, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if info.IsDir() {
			continue
		}
		return path, nil
	}
	return "", fmt.Errorf("required file missing: config.{toml,json}")
}

func discoverAgentWorkspaces(workspace string) (workspaces []string, err error) {
	workspaceAbs, err := filepath.Abs(strings.TrimSpace(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	workspaceAbs = filepath.Clean(workspaceAbs)
	slog.Debug("agent_runtime_loader: discovering workspaces", "workspace", workspaceAbs)

	out := make([]string, 0, 8)
	seen := map[string]struct{}{}
	addCandidate := func(path string) {
		if _, exists := seen[path]; exists {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}

	agentsRoot := filepath.Join(workspaceAbs, "agents")
	ensureSharedProfile := func(agentWorkspace string) error {
		if err := ensureSharedUserProfile(agentsRoot, agentWorkspace); err != nil {
			return fmt.Errorf("ensure shared user profile for %s: %w", agentWorkspace, err)
		}
		return nil
	}
	entries, readErr := os.ReadDir(agentsRoot)
	if readErr != nil {
		if !os.IsNotExist(readErr) {
			return nil, fmt.Errorf("read agent workspace directory %s: %w", agentsRoot, readErr)
		}
		readErr = nil
	}
	if readErr == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Clean(filepath.Join(agentsRoot, entry.Name()))
			ok, err := hasAgentWorkspaceFiles(candidate)
			if err != nil {
				return nil, err
			}
			if ok {
				if err := ensureWorkspaceTemplateUpdateInstructions(candidate); err != nil {
					return nil, fmt.Errorf("ensure template update instructions for %s: %w", candidate, err)
				}
				if err := ensureSharedProfile(candidate); err != nil {
					return nil, err
				}
				addCandidate(candidate)
			}
		}
	}

	if len(out) == 0 {
		defaultWorkspace := filepath.Join(agentsRoot, defaultAgentWorkspaceID)
		if err := ensureAgentWorkspace(defaultAgentWorkspaceID, defaultWorkspace); err != nil {
			return nil, fmt.Errorf("create main agent workspace %s: %w", defaultWorkspace, err)
		}
		if err := ensureSharedProfile(defaultWorkspace); err != nil {
			return nil, err
		}
		addCandidate(defaultWorkspace)
		slog.Info("agent_runtime_loader: created default main workspace", "workspace", defaultWorkspace)
	}
	sort.Strings(out)
	slog.Debug("agent_runtime_loader: workspace discovery complete", "count", len(out))
	return out, nil
}

func hasAgentWorkspaceFiles(workspace string) (bool, error) {
	for _, name := range []string{"config.toml", "config.json"} {
		path := filepath.Join(workspace, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, fmt.Errorf("stat %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		return true, nil
	}
	return false, nil
}
