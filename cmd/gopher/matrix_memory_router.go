package main

import (
	"context"
	"strings"

	"github.com/bstncartwright/gopher/pkg/memory"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type gatewayMemoryRouter struct {
	defaultManager memory.MemoryManager
	byAgent        map[string]memory.MemoryManager
}

var _ memory.MemoryManager = (*gatewayMemoryRouter)(nil)

func newGatewayMemoryRouter(runtime *gatewayAgentRuntime) memory.MemoryManager {
	if runtime == nil {
		return nil
	}
	byAgent := map[string]memory.MemoryManager{}
	var defaultManager memory.MemoryManager
	for actorID, agent := range runtime.Agents {
		if agent == nil || agent.LongTermMemory == nil {
			continue
		}
		manager := agent.LongTermMemory
		normalized := strings.TrimSpace(string(actorID))
		if normalized != "" {
			byAgent[normalized] = manager
			byAgent[strings.TrimPrefix(normalized, "agent:")] = manager
		}
		if defaultManager == nil {
			defaultManager = manager
		}
	}
	if defaultManager == nil {
		return nil
	}
	return &gatewayMemoryRouter{
		defaultManager: defaultManager,
		byAgent:        byAgent,
	}
}

func (r *gatewayMemoryRouter) Store(ctx context.Context, record memory.MemoryRecord) error {
	if r == nil {
		return nil
	}
	manager := r.resolveManager(record.AgentID)
	if manager == nil {
		return nil
	}
	return manager.Store(ctx, record)
}

func (r *gatewayMemoryRouter) Retrieve(ctx context.Context, query memory.MemoryQuery) ([]memory.MemoryRecord, error) {
	if r == nil {
		return nil, nil
	}
	manager := r.resolveManager(query.AgentID)
	if manager == nil {
		return nil, nil
	}
	return manager.Retrieve(ctx, query)
}

func (r *gatewayMemoryRouter) resolveManager(agentID string) memory.MemoryManager {
	if r == nil {
		return nil
	}
	id := strings.TrimSpace(agentID)
	if id != "" {
		if manager, ok := r.byAgent[id]; ok {
			return manager
		}
		if manager, ok := r.byAgent[strings.TrimPrefix(id, "agent:")]; ok {
			return manager
		}
	}
	return r.defaultManager
}

type sessionPublisherFlusher struct {
	flusher interface {
		FlushSession(ctx context.Context, sessionID sessionrt.SessionID) error
	}
}

func (f *sessionPublisherFlusher) FlushSession(ctx context.Context, sessionID string) error {
	if f == nil || f.flusher == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	return f.flusher.FlushSession(ctx, sessionrt.SessionID(strings.TrimSpace(sessionID)))
}
