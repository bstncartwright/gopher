package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type envReadingExecutor struct {
	key string
}

func (e *envReadingExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	value := os.Getenv(e.key)
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{{
			Type: sessionrt.EventMessage,
			From: input.ActorID,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleAgent,
				Content: value,
			},
		}},
	}, nil
}

type envReadingFailingExecutor struct {
	key string
}

func (e *envReadingFailingExecutor) Step(context.Context, sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{}, fmt.Errorf("saw %s", os.Getenv(e.key))
}

func TestRuntimeAppliesAndRestoresAuthEnvForExecution(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "node-local-key")

	fabric := fabricts.NewInMemoryBus()
	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:   "node-auth",
		Fabric:   fabric,
		Executor: &envReadingExecutor{key: "OPENAI_API_KEY"},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime.Start() error: %v", err)
	}
	defer runtime.Stop()

	request := ExecutionRequest{
		SessionID: "s1",
		ActorID:   "agent:a",
		AuthEnv: map[string]string{
			"OPENAI_API_KEY": "gateway-shared-key",
		},
	}
	blob, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	responseBlob, err := fabric.Request(ctx, fabricts.NodeControlSubject("node-auth"), blob)
	if err != nil {
		t.Fatalf("fabric.Request() error: %v", err)
	}
	var response ExecutionResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		t.Fatalf("decode execution response: %v", err)
	}
	if response.Error != "" {
		t.Fatalf("unexpected runtime error: %s", response.Error)
	}
	if len(response.Events) != 1 {
		t.Fatalf("events count = %d, want 1", len(response.Events))
	}
	content, ok := messageContent(response.Events[0].Payload)
	if !ok {
		t.Fatalf("response payload is not a message: %#v", response.Events[0].Payload)
	}
	if content != "gateway-shared-key" {
		t.Fatalf("executor saw %q, want gateway-shared-key", content)
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "node-local-key" {
		t.Fatalf("OPENAI_API_KEY after execution = %q, want node-local-key", got)
	}
}

func TestRuntimeRestoresAuthEnvWhenExecutionFails(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "node-local-key")

	fabric := fabricts.NewInMemoryBus()
	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:   "node-auth-fail",
		Fabric:   fabric,
		Executor: &envReadingFailingExecutor{key: "OPENAI_API_KEY"},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime.Start() error: %v", err)
	}
	defer runtime.Stop()

	request := ExecutionRequest{
		SessionID: "s1",
		ActorID:   "agent:a",
		AuthEnv: map[string]string{
			"OPENAI_API_KEY": "gateway-shared-key",
		},
	}
	blob, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	responseBlob, err := fabric.Request(ctx, fabricts.NodeControlSubject("node-auth-fail"), blob)
	if err != nil {
		t.Fatalf("fabric.Request() error: %v", err)
	}
	var response ExecutionResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		t.Fatalf("decode execution response: %v", err)
	}
	if response.Error == "" {
		t.Fatalf("expected execution error, got none")
	}
	if got := os.Getenv("OPENAI_API_KEY"); got != "node-local-key" {
		t.Fatalf("OPENAI_API_KEY after failed execution = %q, want node-local-key", got)
	}
}

func messageContent(payload any) (string, bool) {
	switch value := payload.(type) {
	case sessionrt.Message:
		return value.Content, true
	case map[string]any:
		content, _ := value["content"].(string)
		return content, content != ""
	default:
		return "", false
	}
}
