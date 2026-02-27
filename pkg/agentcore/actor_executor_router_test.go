package agentcore

import (
	"context"
	"strings"
	"sync"
	"testing"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type fixedExecutor struct {
	text string
}

func (e *fixedExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				Type: sessionrt.EventMessage,
				From: input.ActorID,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: e.text,
				},
			},
		},
	}, nil
}

func TestActorExecutorRouterRoutesByActorID(t *testing.T) {
	router, err := NewActorExecutorRouter("agent:planner", map[sessionrt.ActorID]sessionrt.AgentExecutor{
		"agent:planner": &fixedExecutor{text: "planner"},
		"agent:writer":  &fixedExecutor{text: "writer"},
	})
	if err != nil {
		t.Fatalf("NewActorExecutorRouter() error: %v", err)
	}

	out, err := router.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "agent:writer",
	})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}
	if len(out.Events) != 1 {
		t.Fatalf("expected one event, got %d", len(out.Events))
	}
	msg, ok := out.Events[0].Payload.(sessionrt.Message)
	if !ok {
		t.Fatalf("expected sessionrt.Message payload, got %T", out.Events[0].Payload)
	}
	if msg.Content != "writer" {
		t.Fatalf("content = %q, want writer", msg.Content)
	}
}

func TestActorExecutorRouterResolvesAgentPrefixAlias(t *testing.T) {
	router, err := NewActorExecutorRouter("planner", map[sessionrt.ActorID]sessionrt.AgentExecutor{
		"planner": &fixedExecutor{text: "ok"},
	})
	if err != nil {
		t.Fatalf("NewActorExecutorRouter() error: %v", err)
	}

	out, err := router.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "agent:planner",
	})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}
	if out.Events[0].From != "planner" {
		t.Fatalf("event from = %q, want planner", out.Events[0].From)
	}
}

func TestActorExecutorRouterUsesDefaultActor(t *testing.T) {
	router, err := NewActorExecutorRouter("agent:planner", map[sessionrt.ActorID]sessionrt.AgentExecutor{
		"agent:planner": &fixedExecutor{text: "planner"},
	})
	if err != nil {
		t.Fatalf("NewActorExecutorRouter() error: %v", err)
	}

	out, err := router.Step(context.Background(), sessionrt.AgentInput{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}
	msg, ok := out.Events[0].Payload.(sessionrt.Message)
	if !ok {
		t.Fatalf("expected sessionrt.Message payload, got %T", out.Events[0].Payload)
	}
	if msg.Content != "planner" {
		t.Fatalf("content = %q, want planner", msg.Content)
	}
}

func TestActorExecutorRouterUnknownActorErrorIncludesKnownActors(t *testing.T) {
	router, err := NewActorExecutorRouter("agent:planner", map[sessionrt.ActorID]sessionrt.AgentExecutor{
		"agent:planner": &fixedExecutor{text: "planner"},
	})
	if err != nil {
		t.Fatalf("NewActorExecutorRouter() error: %v", err)
	}

	_, err = router.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "agent:unknown",
	})
	if err == nil {
		t.Fatalf("expected unknown actor error")
	}
	if !strings.Contains(err.Error(), "known: agent:planner") {
		t.Fatalf("expected known actor list in error, got: %v", err)
	}
}

func TestActorExecutorRouterRegisterAndUnregisterActor(t *testing.T) {
	router, err := NewActorExecutorRouter("agent:planner", map[sessionrt.ActorID]sessionrt.AgentExecutor{
		"agent:planner": &fixedExecutor{text: "planner"},
	})
	if err != nil {
		t.Fatalf("NewActorExecutorRouter() error: %v", err)
	}

	if err := router.RegisterActor("agent:writer", &fixedExecutor{text: "writer"}); err != nil {
		t.Fatalf("RegisterActor() error: %v", err)
	}

	out, err := router.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "writer",
	})
	if err != nil {
		t.Fatalf("Step() error: %v", err)
	}
	msg, ok := out.Events[0].Payload.(sessionrt.Message)
	if !ok {
		t.Fatalf("expected sessionrt.Message payload, got %T", out.Events[0].Payload)
	}
	if msg.Content != "writer" {
		t.Fatalf("content = %q, want writer", msg.Content)
	}

	if !router.UnregisterActor("agent:writer") {
		t.Fatalf("expected UnregisterActor() to return true")
	}
	if router.UnregisterActor("agent:writer") {
		t.Fatalf("expected UnregisterActor() second call to return false")
	}
	_, err = router.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "writer",
	})
	if err == nil {
		t.Fatalf("expected unknown actor error after unregister")
	}
}

func TestActorExecutorRouterConcurrentRegisterAndStep(t *testing.T) {
	router, err := NewActorExecutorRouter("agent:planner", map[sessionrt.ActorID]sessionrt.AgentExecutor{
		"agent:planner": &fixedExecutor{text: "planner"},
	})
	if err != nil {
		t.Fatalf("NewActorExecutorRouter() error: %v", err)
	}
	if err := router.RegisterActor("agent:writer", &fixedExecutor{text: "writer"}); err != nil {
		t.Fatalf("RegisterActor() error: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = router.Step(context.Background(), sessionrt.AgentInput{
					SessionID: "sess-1",
					ActorID:   "agent:planner",
				})
				return
			}
			_, _ = router.Step(context.Background(), sessionrt.AgentInput{
				SessionID: "sess-1",
				ActorID:   "agent:writer",
			})
		}(i)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = router.UnregisterActor("agent:writer")
		_ = router.RegisterActor("agent:writer", &fixedExecutor{text: "writer"})
	}()
	wg.Wait()

	_, err = router.Step(context.Background(), sessionrt.AgentInput{
		SessionID: "sess-1",
		ActorID:   "agent:writer",
	})
	if err != nil {
		t.Fatalf("Step(agent:writer) error: %v", err)
	}
}
