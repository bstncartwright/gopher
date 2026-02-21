package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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
	AuthEnvKeys        []string
	EnvLookup          func(string) string
}

type DistributedExecutor struct {
	gatewayNodeID string
	local         sessionrt.AgentExecutor
	scheduler     *scheduler.Scheduler
	fabric        fabricts.Fabric
	resolve       CapabilityResolver
	authEnvKeys   []string
	envLookup     func(string) string
}

var _ sessionrt.AgentExecutor = (*DistributedExecutor)(nil)

const (
	shareAuthEnvEnabledVar = "GOPHER_GATEWAY_SHARE_AUTH_ENV"
	shareAuthEnvKeysVar    = "GOPHER_GATEWAY_SHARED_AUTH_ENV_KEYS"
)

var defaultSharedAuthEnvKeys = []string{
	"OPENAI_API_KEY",
	"ZAI_API_KEY",
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
	envLookup := opts.EnvLookup
	if envLookup == nil {
		envLookup = os.Getenv
	}
	authEnvKeys := normalizeEnvKeys(opts.AuthEnvKeys)
	if authEnvKeys == nil {
		authEnvKeys = defaultAuthEnvKeys(envLookup)
	}
	return &DistributedExecutor{
		gatewayNodeID: strings.TrimSpace(opts.GatewayNodeID),
		local:         opts.LocalExecutor,
		scheduler:     opts.Scheduler,
		fabric:        opts.Fabric,
		resolve:       resolver,
		authEnvKeys:   authEnvKeys,
		envLookup:     envLookup,
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
		AuthEnv:   e.sharedAuthEnv(),
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
