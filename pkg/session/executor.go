package session

import "context"

type AgentExecutor interface {
	Step(ctx context.Context, input AgentInput) (AgentOutput, error)
}

type AgentInput struct {
	SessionID SessionID
	ActorID   ActorID
	History   []Event
}

type AgentOutput struct {
	Events []Event
}
