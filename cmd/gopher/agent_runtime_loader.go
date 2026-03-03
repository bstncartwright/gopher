package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
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
	workspaces, err := discoverAgentWorkspaces(workspace)
	if err != nil {
		return nil, err
	}

	agents := make(map[sessionrt.ActorID]*agentcore.Agent, len(workspaces))
	executors := make(map[sessionrt.ActorID]sessionrt.AgentExecutor, len(workspaces))

	for _, candidate := range workspaces {
		agent, err := agentcore.LoadAgent(candidate)
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
	return &agentRuntime{
		Executor:       router,
		Router:         router,
		DefaultActorID: defaultActorID,
		Agents:         agents,
	}, nil
}

func discoverAgentWorkspaces(workspace string) (workspaces []string, err error) {
	workspaceAbs, err := filepath.Abs(strings.TrimSpace(workspace))
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	workspaceAbs = filepath.Clean(workspaceAbs)

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
				addCandidate(candidate)
			}
		}
	}

	if len(out) == 0 {
		defaultWorkspace := filepath.Join(agentsRoot, defaultAgentWorkspaceID)
		if err := ensureAgentWorkspace(defaultAgentWorkspaceID, defaultWorkspace); err != nil {
			return nil, fmt.Errorf("create main agent workspace %s: %w", defaultWorkspace, err)
		}
		addCandidate(defaultWorkspace)
	}
	sort.Strings(out)
	return out, nil
}

func hasAgentWorkspaceFiles(workspace string) (bool, error) {
	required := [][]string{
		{"config.toml", "config.json"},
		{"policies.toml", "policies.json"},
	}
	for _, candidates := range required {
		found := false
		for _, name := range candidates {
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
			found = true
			break
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}
