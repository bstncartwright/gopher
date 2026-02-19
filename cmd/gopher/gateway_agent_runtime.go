package main

type gatewayAgentRuntime = agentRuntime

func loadGatewayAgentRuntime(workspace string) (*gatewayAgentRuntime, error) {
	return loadAgentRuntime(workspace)
}

func discoverGatewayAgentWorkspaces(workspace string) (workspaces []string, err error) {
	return discoverAgentWorkspaces(workspace)
}
