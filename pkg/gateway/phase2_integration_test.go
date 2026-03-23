package gateway

import (
	"context"
	"strings"
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

func eventMessageFields(event sessionrt.Event) (string, sessionrt.ActorID, bool) {
	if event.Type != sessionrt.EventMessage {
		return "", "", false
	}
	switch payload := event.Payload.(type) {
	case sessionrt.Message:
		return payload.Content, payload.TargetActorID, true
	case map[string]any:
		content, _ := payload["content"].(string)
		target, _ := payload["target_actor_id"].(string)
		return content, sessionrt.ActorID(target), true
	default:
		return "", "", false
	}
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

func TestRecoveryResumesInterruptedSessionAndKeepsSessionActive(t *testing.T) {
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
	if err := managerA.SendEvent(ctx, sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "resume me"},
	}); err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}

	record, err := store.GetSessionRecord(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	record.InFlight = true
	record.Status = sessionrt.SessionActive
	record.ResumeTriggerSeq = 2
	record.ResumeActorIDs = []sessionrt.ActorID{"agent:a"}
	if err := store.UpsertSession(ctx, record); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}

	exec := &staticExecutor{text: "ack"}
	managerB, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, Executor: exec, RecoverOnStart: true})
	if err != nil {
		t.Fatalf("NewManager(B) error: %v", err)
	}
	loaded, err := managerB.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSession(recovered) error: %v", err)
	}
	if loaded.Status != sessionrt.SessionActive {
		t.Fatalf("expected recovered session to remain active, got %v", loaded.Status)
	}

	events, err := store.List(ctx, created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 events after recovery, got %d", len(events))
	}
	recoveryPrompts := 0
	agentReplies := 0
	for _, event := range events {
		content, targetActorID, ok := eventMessageFields(event)
		if !ok || event.Type != sessionrt.EventMessage {
			continue
		}
		if event.From == sessionrt.SystemActorID && strings.Contains(content, "[resume after interruption]") {
			recoveryPrompts++
			if targetActorID != "agent:a" {
				t.Fatalf("recovery prompt target = %q, want agent:a", targetActorID)
			}
			continue
		}
		if event.From == "agent:a" && content == "ack" {
			agentReplies++
		}
	}
	if recoveryPrompts != 1 {
		t.Fatalf("recovery prompts = %d, want 1", recoveryPrompts)
	}
	if agentReplies != 1 {
		t.Fatalf("agent replies = %d, want 1", agentReplies)
	}

	recoveredRecord, err := store.GetSessionRecord(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord(recovered) error: %v", err)
	}
	if recoveredRecord.InFlight {
		t.Fatalf("expected recovered session in_flight=false")
	}
	if recoveredRecord.PendingResume {
		t.Fatalf("expected recovered session pending_resume=false")
	}
}

func TestRecoveryReplaysExistingRecoveryPromptWithoutDuplicatingIt(t *testing.T) {
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
	if err := managerA.SendEvent(ctx, sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "one"},
	}); err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}

	record, err := store.GetSessionRecord(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	record.InFlight = true
	record.Status = sessionrt.SessionActive
	record.ResumeTriggerSeq = 2
	record.ResumeActorIDs = []sessionrt.ActorID{"agent:a"}
	if err := store.UpsertSession(ctx, record); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}

	if _, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, RecoverOnStart: true}); err != nil {
		t.Fatalf("NewManager(B) error: %v", err)
	}

	midEvents, err := store.List(ctx, created.ID)
	if err != nil {
		t.Fatalf("List(mid) error: %v", err)
	}
	if len(midEvents) != 3 {
		t.Fatalf("expected one persisted recovery prompt before replay, got %d events", len(midEvents))
	}

	managerC, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, Executor: &staticExecutor{text: "ack"}, RecoverOnStart: true})
	if err != nil {
		t.Fatalf("NewManager(C) error: %v", err)
	}
	loaded, err := managerC.GetSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSession(recovered) error: %v", err)
	}
	if loaded.Status != sessionrt.SessionActive {
		t.Fatalf("expected active recovered session, got %v", loaded.Status)
	}

	events, err := store.List(ctx, created.ID)
	if err != nil {
		t.Fatalf("List(final) error: %v", err)
	}
	recoveryPrompts := 0
	agentReplies := 0
	for _, event := range events {
		content, _, ok := eventMessageFields(event)
		if !ok || event.Type != sessionrt.EventMessage {
			continue
		}
		if event.From == sessionrt.SystemActorID && strings.Contains(content, "[resume after interruption]") {
			recoveryPrompts++
		}
		if event.From == "agent:a" && content == "ack" {
			agentReplies++
		}
	}
	if recoveryPrompts != 1 {
		t.Fatalf("recovery prompts = %d, want 1", recoveryPrompts)
	}
	if agentReplies != 1 {
		t.Fatalf("agent replies = %d, want 1", agentReplies)
	}
}

func TestRecoveryResumesFromPartialPersistedHistoryWithoutDuplicatingOutput(t *testing.T) {
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
	if err := managerA.SendEvent(ctx, sessionrt.Event{
		SessionID: created.ID,
		From:      "user:me",
		Type:      sessionrt.EventMessage,
		Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "run the task"},
	}); err != nil {
		t.Fatalf("SendEvent() error: %v", err)
	}
	if err := store.Append(ctx, sessionrt.Event{
		ID:        sessionrt.EventID(string(created.ID) + "-000003"),
		SessionID: created.ID,
		From:      "agent:a",
		Type:      sessionrt.EventToolCall,
		Payload: map[string]any{
			"name": "read",
			"args": map[string]any{"path": "README.md"},
		},
		Timestamp: time.Now().UTC(),
		Seq:       3,
	}); err != nil {
		t.Fatalf("Append(tool_call) error: %v", err)
	}

	record, err := store.GetSessionRecord(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetSessionRecord() error: %v", err)
	}
	record.InFlight = true
	record.Status = sessionrt.SessionActive
	record.LastSeq = 3
	record.ResumeTriggerSeq = 2
	record.ResumeActorIDs = []sessionrt.ActorID{"agent:a"}
	if err := store.UpsertSession(ctx, record); err != nil {
		t.Fatalf("UpsertSession() error: %v", err)
	}

	if _, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store, Executor: &staticExecutor{text: "ack"}, RecoverOnStart: true}); err != nil {
		t.Fatalf("NewManager(B) error: %v", err)
	}

	events, err := store.List(ctx, created.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	recoveryPrompts := 0
	toolCalls := 0
	agentReplies := 0
	for _, event := range events {
		content, _, ok := eventMessageFields(event)
		if event.Type == sessionrt.EventToolCall {
			toolCalls++
		}
		if ok && event.From == sessionrt.SystemActorID && strings.Contains(content, "[resume after interruption]") {
			recoveryPrompts++
		}
		if ok && event.From == "agent:a" && content == "ack" {
			agentReplies++
		}
	}
	if toolCalls != 1 {
		t.Fatalf("tool calls = %d, want 1 preserved partial output", toolCalls)
	}
	if recoveryPrompts != 1 {
		t.Fatalf("recovery prompts = %d, want 1", recoveryPrompts)
	}
	if agentReplies != 1 {
		t.Fatalf("agent replies = %d, want 1", agentReplies)
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
