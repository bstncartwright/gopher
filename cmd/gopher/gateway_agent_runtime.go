package main

import sessionrt "github.com/bstncartwright/gopher/pkg/session"

type gatewayAgentRuntime = agentRuntime

func loadGatewayRuntimeExecutor(workspace string) (sessionrt.AgentExecutor, error) {
	runtime, err := loadAgentRuntime(workspace)
	if err != nil {
		return nil, err
	}
	return runtime.Executor, nil
}

func loadGatewayAgentRuntime(workspace string) (*gatewayAgentRuntime, error) {
	return loadAgentRuntime(workspace)
}

func discoverGatewayAgentWorkspaces(workspace string) (workspaces []string, err error) {
	return discoverAgentWorkspaces(workspace)
}
