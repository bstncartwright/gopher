package gateway

import (
	"context"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	storepkg "github.com/bstncartwright/gopher/pkg/store"
)

type staticExecutor struct {
	text string
}

func (e *staticExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{Events: []sessionrt.Event{{
		Type: sessionrt.EventMessage,
		From: input.ActorID,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: e.text,
		},
	}}}, nil
}

func TestGatewayRecoveryReplaysAndContinues(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.NewFileEventStore(storepkg.FileEventStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}
	exec := &staticExecutor{text: "ack"}

	managerA, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, Executor: exec})
	if err != nil {
		t.Fatalf("NewManager(A) error: %v", err)
	}
	created, err := managerA.CreateSession(ctx, sessionrt.CreateSessionOptions{Participants: []sessionrt.Participant{
		{ID: "agent:a", Type: sessionrt.ActorAgent},
		{ID: "user:me", Type: sessionrt.ActorHuman},
	}})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	if err := managerA.SendEvent(ctx, sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "one"},
	}); err != nil {
		t.Fatalf("SendEvent(A) error: %v", err)
	}

	managerB, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, Executor: exec, RecoverOnStart: true})
	if err != nil {
		t.Fatalf("NewManager(B) error: %v", err)
	}
	loaded, err := managerB.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSession(recovered) error: %v", err)
	}
	if loaded.Status != sessionrt.SessionActive {
		t.Fatalf("expected active recovered session, got %v", loaded.Status)
	}

	if err := managerB.SendEvent(ctx, sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "two"},
	}); err != nil {
		t.Fatalf("SendEvent(B) error: %v", err)
	}

	events, err := store.List(ctx, created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 events across restart, got %d", len(events))
	}
	for i, event := range events {
		want := uint64(i + 1)
		if event.Seq != want {
			t.Fatalf("event %d seq = %d, want %d", i, event.Seq, want)
		}
	}
}

func TestRecoveryFailsSessionsWithInFlightWork(t *testing.T) {
	ctx := context.Background()
	store, err := storepkg.NewFileEventStore(storepkg.FileEventStoreOptions{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewFileEventStore() error: %v", err)
	}
	managerA, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store})
	if err != nil {
		t.Fatalf("NewManager(A) error: %v", err)
	}
	created, err := managerA.CreateSession(ctx, sessionrt.CreateSessionOptions{Participants: []sessionrt.Participant{
		{ID: "agent:a", Type: sessionrt.ActorAgent},
		{ID: "user:me", Type: sessionrt.ActorHuman},
	}})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	record, err := store.GetSessionRecord(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	record.InFlight = true
	record.Status = sessionrt.SessionActive
	if err := store.UpsertSession(ctx, record); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}

	managerB, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, RecoverOnStart: true})
	if err != nil {
		t.Fatalf("NewManager(B) error: %v", err)
	}
	loaded, err := managerB.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSession(recovered) error: %v", err)
	}
	if loaded.Status != sessionrt.SessionFailed {
		t.Fatalf("expected recovered session to be failed, got %v", loaded.Status)
	}

	events, err := store.List(ctx, created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected recovery to append failure events, got %d", len(events))
	}
	last := events[len(events)-1]
	if last.Type != sessionrt.EventControl {
		t.Fatalf("expected final control event, got %s", last.Type)
	}
	ctrl, ok := anyToControl(last.Payload)
	if !ok || ctrl.Action != sessionrt.ControlActionSessionFailed {
		t.Fatalf("expected final control action %q", sessionrt.ControlActionSessionFailed)
	}
}

func TestRemoteNodeExecutionAndGatewayRestartRebuildsRegistry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	fabric := fabricts.NewInMemoryBus()
	localExec := &staticExecutor{text: "local"}
	remoteExec := &staticExecutor{text: "remote"}

	remoteRuntime, err := node.NewRuntime(node.RuntimeOptions{
		NodeID:            "node-gpu",
		Capabilities:      []scheduler.Capability{{Kind: scheduler.CapabilityAgent, Name: "agent"}, {Kind: scheduler.CapabilityTool, Name: "gpu"}},
		Fabric:            fabric,
		Executor:          remoteExec,
		HeartbeatInterval: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}
	if err := remoteRuntime.Start(ctx); err != nil {
		t.Fatalf("remoteRuntime.Start() error: %v", err)
	}
	defer remoteRuntime.Stop()

	registryA := scheduler.NewRegistry(300 * time.Millisecond)
	registryA.Upsert(scheduler.NodeInfo{NodeID: "gateway", IsGateway: true, Capabilities: []scheduler.Capability{{Kind: scheduler.CapabilityAgent, Name: "agent"}}})
	syncA, err := NewNodeRegistrySync(NodeRegistrySyncOptions{Fabric: fabric, Registry: registryA, PruneInterval: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewNodeRegistrySync(A) error: %v", err)
	}
	if err := syncA.Start(ctx); err != nil {
		t.Fatalf("syncA.Start() error: %v", err)
	}

	if err := waitForNode(ctx, registryA, "node-gpu"); err != nil {
		t.Fatalf("node did not register on first gateway runtime: %v", err)
	}

	schedulerA := scheduler.NewScheduler("gateway", registryA)
	distributedA, err := NewDistributedExecutor(DistributedExecutorOptions{
		GatewayNodeID: "gateway",
		LocalExecutor: localExec,
		Scheduler:     schedulerA,
		Fabric:        fabric,
		CapabilityResolver: func(sessionrt.AgentInput) []scheduler.Capability {
			return []scheduler.Capability{{Kind: scheduler.CapabilityTool, Name: "gpu"}}
		},
	})
	if err != nil {
		t.Fatalf("NewDistributedExecutor(A) error: %v", err)
	}

	first, err := distributedA.Step(ctx, sessionrt.AgentInput{SessionID: "s1", ActorID: "agent:a"})
	if err != nil {
		t.Fatalf("distributedA.Step() error: %v", err)
	}
	if msg := first.Events[0].Payload.(map[string]any)["content"]; msg != "remote" {
		t.Fatalf("expected remote execution before restart, got %v", msg)
	}

	// Simulate gateway restart: node keeps running, gateway in-memory registry is rebuilt from heartbeats.
	syncA.Stop()

	registryB := scheduler.NewRegistry(300 * time.Millisecond)
	registryB.Upsert(scheduler.NodeInfo{NodeID: "gateway", IsGateway: true, Capabilities: []scheduler.Capability{{Kind: scheduler.CapabilityAgent, Name: "agent"}}})
	syncB, err := NewNodeRegistrySync(NodeRegistrySyncOptions{Fabric: fabric, Registry: registryB, PruneInterval: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewNodeRegistrySync(B) error: %v", err)
	}
	if err := syncB.Start(ctx); err != nil {
		t.Fatalf("syncB.Start() error: %v", err)
	}
	defer syncB.Stop()

	if err := waitForNode(ctx, registryB, "node-gpu"); err != nil {
		t.Fatalf("node did not reappear after gateway restart: %v", err)
	}

	schedulerB := scheduler.NewScheduler("gateway", registryB)
	distributedB, err := NewDistributedExecutor(DistributedExecutorOptions{
		GatewayNodeID: "gateway",
		LocalExecutor: localExec,
		Scheduler:     schedulerB,
		Fabric:        fabric,
		CapabilityResolver: func(sessionrt.AgentInput) []scheduler.Capability {
			return []scheduler.Capability{{Kind: scheduler.CapabilityTool, Name: "gpu"}}
		},
	})
	if err != nil {
		t.Fatalf("NewDistributedExecutor(B) error: %v", err)
	}

	second, err := distributedB.Step(ctx, sessionrt.AgentInput{SessionID: "s2", ActorID: "agent:a"})
	if err != nil {
		t.Fatalf("distributedB.Step() error: %v", err)
	}
	if msg := second.Events[0].Payload.(map[string]any)["content"]; msg != "remote" {
		t.Fatalf("expected remote execution after restart, got %v", msg)
	}
}

func waitForNode(ctx context.Context, registry *scheduler.Registry, nodeID string) error {
	for {
		if _, ok := registry.Get(nodeID); ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func anyToControl(payload any) (sessionrt.ControlPayload, bool) {
	switch value := payload.(type) {
	case sessionrt.ControlPayload:
		return value, true
	case map[string]any:
		action, _ := value["action"].(string)
		reason, _ := value["reason"].(string)
		return sessionrt.ControlPayload{Action: action, Reason: reason}, action != ""
	default:
		return sessionrt.ControlPayload{}, false
	}
}
