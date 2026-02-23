package session

import "context"

type AgentExecutor interface {
	Step(ctx context.Context, input AgentInput) (AgentOutput, error)
}

type AgentEventEmitter func(Event) error

type StreamingAgentExecutor interface {
	StepStream(ctx context.Context, input AgentInput, emit AgentEventEmitter) error
}

type AgentInput struct {
	SessionID SessionID
	ActorID   ActorID
	History   []Event
}

type AgentOutput struct {
	Events []Event
}
