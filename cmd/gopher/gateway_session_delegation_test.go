package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type noopAgentExecutor struct{}

func (noopAgentExecutor) Step(context.Context, sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{}, nil
}

type blockingAgentExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingAgentExecutor) Step(ctx context.Context, _ sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	select {
	case <-e.release:
	case <-ctx.Done():
		return sessionrt.AgentOutput{}, ctx.Err()
	}
	return sessionrt.AgentOutput{}, nil
}

func waitForDelegationKickoff(
	t *testing.T,
	ctx context.Context,
	store gatewaySessionDelegationStore,
	sessionID sessionrt.SessionID,
	match func(msg sessionrt.Message) bool,
) []sessionrt.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		events, err := store.List(ctx, sessionID)
		if err != nil {
			t.Fatalf("store.List(delegation) error: %v", err)
		}
		for _, event := range events {
			if event.Type != sessionrt.EventMessage {
				continue
			}
			msg, ok := event.Payload.(sessionrt.Message)
			if !ok {
				continue
			}
			if match(msg) {
				return events
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for kickoff message in session %s", sessionID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForSessionControlAction(
	t *testing.T,
	ctx context.Context,
	store gatewaySessionDelegationStore,
	sessionID sessionrt.SessionID,
	action string,
) sessionrt.Event {
	t.Helper()
	action = strings.TrimSpace(action)
	deadline := time.Now().Add(2 * time.Second)
	for {
		events, err := store.List(ctx, sessionID)
		if err != nil {
			t.Fatalf("store.List(session controls) error: %v", err)
		}
		for _, event := range events {
			if event.Type != sessionrt.EventControl {
				continue
			}
			payload, ok := controlPayloadFromAny(event.Payload)
			if !ok {
				continue
			}
			if strings.TrimSpace(payload.Action) == action {
				return event
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for control action %q in session %s", action, sessionID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func loadDelegationTestAgent(t *testing.T, workspaceRoot, agentID string) *agentcore.Agent {
	t.Helper()
	workspace := filepath.Join(workspaceRoot, "agents", agentID)
	createGatewayTestAgentWorkspace(t, workspace, agentID)
	if err := os.WriteFile(filepath.Join(workspace, "TASK.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	agent, err := agentcore.LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent(%s): %v", workspace, err)
	}
	return agent
}

func newDynamicDelegationFixture(t *testing.T) (context.Context, *gatewaySessionDelegationToolService, gatewaySessionDelegationStore, *agentcore.ActorExecutorRouter, *agentcore.Agent, *sessionrt.Manager, *sessionrt.Session) {
	t.Helper()
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	sourceAgent := loadDelegationTestAgent(t, workspaceRoot, "milo")

	router, err := agentcore.NewActorExecutorRouter("milo", map[sessionrt.ActorID]sessionrt.AgentExecutor{
		"milo": agentcore.NewSessionRuntimeAdapter(sourceAgent),
	})
	if err != nil {
		t.Fatalf("NewActorExecutorRouter() error: %v", err)
	}

	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: router,
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	sourceSession, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{{ID: "milo", Type: sessionrt.ActorAgent}},
	})
	if err != nil {
		t.Fatalf("CreateSession(source) error: %v", err)
	}

	dataDir := t.TempDir()
	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo": sourceAgent,
	}, dataDir, nil, router)
	sourceAgent.Delegation = service
	return ctx, service, store, router, sourceAgent, manager, sourceSession
}

func TestGatewaySessionDelegationCreatesSessionAndKickoff(t *testing.T) {
	ctx := context.Background()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	source, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "milo", Type: sessionrt.ActorAgent},
			{ID: "worker", Type: sessionrt.ActorAgent},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession(source) error: %v", err)
	}

	dataDir := t.TempDir()
	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo":   {},
		"worker": {},
	}, dataDir, nil, nil)

	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "worker",
		Title:           "billing migration",
		Message:         "Investigate failures and report next step.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	if strings.TrimSpace(result.SessionID) == "" {
		t.Fatalf("delegation session id should be set")
	}
	if result.SourceAgentID != "milo" || result.TargetAgentID != "worker" {
		t.Fatalf("delegation routing mismatch: %+v", result)
	}

	events := waitForDelegationKickoff(t, ctx, store, sessionrt.SessionID(result.SessionID), func(msg sessionrt.Message) bool {
		if msg.TargetActorID != "worker" || !strings.Contains(msg.Content, "Delegation for worker:") {
			return false
		}
		if msg.Role != sessionrt.RoleAgent {
			t.Fatalf("kickoff role = %q, want agent", msg.Role)
		}
		return true
	})
	if len(events) == 0 {
		t.Fatalf("expected kickoff message targeted to worker")
	}

	delegationsPath := filepath.Join(dataDir, "control", "delegations.jsonl")
	blob, err := os.ReadFile(delegationsPath)
	if err != nil {
		t.Fatalf("read delegations log error: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(blob)), "\n")
	if len(lines) < 1 {
		t.Fatalf("expected at least one delegation record")
	}
	record := map[string]any{}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &record); err != nil {
		t.Fatalf("decode delegation record: %v", err)
	}
	if got, _ := record["source_session_id"].(string); got != string(source.ID) {
		t.Fatalf("source_session_id = %q, want %q", got, source.ID)
	}
	if got, _ := record["target_agent_id"].(string); got != "worker" {
		t.Fatalf("target_agent_id = %q, want worker", got)
	}
}

func TestGatewaySessionDelegationSingleAgentAutoCreatesExplicitAliasTarget(t *testing.T) {
	ctx, service, store, _, _, _, sourceSession := newDynamicDelegationFixture(t)

	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "subagent1",
		Message:         "Investigate and report back.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	if result.TargetAgentID != "subagent1" {
		t.Fatalf("target = %q, want subagent1", result.TargetAgentID)
	}
	if !result.Ephemeral {
		t.Fatalf("expected ephemeral=true")
	}
	if result.WorkspaceMode != "isolated_temp" || result.MergeMode != "diff_for_approval" {
		t.Fatalf("unexpected modes: workspace=%q merge=%q", result.WorkspaceMode, result.MergeMode)
	}

	if _, exists := service.lookupAgent("subagent1"); !exists {
		t.Fatalf("expected subagent1 to be registered")
	}

	waitForDelegationKickoff(t, ctx, store, sessionrt.SessionID(result.SessionID), func(msg sessionrt.Message) bool {
		return msg.TargetActorID == "subagent1" && strings.Contains(msg.Content, "Delegation for subagent1:")
	})
}

func TestGatewaySessionDelegationCreateWithoutTargetAllocatesSequentialSubagents(t *testing.T) {
	ctx, service, _, _, _, _, sourceSession := newDynamicDelegationFixture(t)

	first, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		Message:         "Task one.",
	})
	if err != nil {
		t.Fatalf("first CreateDelegationSession() error: %v", err)
	}
	if first.TargetAgentID != "subagent1" {
		t.Fatalf("first target = %q, want subagent1", first.TargetAgentID)
	}

	second, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		Message:         "Task two.",
	})
	if err != nil {
		t.Fatalf("second CreateDelegationSession() error: %v", err)
	}
	if second.TargetAgentID != "subagent2" {
		t.Fatalf("second target = %q, want subagent2", second.TargetAgentID)
	}
}

func TestGatewaySessionDelegationRejectsSelfTargetAgent(t *testing.T) {
	ctx := context.Background()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	source, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{{ID: "milo", Type: sessionrt.ActorAgent}},
	})
	if err != nil {
		t.Fatalf("CreateSession(source) error: %v", err)
	}

	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo": {},
	}, t.TempDir(), nil, nil)
	_, err = service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "milo",
		Message:         "Investigate and report back.",
	})
	if err == nil {
		t.Fatalf("expected self-target delegation to be rejected")
	}
	if !strings.Contains(err.Error(), "source and target agents must be different") {
		t.Fatalf("expected self-target validation error, got: %v", err)
	}
}

func TestGatewaySessionDelegationKillGeneratesDiffArtifactForEphemeralWorker(t *testing.T) {
	ctx, service, _, _, sourceAgent, _, sourceSession := newDynamicDelegationFixture(t)

	sourceFile := filepath.Join(sourceAgent.Workspace, "TASK.md")
	orig, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatalf("read source file: %v", err)
	}

	created, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		Message:         "Edit task notes.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}

	worker, exists := service.lookupAgent(sessionrt.ActorID(created.TargetAgentID))
	if !exists {
		t.Fatalf("expected worker agent %q", created.TargetAgentID)
	}
	workerFile := filepath.Join(worker.Workspace, "TASK.md")
	if err := os.WriteFile(workerFile, []byte("delegated change\n"), 0o644); err != nil {
		t.Fatalf("write worker file: %v", err)
	}

	after, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatalf("read source file after worker edit: %v", err)
	}
	if string(after) != string(orig) {
		t.Fatalf("expected source workspace unchanged before merge")
	}

	killOut, err := service.KillDelegationSession(ctx, agentcore.DelegationKillRequest{
		SourceSessionID: string(sourceSession.ID),
		DelegationID:    created.SessionID,
	})
	if err != nil {
		t.Fatalf("KillDelegationSession() error: %v", err)
	}
	if !killOut.Killed {
		t.Fatalf("expected killed=true")
	}

	listAll, err := service.ListDelegationSessions(ctx, agentcore.DelegationListRequest{
		SourceSessionID: string(sourceSession.ID),
		IncludeInactive: true,
	})
	if err != nil {
		t.Fatalf("ListDelegationSessions(include inactive) error: %v", err)
	}
	if len(listAll) != 1 {
		t.Fatalf("all list count = %d, want 1", len(listAll))
	}
	if strings.TrimSpace(listAll[0].DiffArtifact) == "" {
		t.Fatalf("expected diff artifact path in list output")
	}
	blob, err := os.ReadFile(listAll[0].DiffArtifact)
	if err != nil {
		t.Fatalf("read diff artifact: %v", err)
	}
	if !strings.Contains(string(blob), "delegated change") {
		t.Fatalf("diff artifact missing expected content")
	}
}

func TestGatewaySessionDelegationTTLExpiresEphemeralWorker(t *testing.T) {
	ctx, service, _, router, _, _, sourceSession := newDynamicDelegationFixture(t)
	service.ttl = 10 * time.Millisecond

	created, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		Message:         "Run quick check.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}

	state := service.ephemeral[created.SessionID]
	state.LastActivity = time.Now().UTC().Add(-1 * time.Hour)
	service.ephemeral[created.SessionID] = state

	_, err = service.ListDelegationSessions(ctx, agentcore.DelegationListRequest{
		SourceSessionID: string(sourceSession.ID),
		IncludeInactive: true,
	})
	if err != nil {
		t.Fatalf("ListDelegationSessions() error: %v", err)
	}

	if _, exists := service.lookupAgent(sessionrt.ActorID(created.TargetAgentID)); exists {
		t.Fatalf("expected ephemeral worker to be removed after TTL")
	}
	if _, err := os.Stat(state.WorkerWorkspace); !os.IsNotExist(err) {
		t.Fatalf("expected worker workspace removed, stat err=%v", err)
	}
	_, err = router.Step(context.Background(), sessionrt.AgentInput{ActorID: sessionrt.ActorID(created.TargetAgentID)})
	if err == nil {
		t.Fatalf("expected router to reject expired worker actor")
	}

	listAll, err := service.ListDelegationSessions(ctx, agentcore.DelegationListRequest{
		SourceSessionID: string(sourceSession.ID),
		IncludeInactive: true,
	})
	if err != nil {
		t.Fatalf("ListDelegationSessions(include inactive) error: %v", err)
	}
	if len(listAll) != 1 {
		t.Fatalf("all list count = %d, want 1", len(listAll))
	}
	if listAll[0].Status != "expired" {
		t.Fatalf("status = %q, want expired", listAll[0].Status)
	}
}

func TestGatewaySessionDelegationCreateReturnsBeforeDelegatedTurnCompletes(t *testing.T) {
	ctx := context.Background()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	exec := &blockingAgentExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: exec,
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	source, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "milo", Type: sessionrt.ActorAgent},
			{ID: "worker", Type: sessionrt.ActorAgent},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession(source) error: %v", err)
	}

	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo":   {},
		"worker": {},
	}, t.TempDir(), nil, nil)

	start := time.Now()
	_, err = service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "worker",
		Message:         "Investigate and report back.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("CreateDelegationSession() took %s, want < 200ms", elapsed)
	}

	select {
	case <-exec.started:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for delegated kickoff execution")
	}
	close(exec.release)
}

func TestGatewaySessionDelegationCompletedAnnouncesToSourceSession(t *testing.T) {
	ctx := context.Background()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	source, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "milo", Type: sessionrt.ActorAgent},
			{ID: "worker", Type: sessionrt.ActorAgent},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession(source) error: %v", err)
	}

	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo":   {},
		"worker": {},
	}, t.TempDir(), nil, nil)

	created, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "worker",
		Message:         "Investigate and report back.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}

	if err := manager.SendEvent(ctx, sessionrt.Event{
		SessionID: sessionrt.SessionID(created.SessionID),
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload: sessionrt.ControlPayload{
			Action: sessionrt.ControlActionSessionCompleted,
		},
	}); err != nil {
		t.Fatalf("SendEvent(session.completed) error: %v", err)
	}

	controlEvent := waitForSessionControlAction(t, ctx, store, source.ID, "delegation.completed")
	ctrl, ok := controlPayloadFromAny(controlEvent.Payload)
	if !ok {
		t.Fatalf("expected control payload for delegation.completed")
	}
	delegationID, _ := ctrl.Metadata["delegation_id"].(string)
	if strings.TrimSpace(delegationID) != created.SessionID {
		t.Fatalf("delegation_id metadata = %q, want %q", delegationID, created.SessionID)
	}
	status, _ := ctrl.Metadata["status"].(string)
	if strings.TrimSpace(status) != "completed" {
		t.Fatalf("status metadata = %q, want completed", status)
	}

	waitForDelegationKickoff(t, ctx, store, source.ID, func(msg sessionrt.Message) bool {
		return msg.TargetActorID == "milo" &&
			strings.Contains(msg.Content, "finished session") &&
			strings.Contains(msg.Content, created.SessionID)
	})
}
