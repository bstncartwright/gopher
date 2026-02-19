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

type agentRuntime struct {
	Executor       sessionrt.AgentExecutor
	DefaultActorID sessionrt.ActorID
	Agents         map[sessionrt.ActorID]*agentcore.Agent
}

func loadAgentRuntime(workspace string) (*agentRuntime, error) {
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

		agents[actorID] = agent
		executors[actorID] = agentcore.NewSessionRuntimeAdapter(agent)
	}

	actorIDs := make([]string, 0, len(agents))
	for actorID := range agents {
		actorIDs = append(actorIDs, string(actorID))
	}
	sort.Strings(actorIDs)
	defaultActorID := sessionrt.ActorID(actorIDs[0])

	router, err := agentcore.NewActorExecutorRouter(defaultActorID, executors)
	if err != nil {
		return nil, err
	}
	return &agentRuntime{
		Executor:       router,
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
		return nil, fmt.Errorf("no agent workspace found: expected %s/<agent_id>", agentsRoot)
	}
	sort.Strings(out)
	return out, nil
}

func hasAgentWorkspaceFiles(workspace string) (bool, error) {
	required := []string{"config.json", "policies.json"}
	for _, name := range required {
		path := filepath.Join(workspace, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("stat %s: %w", path, err)
		}
		if info.IsDir() {
			return false, nil
		}
	}
	return true, nil
}
