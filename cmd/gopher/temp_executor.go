package main

import (
	"context"
	"fmt"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type tempExecutor struct{}

func newTempExecutor() sessionrt.AgentExecutor {
	return &tempExecutor{}
}

func (e *tempExecutor) Step(_ context.Context, _ sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{}, fmt.Errorf("gateway executor is not configured yet")
}
