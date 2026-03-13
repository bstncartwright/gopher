package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type CapabilityResolver func(input sessionrt.AgentInput) []scheduler.Capability
type RemoteActorLocator func(actorID sessionrt.ActorID) (scheduler.NodeInfo, bool)

type DistributedExecutorOptions struct {
	GatewayNodeID      string
	LocalExecutor      sessionrt.AgentExecutor
	Scheduler          *scheduler.Scheduler
	Fabric             fabricts.Fabric
	CapabilityResolver CapabilityResolver
	RemoteActorLocator RemoteActorLocator
	AuthEnvKeys        []string
	EnvLookup          func(string) string
}

type DistributedExecutor struct {
	gatewayNodeID string
	local         sessionrt.AgentExecutor
	scheduler     *scheduler.Scheduler
	fabric        fabricts.Fabric
	resolve       CapabilityResolver
	locateActor   RemoteActorLocator
	authEnvKeys   []string
	envLookup     func(string) string
}

var _ sessionrt.AgentExecutor = (*DistributedExecutor)(nil)
var _ sessionrt.StreamingAgentExecutor = (*DistributedExecutor)(nil)

const (
	shareAuthEnvEnabledVar = "GOPHER_GATEWAY_SHARE_AUTH_ENV"
	shareAuthEnvKeysVar    = "GOPHER_GATEWAY_SHARED_AUTH_ENV_KEYS"
)

var defaultSharedAuthEnvKeys = []string{
	"OPENAI_API_KEY",
	"ZAI_API_KEY",
	"EXA_API_KEY",
	"TAVILY_API_KEY",
	"KIMI_API_KEY",
	"ANTHROPIC_API_KEY",
	"OLLAMA_API_KEY",
	"OPENAI_CODEX_API_KEY",
	"OPENAI_CODEX_TOKEN",
	"OPENAI_CODEX_REFRESH_TOKEN",
	"OPENAI_CODEX_TOKEN_EXPIRES",
}

func NewDistributedExecutor(opts DistributedExecutorOptions) (*DistributedExecutor, error) {
	if opts.LocalExecutor == nil {
		slog.Error("distributed_executor: local executor is required")
		return nil, fmt.Errorf("local executor is required")
	}
	if opts.Scheduler == nil {
		slog.Error("distributed_executor: scheduler is required")
		return nil, fmt.Errorf("scheduler is required")
	}
	if opts.Fabric == nil {
		slog.Error("distributed_executor: fabric is required")
		return nil, fmt.Errorf("fabric is required")
	}
	resolver := opts.CapabilityResolver
	if resolver == nil {
		resolver = func(sessionrt.AgentInput) []scheduler.Capability { return nil }
	}
	envLookup := opts.EnvLookup
	if envLookup == nil {
		envLookup = os.Getenv
	}
	authEnvKeys := normalizeEnvKeys(opts.AuthEnvKeys)
	if authEnvKeys == nil {
		authEnvKeys = defaultAuthEnvKeys(envLookup)
	}
	slog.Info(
		"distributed_executor: initialized",
		"gateway_node_id", strings.TrimSpace(opts.GatewayNodeID),
		"shared_auth_env_keys", len(authEnvKeys),
		"remote_actor_locator", opts.RemoteActorLocator != nil,
	)
	return &DistributedExecutor{
		gatewayNodeID: strings.TrimSpace(opts.GatewayNodeID),
		local:         opts.LocalExecutor,
		scheduler:     opts.Scheduler,
		fabric:        opts.Fabric,
		resolve:       resolver,
		locateActor:   opts.RemoteActorLocator,
		authEnvKeys:   authEnvKeys,
		envLookup:     envLookup,
	}, nil
}

func (e *DistributedExecutor) Step(ctx context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	selection, err := e.selectTarget(input)
	if err != nil {
		slog.Error("distributed_executor: failed to select target", "session_id", input.SessionID, "actor_id", input.ActorID, "error", err)
		return sessionrt.AgentOutput{}, err
	}
	slog.Debug("distributed_executor: selected target", "session_id", input.SessionID, "actor_id", input.ActorID, "location", selection.Location, "node_id", selection.NodeID)
	if selection.Location == scheduler.ExecGateway || selection.NodeID == "" || selection.NodeID == e.gatewayNodeID {
		slog.Debug("distributed_executor: executing locally", "session_id", input.SessionID, "actor_id", input.ActorID)
		return e.local.Step(ctx, input)
	}
	return e.stepRemote(ctx, input, selection.NodeID)
}

func (e *DistributedExecutor) StepStream(ctx context.Context, input sessionrt.AgentInput, emit sessionrt.AgentEventEmitter) error {
	selection, err := e.selectTarget(input)
	if err != nil {
		slog.Error("distributed_executor: failed to select target for stream", "session_id", input.SessionID, "actor_id", input.ActorID, "error", err)
		return err
	}
	slog.Debug("distributed_executor: selected target for stream", "session_id", input.SessionID, "actor_id", input.ActorID, "location", selection.Location, "node_id", selection.NodeID)
	if selection.Location == scheduler.ExecGateway || selection.NodeID == "" || selection.NodeID == e.gatewayNodeID {
		if streaming, ok := e.local.(sessionrt.StreamingAgentExecutor); ok {
			slog.Debug("distributed_executor: streaming locally", "session_id", input.SessionID, "actor_id", input.ActorID)
			return streaming.StepStream(ctx, input, emit)
		}
		out, stepErr := e.local.Step(ctx, input)
		if stepErr != nil {
			return stepErr
		}
		return emitOutput(out, emit)
	}

	out, err := e.stepRemote(ctx, input, selection.NodeID)
	if err != nil {
		return err
	}
	return emitOutput(out, emit)
}

func (e *DistributedExecutor) selectTarget(input sessionrt.AgentInput) (scheduler.Selection, error) {
	if e.locateActor != nil {
		if nodeInfo, ok := e.locateActor(input.ActorID); ok {
			location := scheduler.ExecNode
			if nodeInfo.NodeID == "" || nodeInfo.NodeID == e.gatewayNodeID {
				location = scheduler.ExecGateway
			}
			slog.Debug("distributed_executor: actor located explicitly", "actor_id", input.ActorID, "node_id", nodeInfo.NodeID, "location", location)
			return scheduler.Selection{Location: location, NodeID: nodeInfo.NodeID}, nil
		}
	}
	required := e.resolve(input)
	slog.Debug("distributed_executor: selecting scheduler target", "actor_id", input.ActorID, "required_capabilities", required)
	return e.scheduler.Select(scheduler.SelectionRequest{RequiredCapabilities: required})
}

func (e *DistributedExecutor) stepRemote(ctx context.Context, input sessionrt.AgentInput, nodeID string) (sessionrt.AgentOutput, error) {
	slog.Info("distributed_executor: executing remotely", "session_id", input.SessionID, "actor_id", input.ActorID, "node_id", nodeID, "history_count", len(input.History))
	request := node.ExecutionRequest{
		SessionID: input.SessionID,
		ActorID:   input.ActorID,
		History:   input.History,
		AuthEnv:   e.sharedAuthEnv(),
	}
	blob, err := json.Marshal(request)
	if err != nil {
		slog.Error("distributed_executor: failed to marshal execution request", "session_id", input.SessionID, "actor_id", input.ActorID, "node_id", nodeID, "error", err)
		return sessionrt.AgentOutput{}, fmt.Errorf("marshal execution request: %w", err)
	}

	responseBlob, err := e.fabric.Request(ctx, fabricts.NodeControlSubject(nodeID), blob)
	if err != nil {
		slog.Error("distributed_executor: remote request failed", "session_id", input.SessionID, "actor_id", input.ActorID, "node_id", nodeID, "error", err)
		return sessionrt.AgentOutput{}, fmt.Errorf("remote node %s request failed: %w", nodeID, err)
	}
	var response node.ExecutionResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		slog.Error("distributed_executor: failed to decode remote response", "session_id", input.SessionID, "actor_id", input.ActorID, "node_id", nodeID, "error", err)
		return sessionrt.AgentOutput{}, fmt.Errorf("decode remote node %s response: %w", nodeID, err)
	}
	if strings.TrimSpace(response.Error) != "" {
		slog.Error("distributed_executor: remote node returned error", "session_id", input.SessionID, "actor_id", input.ActorID, "node_id", nodeID, "error", strings.TrimSpace(response.Error))
		return sessionrt.AgentOutput{}, fmt.Errorf("remote node %s: %s", nodeID, response.Error)
	}
	slog.Info("distributed_executor: remote execution completed", "session_id", input.SessionID, "actor_id", input.ActorID, "node_id", nodeID, "events", len(response.Events))
	return sessionrt.AgentOutput{Events: response.Events}, nil
}

func emitOutput(output sessionrt.AgentOutput, emit sessionrt.AgentEventEmitter) error {
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

func (e *DistributedExecutor) sharedAuthEnv() map[string]string {
	if e == nil || len(e.authEnvKeys) == 0 || e.envLookup == nil {
		return nil
	}
	shared := make(map[string]string, len(e.authEnvKeys))
	for _, key := range e.authEnvKeys {
		value := strings.TrimSpace(e.envLookup(key))
		if value == "" {
			continue
		}
		shared[key] = value
	}
	if len(shared) == 0 {
		return nil
	}
	slog.Debug("distributed_executor: sharing auth env keys", "count", len(shared))
	return shared
}

func defaultAuthEnvKeys(lookup func(string) string) []string {
	if lookup == nil {
		return append([]string(nil), defaultSharedAuthEnvKeys...)
	}
	if raw := strings.TrimSpace(lookup(shareAuthEnvEnabledVar)); raw != "" {
		if enabled, err := strconv.ParseBool(raw); err == nil && !enabled {
			return nil
		}
	}
	if raw := strings.TrimSpace(lookup(shareAuthEnvKeysVar)); raw != "" {
		return normalizeEnvKeys(strings.Split(raw, ","))
	}
	return append([]string(nil), defaultSharedAuthEnvKeys...)
}

func normalizeEnvKeys(keys []string) []string {
	if keys == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		normalized := strings.TrimSpace(key)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}
