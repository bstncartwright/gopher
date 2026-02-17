package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type CapabilityResolver func(input sessionrt.AgentInput) []scheduler.Capability

type DistributedExecutorOptions struct {
	GatewayNodeID      string
	LocalExecutor      sessionrt.AgentExecutor
	Scheduler          *scheduler.Scheduler
	Fabric             fabricts.Fabric
	CapabilityResolver CapabilityResolver
}

type DistributedExecutor struct {
	gatewayNodeID string
	local         sessionrt.AgentExecutor
	scheduler     *scheduler.Scheduler
	fabric        fabricts.Fabric
	resolve       CapabilityResolver
}

var _ sessionrt.AgentExecutor = (*DistributedExecutor)(nil)

func NewDistributedExecutor(opts DistributedExecutorOptions) (*DistributedExecutor, error) {
	if opts.LocalExecutor == nil {
		return nil, fmt.Errorf("local executor is required")
	}
	if opts.Scheduler == nil {
		return nil, fmt.Errorf("scheduler is required")
	}
	if opts.Fabric == nil {
		return nil, fmt.Errorf("fabric is required")
	}
	resolver := opts.CapabilityResolver
	if resolver == nil {
		resolver = func(sessionrt.AgentInput) []scheduler.Capability { return nil }
	}
	return &DistributedExecutor{
		gatewayNodeID: strings.TrimSpace(opts.GatewayNodeID),
		local:         opts.LocalExecutor,
		scheduler:     opts.Scheduler,
		fabric:        opts.Fabric,
		resolve:       resolver,
	}, nil
}

func (e *DistributedExecutor) Step(ctx context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	required := e.resolve(input)
	selection, err := e.scheduler.Select(scheduler.SelectionRequest{RequiredCapabilities: required})
	if err != nil {
		return sessionrt.AgentOutput{}, err
	}
	if selection.Location == scheduler.ExecGateway || selection.NodeID == "" || selection.NodeID == e.gatewayNodeID {
		return e.local.Step(ctx, input)
	}

	request := node.ExecutionRequest{
		SessionID: input.SessionID,
		ActorID:   input.ActorID,
		History:   input.History,
	}
	blob, err := json.Marshal(request)
	if err != nil {
		return sessionrt.AgentOutput{}, fmt.Errorf("marshal execution request: %w", err)
	}

	responseBlob, err := e.fabric.Request(ctx, fabricts.NodeControlSubject(selection.NodeID), blob)
	if err != nil {
		return sessionrt.AgentOutput{}, fmt.Errorf("remote node %s request failed: %w", selection.NodeID, err)
	}
	var response node.ExecutionResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		return sessionrt.AgentOutput{}, fmt.Errorf("decode remote node %s response: %w", selection.NodeID, err)
	}
	if strings.TrimSpace(response.Error) != "" {
		return sessionrt.AgentOutput{}, fmt.Errorf("remote node %s: %s", selection.NodeID, response.Error)
	}
	return sessionrt.AgentOutput{Events: response.Events}, nil
}
