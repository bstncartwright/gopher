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
	}, dataDir, nil)

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

func TestGatewaySessionDelegationRejectsUnknownTargetAgent(t *testing.T) {
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
		},
	})
	if err != nil {
		t.Fatalf("CreateSession(source) error: %v", err)
	}

	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo":  {},
		"riley": {},
	}, t.TempDir(), nil)
	_, err = service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "worker",
		Message:         "please help",
	})
	if err == nil {
		t.Fatalf("expected unknown target agent error")
	}
}

func TestGatewaySessionDelegationSingleAgentAllowsAliasTarget(t *testing.T) {
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
	dataDir := t.TempDir()
	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo": {},
	}, dataDir, nil)
	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "subagent1",
		Message:         "Investigate and report back.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	if result.TargetAgentID != "subagent1" {
		t.Fatalf("target agent = %q, want subagent1", result.TargetAgentID)
	}
	delegatedSession, err := manager.GetSession(ctx, sessionrt.SessionID(result.SessionID))
	if err != nil {
		t.Fatalf("GetSession(delegated) error: %v", err)
	}
	if len(delegatedSession.Participants) != 1 {
		t.Fatalf("participants count = %d, want 1", len(delegatedSession.Participants))
	}
	if _, ok := delegatedSession.Participants["milo"]; !ok {
		t.Fatalf("expected milo participant in single-agent delegated session")
	}
	waitForDelegationKickoff(t, ctx, store, sessionrt.SessionID(result.SessionID), func(msg sessionrt.Message) bool {
		return msg.TargetActorID == "milo" && strings.Contains(msg.Content, "Delegation for subagent1:")
	})

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
	if got, _ := record["target_agent_id"].(string); got != "subagent1" {
		t.Fatalf("target_agent_id = %q, want subagent1", got)
	}
	if got, _ := record["resolved_target_agent_id"].(string); got != "milo" {
		t.Fatalf("resolved_target_agent_id = %q, want milo", got)
	}
}

func TestGatewaySessionDelegationListKillAndLog(t *testing.T) {
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
	}, t.TempDir(), nil)

	created, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "worker",
		Message:         "Investigate and reply with next steps.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}

	listActive, err := service.ListDelegationSessions(ctx, agentcore.DelegationListRequest{SourceSessionID: string(source.ID)})
	if err != nil {
		t.Fatalf("ListDelegationSessions(active) error: %v", err)
	}
	if len(listActive) != 1 {
		t.Fatalf("active list count = %d, want 1", len(listActive))
	}
	if listActive[0].SessionID != created.SessionID {
		t.Fatalf("listed session_id = %q, want %q", listActive[0].SessionID, created.SessionID)
	}
	if listActive[0].Status != "active" {
		t.Fatalf("listed status = %q, want active", listActive[0].Status)
	}

	waitForDelegationKickoff(t, ctx, store, sessionrt.SessionID(created.SessionID), func(msg sessionrt.Message) bool {
		return strings.Contains(msg.Content, "Delegation for worker:")
	})

	logOut, err := service.GetDelegationLog(ctx, agentcore.DelegationLogRequest{
		SourceSessionID: string(source.ID),
		DelegationID:    created.SessionID,
		Offset:          0,
		Limit:           20,
	})
	if err != nil {
		t.Fatalf("GetDelegationLog() error: %v", err)
	}
	if logOut.Count == 0 {
		t.Fatalf("expected delegation log entries")
	}
	foundKickoff := false
	for _, entry := range logOut.Entries {
		if strings.Contains(entry.Content, "Delegation for worker:") {
			foundKickoff = true
			break
		}
	}
	if !foundKickoff {
		t.Fatalf("expected kickoff content in delegation log, entries=%+v", logOut.Entries)
	}

	killOut, err := service.KillDelegationSession(ctx, agentcore.DelegationKillRequest{
		SourceSessionID: string(source.ID),
		DelegationID:    created.SessionID,
	})
	if err != nil {
		t.Fatalf("KillDelegationSession() error: %v", err)
	}
	if !killOut.Killed {
		t.Fatalf("expected killed=true, got false")
	}
	if killOut.Status != "cancelled" {
		t.Fatalf("kill status = %q, want cancelled", killOut.Status)
	}

	listActiveAfterKill, err := service.ListDelegationSessions(ctx, agentcore.DelegationListRequest{SourceSessionID: string(source.ID)})
	if err != nil {
		t.Fatalf("ListDelegationSessions(after kill active) error: %v", err)
	}
	if len(listActiveAfterKill) != 0 {
		t.Fatalf("active list count after kill = %d, want 0", len(listActiveAfterKill))
	}

	listAll, err := service.ListDelegationSessions(ctx, agentcore.DelegationListRequest{
		SourceSessionID: string(source.ID),
		IncludeInactive: true,
	})
	if err != nil {
		t.Fatalf("ListDelegationSessions(include inactive) error: %v", err)
	}
	if len(listAll) != 1 {
		t.Fatalf("all list count = %d, want 1", len(listAll))
	}
	if listAll[0].Status != "cancelled" {
		t.Fatalf("all list status = %q, want cancelled", listAll[0].Status)
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
	}, t.TempDir(), nil)

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
