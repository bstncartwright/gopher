package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type failingLocalExecutor struct{}

func (f *failingLocalExecutor) Step(context.Context, sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{}, context.DeadlineExceeded
}

func TestDistributedExecutorSharesAuthEnvWithRemoteNode(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	registry := scheduler.NewRegistry(0)
	registry.Upsert(scheduler.NodeInfo{
		NodeID:       "gateway",
		IsGateway:    true,
		Capabilities: []scheduler.Capability{{Kind: scheduler.CapabilityAgent, Name: "agent"}},
	})
	registry.Upsert(scheduler.NodeInfo{
		NodeID:       "node-remote",
		Capabilities: []scheduler.Capability{{Kind: scheduler.CapabilityTool, Name: "gpu"}},
	})
	sched := scheduler.NewScheduler("gateway", registry)

	requests := make(chan node.ExecutionRequest, 1)
	_, err := fabric.Subscribe(fabricts.NodeControlSubject("node-remote"), func(ctx context.Context, message fabricts.Message) {
		var request node.ExecutionRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			t.Errorf("unmarshal request: %v", err)
			return
		}
		requests <- request

		response := node.ExecutionResponse{
			Events: []sessionrt.Event{{
				Type: sessionrt.EventMessage,
				From: request.ActorID,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: "remote",
				},
			}},
		}
		blob, err := json.Marshal(response)
		if err != nil {
			t.Errorf("marshal response: %v", err)
			return
		}
		_ = fabric.Publish(ctx, fabricts.Message{Subject: message.Reply, Data: blob})
	})
	if err != nil {
		t.Fatalf("subscribe remote node subject: %v", err)
	}

	env := map[string]string{
		"OPENAI_API_KEY": "gateway-openai-key",
		"ZAI_API_KEY":    "",
	}
	distributed, err := NewDistributedExecutor(DistributedExecutorOptions{
		GatewayNodeID: "gateway",
		LocalExecutor: &failingLocalExecutor{},
		Scheduler:     sched,
		Fabric:        fabric,
		CapabilityResolver: func(sessionrt.AgentInput) []scheduler.Capability {
			return []scheduler.Capability{{Kind: scheduler.CapabilityTool, Name: "gpu"}}
		},
		AuthEnvKeys: []string{"OPENAI_API_KEY", "ZAI_API_KEY"},
		EnvLookup: func(key string) string {
			return env[key]
		},
	})
	if err != nil {
		t.Fatalf("NewDistributedExecutor() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = distributed.Step(ctx, sessionrt.AgentInput{
		SessionID: "session-1",
		ActorID:   "agent:test",
	})
	if err != nil {
		t.Fatalf("distributed.Step() error: %v", err)
	}

	select {
	case request := <-requests:
		if got := request.AuthEnv["OPENAI_API_KEY"]; got != "gateway-openai-key" {
			t.Fatalf("shared OPENAI_API_KEY = %q, want %q", got, "gateway-openai-key")
		}
		if _, ok := request.AuthEnv["ZAI_API_KEY"]; ok {
			t.Fatalf("expected empty ZAI_API_KEY to be omitted, got auth_env=%v", request.AuthEnv)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for remote execution request: %v", ctx.Err())
	}
}
