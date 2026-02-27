package agentcore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

// ActorExecutorRouter dispatches session runtime steps to actor-specific executors.
type ActorExecutorRouter struct {
	mu           sync.RWMutex
	defaultActor sessionrt.ActorID
	executors    map[sessionrt.ActorID]sessionrt.AgentExecutor
	aliases      map[sessionrt.ActorID]sessionrt.ActorID
}

var _ sessionrt.AgentExecutor = (*ActorExecutorRouter)(nil)
var _ sessionrt.StreamingAgentExecutor = (*ActorExecutorRouter)(nil)

func NewActorExecutorRouter(defaultActor sessionrt.ActorID, executors map[sessionrt.ActorID]sessionrt.AgentExecutor) (*ActorExecutorRouter, error) {
	if len(executors) == 0 {
		return nil, fmt.Errorf("at least one actor executor is required")
	}

	normalized := make(map[sessionrt.ActorID]sessionrt.AgentExecutor, len(executors))
	for actorID, executor := range executors {
		id := normalizeActorID(actorID)
		if id == "" {
			return nil, fmt.Errorf("actor id is required")
		}
		if executor == nil {
			return nil, fmt.Errorf("executor for actor %q is required", id)
		}
		if _, exists := normalized[id]; exists {
			return nil, fmt.Errorf("duplicate executor for actor %q", id)
		}
		normalized[id] = executor
	}

	resolvedDefault := normalizeActorID(defaultActor)
	if resolvedDefault == "" && len(normalized) == 1 {
		for actorID := range normalized {
			resolvedDefault = actorID
		}
	}
	if resolvedDefault != "" {
		if _, ok := normalized[resolvedDefault]; !ok {
			return nil, fmt.Errorf("default actor %q is not registered", resolvedDefault)
		}
	}

	router := &ActorExecutorRouter{
		defaultActor: resolvedDefault,
		executors:    make(map[sessionrt.ActorID]sessionrt.AgentExecutor, len(normalized)),
		aliases:      make(map[sessionrt.ActorID]sessionrt.ActorID, len(normalized)*2),
	}
	for actorID, executor := range normalized {
		if err := router.registerActorLocked(actorID, executor); err != nil {
			return nil, err
		}
	}
	return router, nil
}

func (r *ActorExecutorRouter) Step(ctx context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	executor, resolvedActor, err := r.resolveExecutor(input.ActorID)
	if err != nil {
		return sessionrt.AgentOutput{}, err
	}
	input.ActorID = resolvedActor
	return executor.Step(ctx, input)
}

func (r *ActorExecutorRouter) StepStream(ctx context.Context, input sessionrt.AgentInput, emit sessionrt.AgentEventEmitter) error {
	executor, resolvedActor, err := r.resolveExecutor(input.ActorID)
	if err != nil {
		return err
	}
	input.ActorID = resolvedActor

	if streaming, ok := executor.(sessionrt.StreamingAgentExecutor); ok {
		return streaming.StepStream(ctx, input, emit)
	}

	output, err := executor.Step(ctx, input)
	if err != nil {
		return err
	}
	if emit == nil {
		return nil
	}
	for _, event := range output.Events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func (r *ActorExecutorRouter) KnownActors() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.knownActorsLocked()
}

func (r *ActorExecutorRouter) RegisterActor(actorID sessionrt.ActorID, executor sessionrt.AgentExecutor) error {
	id := normalizeActorID(actorID)
	if id == "" {
		return fmt.Errorf("actor id is required")
	}
	if executor == nil {
		return fmt.Errorf("executor for actor %q is required", id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registerActorLocked(id, executor)
}

func (r *ActorExecutorRouter) UnregisterActor(actorID sessionrt.ActorID) bool {
	id := normalizeActorID(actorID)
	if id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.executors[id]; !exists {
		return false
	}
	delete(r.executors, id)
	for alias, canonical := range r.aliases {
		if canonical == id {
			delete(r.aliases, alias)
		}
	}
	if r.defaultActor == id {
		r.defaultActor = ""
		actors := r.knownActorsLocked()
		if len(actors) > 0 {
			r.defaultActor = sessionrt.ActorID(actors[0])
		}
	}
	return true
}

func (r *ActorExecutorRouter) knownActorsLocked() []string {
	keys := make([]string, 0, len(r.executors))
	for actorID := range r.executors {
		keys = append(keys, string(actorID))
	}
	sort.Strings(keys)
	return keys
}

func (r *ActorExecutorRouter) registerActorLocked(actorID sessionrt.ActorID, executor sessionrt.AgentExecutor) error {
	if _, exists := r.executors[actorID]; exists {
		return fmt.Errorf("executor for actor %q already registered", actorID)
	}
	r.executors[actorID] = executor
	r.aliases[actorID] = actorID
	alt := alternateActorID(actorID)
	if alt != "" {
		if _, exists := r.aliases[alt]; !exists {
			r.aliases[alt] = actorID
		}
	}
	if r.defaultActor == "" {
		r.defaultActor = actorID
	}
	return nil
}

func normalizeActorID(actorID sessionrt.ActorID) sessionrt.ActorID {
	value := strings.TrimSpace(string(actorID))
	if value == "" {
		return ""
	}
	return sessionrt.ActorID(value)
}

func alternateActorID(actorID sessionrt.ActorID) sessionrt.ActorID {
	value := string(actorID)
	if strings.HasPrefix(value, "agent:") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(value, "agent:"))
		if trimmed == "" {
			return ""
		}
		return sessionrt.ActorID(trimmed)
	}
	return sessionrt.ActorID("agent:" + value)
}

func (r *ActorExecutorRouter) resolveExecutor(actorID sessionrt.ActorID) (sessionrt.AgentExecutor, sessionrt.ActorID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	normalizedActorID := normalizeActorID(actorID)
	if normalizedActorID == "" {
		normalizedActorID = r.defaultActor
	}
	if normalizedActorID == "" {
		return nil, "", fmt.Errorf("actor id is required")
	}

	resolvedActor := normalizedActorID
	if canonical, ok := r.aliases[normalizedActorID]; ok {
		resolvedActor = canonical
	}

	executor, ok := r.executors[resolvedActor]
	if !ok {
		return nil, "", fmt.Errorf("unknown actor %q (known: %s)", normalizedActorID, strings.Join(r.knownActorsLocked(), ", "))
	}
	return executor, resolvedActor, nil
}
