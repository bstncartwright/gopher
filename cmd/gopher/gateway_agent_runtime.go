package main

import "log/slog"

type gatewayAgentRuntime = agentRuntime

func loadGatewayAgentRuntime(workspace string) (*gatewayAgentRuntime, error) {
	slog.Debug("gateway_agent_runtime: loading with default options", "workspace", workspace)
	return loadGatewayAgentRuntimeWithOptions(workspace, agentRuntimeOptions{})
}

func loadGatewayAgentRuntimeWithOptions(workspace string, opts agentRuntimeOptions) (*gatewayAgentRuntime, error) {
	slog.Debug(
		"gateway_agent_runtime: loading with options",
		"workspace", workspace,
		"capture_deltas", opts.CaptureDeltas,
		"capture_thinking", opts.CaptureThinking,
	)
	return loadAgentRuntimeWithOptions(workspace, opts)
}

func discoverGatewayAgentWorkspaces(workspace string) (workspaces []string, err error) {
	slog.Debug("gateway_agent_runtime: discovering workspaces", "workspace", workspace)
	return discoverAgentWorkspaces(workspace)
}
