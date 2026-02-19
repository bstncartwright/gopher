package main

type gatewayAgentRuntime = agentRuntime

func loadGatewayAgentRuntime(workspace string) (*gatewayAgentRuntime, error) {
	return loadGatewayAgentRuntimeWithOptions(workspace, agentRuntimeOptions{})
}

func loadGatewayAgentRuntimeWithOptions(workspace string, opts agentRuntimeOptions) (*gatewayAgentRuntime, error) {
	return loadAgentRuntimeWithOptions(workspace, opts)
}

func discoverGatewayAgentWorkspaces(workspace string) (workspaces []string, err error) {
	return discoverAgentWorkspaces(workspace)
}
