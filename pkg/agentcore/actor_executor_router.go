package agentcore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

// ActorExecutorRouter dispatches session runtime steps to actor-specific executors.
type ActorExecutorRouter struct {
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
		executors:    normalized,
		aliases:      map[sessionrt.ActorID]sessionrt.ActorID{},
	}
	for actorID := range normalized {
		router.aliases[actorID] = actorID
		alt := alternateActorID(actorID)
		if alt != "" {
			if _, exists := router.aliases[alt]; !exists {
				router.aliases[alt] = actorID
			}
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
	keys := make([]string, 0, len(r.executors))
	for actorID := range r.executors {
		keys = append(keys, string(actorID))
	}
	sort.Strings(keys)
	return keys
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
		return nil, "", fmt.Errorf("unknown actor %q (known: %s)", normalizedActorID, strings.Join(r.KnownActors(), ", "))
	}
	return executor, resolvedActor, nil
}
