package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/a2a"
	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/config"
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

type actorCountingExecutor struct {
	mu    sync.Mutex
	calls map[sessionrt.ActorID]int
}

func (e *actorCountingExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	e.mu.Lock()
	if e.calls == nil {
		e.calls = map[sessionrt.ActorID]int{}
	}
	e.calls[input.ActorID]++
	e.mu.Unlock()

	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				Type: sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: string(input.ActorID) + " ack",
				},
			},
		},
	}, nil
}

func (e *actorCountingExecutor) callCount(actorID sessionrt.ActorID) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[actorID]
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

func modelsToMap(models []ai.Model) map[string]ai.Model {
	out := make(map[string]ai.Model, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out
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
	}, dataDir, nil, router, nil, nil)
	sourceAgent.Delegation = service
	return ctx, service, store, router, sourceAgent, manager, sourceSession
}

func newA2ADelegationFixture(t *testing.T) (context.Context, *gatewaySessionDelegationToolService, *agentcore.Agent, *sessionrt.Session) {
	t.Helper()
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	sourceAgent := loadDelegationTestAgent(t, workspaceRoot, "milo")

	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
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
	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo": sourceAgent,
	}, t.TempDir(), nil, nil, nil, nil)
	sourceAgent.Delegation = service
	return ctx, service, sourceAgent, sourceSession
}

type fakeA2AClient struct {
	mu             sync.Mutex
	sendQueue      []a2a.Task
	subscribeQueue map[string][]a2a.Task
	pollQueue      map[string][]a2a.Task
	subscribeErr   error
	cancelErr      error
	lastSend       []a2a.MessageSendRequest
	cancelled      []string
}

func (f *fakeA2AClient) Discover(_ context.Context, remote a2a.Remote) (a2a.AgentCard, error) {
	return a2a.AgentCard{
		Name:        "Research",
		Description: "Deep research",
		URL:         remote.BaseURL,
		Skills: []a2a.AgentSkill{{
			ID:          "research",
			Name:        "Research",
			Description: "Deep research and synthesis",
		}},
	}, nil
}

func (f *fakeA2AClient) GetExtendedCard(_ context.Context, _ string, remote a2a.Remote) (a2a.AgentCard, error) {
	return f.Discover(context.Background(), remote)
}

func (f *fakeA2AClient) SendMessage(_ context.Context, _ string, _ a2a.Remote, req a2a.MessageSendRequest) (a2a.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSend = append(f.lastSend, req)
	if len(f.sendQueue) == 0 {
		return a2a.Task{}, fmt.Errorf("unexpected send")
	}
	task := f.sendQueue[0]
	f.sendQueue = f.sendQueue[1:]
	return task, nil
}

func (f *fakeA2AClient) GetTask(_ context.Context, _ string, _ a2a.Remote, taskID string) (a2a.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := f.pollQueue[taskID]
	if len(queue) == 0 {
		return a2a.Task{}, fmt.Errorf("unexpected poll %s", taskID)
	}
	task := queue[0]
	f.pollQueue[taskID] = queue[1:]
	return task, nil
}

func (f *fakeA2AClient) SubscribeTask(_ context.Context, _ string, _ a2a.Remote, taskID string, emit func(a2a.Task) error) error {
	if f.subscribeErr != nil {
		return f.subscribeErr
	}
	f.mu.Lock()
	queue := append([]a2a.Task(nil), f.subscribeQueue[taskID]...)
	f.mu.Unlock()
	for _, task := range queue {
		if err := emit(task); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeA2AClient) CancelTask(_ context.Context, _ string, _ a2a.Remote, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, taskID)
	return f.cancelErr
}

func waitForDelegationRecordStatus(t *testing.T, service *gatewaySessionDelegationToolService, delegationID string, status string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		record, ok := service.readDelegationRecords()[delegationID]
		if ok && delegationStatus(record) == status {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for delegation %s status %s", delegationID, status)
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
	}, dataDir, nil, nil, nil, nil)

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

func TestGatewaySessionDelegationCreatesA2ADelegationAndCompletes(t *testing.T) {
	ctx, service, sourceAgent, sourceSession := newA2ADelegationFixture(t)
	client := &fakeA2AClient{
		sendQueue: []a2a.Task{{
			TaskID:    "task-1",
			ContextID: "ctx-1",
			Status:    a2a.TaskStateCompleted,
			Message:   &a2a.Message{Parts: []a2a.Part{{Text: "Remote answer"}}},
		}},
	}
	service.SetA2ABackend(ctx, newGatewayA2ABackend(config.A2AConfig{
		Enabled:                   true,
		DiscoveryTimeout:          time.Second,
		RequestTimeout:            time.Second,
		TaskPollInterval:          10 * time.Millisecond,
		StreamIdleTimeout:         100 * time.Millisecond,
		CardRefreshInterval:       time.Minute,
		ResumeScanInterval:        20 * time.Millisecond,
		CompatLegacyWellKnownPath: true,
		Remotes: []config.A2ARemoteConfig{{
			ID:             "research",
			BaseURL:        "https://example.com/a2a",
			Enabled:        true,
			RequestTimeout: time.Second,
		}},
	}, client))
	sourceAgent.Delegation = service

	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   sourceAgent.ID,
		TargetAgentID:   "a2a:research",
		Message:         "Research the issue.",
		Title:           "remote research",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	if result.TargetAgentID != "a2a:research" {
		t.Fatalf("target agent id = %q", result.TargetAgentID)
	}
	waitForSessionControlAction(t, ctx, service.store, sourceSession.ID, "delegation.completed")
	record := service.readDelegationRecords()[result.SessionID]
	if stringFromMap(record, "remote_id") != "research" {
		t.Fatalf("remote_id = %q", stringFromMap(record, "remote_id"))
	}
	if len(sourceAgent.RemoteDelegationTargets) == 0 {
		t.Fatalf("expected remote delegation targets on source agent")
	}
}

func TestGatewaySessionDelegationIncludesConnectedNodeTargetsOnSourceAgent(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: noopAgentExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	sourceAgent := &agentcore.Agent{ID: "milo"}
	service := newGatewaySessionDelegationToolService(manager, store, map[sessionrt.ActorID]*agentcore.Agent{
		"milo": sourceAgent,
	}, t.TempDir(), nil, nil, func(actorID sessionrt.ActorID) bool {
		return actorID == "browser"
	}, func() []agentcore.RemoteDelegationTarget {
		return []agentcore.RemoteDelegationTarget{
			{ID: "browser", Description: "Connected via node node-2"},
			{ID: "milo", Description: "Should be filtered because it is local"},
		}
	})

	sourceAgent.Delegation = service
	service.refreshKnownAgentsLocked()

	if len(sourceAgent.RemoteDelegationTargets) != 1 {
		t.Fatalf("remote delegation targets len = %d, want 1", len(sourceAgent.RemoteDelegationTargets))
	}
	if got := sourceAgent.RemoteDelegationTargets[0].ID; got != "browser" {
		t.Fatalf("remote target id = %q, want browser", got)
	}
	if got := sourceAgent.RemoteDelegationTargets[0].Description; got != "Connected via node node-2" {
		t.Fatalf("remote target description = %q, want connected-node label", got)
	}
}

func TestGatewaySessionDelegationA2AInputRequiredCanReply(t *testing.T) {
	ctx, service, sourceAgent, sourceSession := newA2ADelegationFixture(t)
	client := &fakeA2AClient{
		sendQueue: []a2a.Task{
			{
				TaskID:    "task-2",
				ContextID: "ctx-2",
				Status:    a2a.TaskStateInputRequired,
				Message:   &a2a.Message{Parts: []a2a.Part{{Text: "What environment is failing?"}}},
			},
			{
				TaskID:    "task-2",
				ContextID: "ctx-2",
				Status:    a2a.TaskStateCompleted,
				Message:   &a2a.Message{Parts: []a2a.Part{{Text: "Thanks, done."}}},
			},
		},
	}
	service.SetA2ABackend(ctx, newGatewayA2ABackend(config.A2AConfig{
		Enabled:                   true,
		DiscoveryTimeout:          time.Second,
		RequestTimeout:            time.Second,
		TaskPollInterval:          10 * time.Millisecond,
		StreamIdleTimeout:         100 * time.Millisecond,
		CardRefreshInterval:       time.Minute,
		ResumeScanInterval:        20 * time.Millisecond,
		CompatLegacyWellKnownPath: true,
		Remotes: []config.A2ARemoteConfig{{
			ID:             "research",
			BaseURL:        "https://example.com/a2a",
			Enabled:        true,
			RequestTimeout: time.Second,
		}},
	}, client))
	sourceAgent.Delegation = service

	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   sourceAgent.ID,
		TargetAgentID:   "a2a:research",
		Message:         "Investigate the deployment failure.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	waitForSessionControlAction(t, ctx, service.store, sourceSession.ID, "delegation.input_required")
	record := waitForDelegationRecordStatus(t, service, result.SessionID, "active")
	if !boolFromMap(record, "waiting_for_input") {
		t.Fatalf("expected waiting_for_input")
	}
	replyResult, err := service.ReplyDelegationSession(ctx, agentcore.DelegationReplyRequest{
		SourceSessionID: string(sourceSession.ID),
		DelegationID:    result.SessionID,
		Message:         "production-us-west-2",
	})
	if err != nil {
		t.Fatalf("ReplyDelegationSession() error: %v", err)
	}
	if !replyResult.Accepted {
		t.Fatalf("expected reply accepted")
	}
	waitForSessionControlAction(t, ctx, service.store, sourceSession.ID, "delegation.completed")
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.lastSend) != 2 {
		t.Fatalf("send calls = %d, want 2", len(client.lastSend))
	}
	if client.lastSend[1].TaskID != "task-2" || client.lastSend[1].ContextID != "ctx-2" {
		t.Fatalf("reply task/context = %q/%q", client.lastSend[1].TaskID, client.lastSend[1].ContextID)
	}
}

func TestGatewaySessionDelegationA2APollingFallbackCompletes(t *testing.T) {
	ctx, service, sourceAgent, sourceSession := newA2ADelegationFixture(t)
	client := &fakeA2AClient{
		sendQueue:    []a2a.Task{{TaskID: "task-3", ContextID: "ctx-3", Status: a2a.TaskStateSubmitted}},
		subscribeErr: fmt.Errorf("stream unavailable"),
		pollQueue: map[string][]a2a.Task{
			"task-3": {{
				TaskID:    "task-3",
				ContextID: "ctx-3",
				Status:    a2a.TaskStateCompleted,
				Message:   &a2a.Message{Parts: []a2a.Part{{Text: "polled result"}}},
			}},
		},
	}
	service.SetA2ABackend(ctx, newGatewayA2ABackend(config.A2AConfig{
		Enabled:                   true,
		DiscoveryTimeout:          time.Second,
		RequestTimeout:            time.Second,
		TaskPollInterval:          10 * time.Millisecond,
		StreamIdleTimeout:         100 * time.Millisecond,
		CardRefreshInterval:       time.Minute,
		ResumeScanInterval:        20 * time.Millisecond,
		CompatLegacyWellKnownPath: true,
		Remotes: []config.A2ARemoteConfig{{
			ID:             "research",
			BaseURL:        "https://example.com/a2a",
			Enabled:        true,
			RequestTimeout: time.Second,
		}},
	}, client))
	sourceAgent.Delegation = service

	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   sourceAgent.ID,
		TargetAgentID:   "a2a:research",
		Message:         "Do a remote job.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	waitForSessionControlAction(t, ctx, service.store, sourceSession.ID, "delegation.completed")
	waitForDelegationRecordStatus(t, service, result.SessionID, "completed")
}

func TestGatewaySessionDelegationA2ACancelRecordsWarningWhenRemoteCancelFails(t *testing.T) {
	ctx, service, sourceAgent, sourceSession := newA2ADelegationFixture(t)
	client := &fakeA2AClient{
		sendQueue: []a2a.Task{{TaskID: "task-4", ContextID: "ctx-4", Status: a2a.TaskStateSubmitted}},
		cancelErr: fmt.Errorf("remote cancel unsupported"),
	}
	service.SetA2ABackend(ctx, newGatewayA2ABackend(config.A2AConfig{
		Enabled:                   true,
		DiscoveryTimeout:          time.Second,
		RequestTimeout:            time.Second,
		TaskPollInterval:          10 * time.Millisecond,
		StreamIdleTimeout:         100 * time.Millisecond,
		CardRefreshInterval:       time.Minute,
		ResumeScanInterval:        20 * time.Millisecond,
		CompatLegacyWellKnownPath: true,
		Remotes: []config.A2ARemoteConfig{{
			ID:             "research",
			BaseURL:        "https://example.com/a2a",
			Enabled:        true,
			RequestTimeout: time.Second,
		}},
	}, client))
	sourceAgent.Delegation = service

	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   sourceAgent.ID,
		TargetAgentID:   "a2a:research",
		Message:         "Cancelable task.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	killResult, err := service.KillDelegationSession(ctx, agentcore.DelegationKillRequest{
		SourceSessionID: string(sourceSession.ID),
		DelegationID:    result.SessionID,
	})
	if err != nil {
		t.Fatalf("KillDelegationSession() error: %v", err)
	}
	if !killResult.Killed {
		t.Fatalf("expected delegation killed")
	}
	record := waitForDelegationRecordStatus(t, service, result.SessionID, "cancelled")
	if !strings.Contains(stringFromMap(record, "warning"), "remote cancel unsupported") {
		t.Fatalf("warning = %q", stringFromMap(record, "warning"))
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

func TestGatewaySessionDelegationEphemeralWorkerModelPolicyDefaultsAndOverride(t *testing.T) {
	ctx, service, _, _, sourceAgent, _, sourceSession := newDynamicDelegationFixture(t)
	sourceAgent.Config.ModelPolicy = "openai:gpt-4o-mini"

	defaulted, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		Message:         "Task one.",
	})
	if err != nil {
		t.Fatalf("defaulted CreateDelegationSession() error: %v", err)
	}
	defaultedWorker, exists := service.lookupAgent(sessionrt.ActorID(defaulted.TargetAgentID))
	if !exists {
		t.Fatalf("expected worker agent %q", defaulted.TargetAgentID)
	}
	if got := strings.TrimSpace(defaultedWorker.Config.ModelPolicy); got != defaultAgentModelPolicy {
		t.Fatalf("defaulted model_policy = %q, want %q", got, defaultAgentModelPolicy)
	}

	overridden, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		ModelPolicy:     "openai:gpt-4o-mini",
		Message:         "Task two.",
	})
	if err != nil {
		t.Fatalf("overridden CreateDelegationSession() error: %v", err)
	}
	overriddenWorker, exists := service.lookupAgent(sessionrt.ActorID(overridden.TargetAgentID))
	if !exists {
		t.Fatalf("expected worker agent %q", overridden.TargetAgentID)
	}
	if got := strings.TrimSpace(overriddenWorker.Config.ModelPolicy); got != "openai:gpt-4o-mini" {
		t.Fatalf("overridden model_policy = %q, want openai:gpt-4o-mini", got)
	}
}

func TestGatewaySessionDelegationRejectsInvalidEphemeralWorkerModelOverride(t *testing.T) {
	ctx, service, _, _, _, _, sourceSession := newDynamicDelegationFixture(t)

	_, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		ModelPolicy:     "openai:not-a-real-model",
		Message:         "Task one.",
	})
	if err == nil {
		t.Fatalf("expected invalid model_policy override error")
	}
	if !strings.Contains(err.Error(), `model not found for model_policy "openai:not-a-real-model"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := service.lookupAgent("subagent1"); exists {
		t.Fatalf("did not expect worker agent to be created after failed validation")
	}
}

func TestGatewaySessionDelegationRejectsInvalidDefaultEphemeralWorkerModelPolicy(t *testing.T) {
	ctx, service, _, _, _, _, sourceSession := newDynamicDelegationFixture(t)

	original := modelsToMap(ai.GetModels(string(ai.ProviderOpenAICodex)))
	updated := modelsToMap(ai.GetModels(string(ai.ProviderOpenAICodex)))
	delete(updated, "gpt-5.4")
	ai.SetModels(ai.ProviderOpenAICodex, updated)
	defer ai.SetModels(ai.ProviderOpenAICodex, original)

	_, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(sourceSession.ID),
		SourceAgentID:   "milo",
		Message:         "Task one.",
	})
	if err == nil {
		t.Fatalf("expected invalid default model_policy error")
	}
	if !strings.Contains(err.Error(), `model not found for model_policy "openai-codex:gpt-5.4"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, exists := service.lookupAgent("subagent1"); exists {
		t.Fatalf("did not expect worker agent to be created after failed validation")
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
	}, t.TempDir(), nil, nil, nil, nil)
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

func TestGatewaySessionDelegationUsesRemoteNamedTargetWithoutEphemeralWorker(t *testing.T) {
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
	}, t.TempDir(), nil, nil, func(actorID sessionrt.ActorID) bool {
		return actorID == "browser"
	}, nil)
	result, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "browser",
		Message:         "Open example.com",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	if result.TargetAgentID != "browser" {
		t.Fatalf("target = %q, want browser", result.TargetAgentID)
	}
	if result.Ephemeral {
		t.Fatalf("expected remote named target to avoid ephemeral worker")
	}
	if _, exists := service.lookupAgent("browser"); exists {
		t.Fatalf("expected browser to remain remote-only, not registered locally")
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
	}, t.TempDir(), nil, nil, nil, nil)

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
	}, t.TempDir(), nil, nil, nil, nil)

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

func TestGatewaySessionDelegationAgentMessageAnnouncesToSourceSession(t *testing.T) {
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
	}, t.TempDir(), nil, nil, nil, nil)

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
		From:      "worker",
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: "done",
		},
	}); err != nil {
		t.Fatalf("SendEvent(worker message) error: %v", err)
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
			strings.TrimSpace(msg.Content) == "done"
	})
}

func TestGatewaySessionDelegationAgentMessageInvokesSourceAgent(t *testing.T) {
	ctx := context.Background()
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	exec := &actorCountingExecutor{}
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:         store,
		Executor:      exec,
		AgentSelector: gatewayMessageTargetSelector("milo"),
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
	}, t.TempDir(), nil, nil, nil, nil)

	created, err := service.CreateDelegationSession(ctx, agentcore.DelegationCreateRequest{
		SourceSessionID: string(source.ID),
		SourceAgentID:   "milo",
		TargetAgentID:   "worker",
		Message:         "Investigate and report back.",
	})
	if err != nil {
		t.Fatalf("CreateDelegationSession() error: %v", err)
	}
	if strings.TrimSpace(created.SessionID) == "" {
		t.Fatalf("delegation session id should be set")
	}

	waitForSessionControlAction(t, ctx, store, source.ID, "delegation.completed")

	deadline := time.Now().Add(2 * time.Second)
	for {
		if exec.callCount("milo") > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for source agent invocation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGatewaySessionDelegationSummaryActiveReturnsProgressDigest(t *testing.T) {
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
	}, t.TempDir(), nil, nil, nil, nil)

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
		From:      "worker",
		Type:      sessionrt.EventToolCall,
		Payload: map[string]any{
			"name": "read",
		},
	}); err != nil {
		t.Fatalf("SendEvent(tool_call) error: %v", err)
	}

	summary, err := service.GetDelegationSummary(ctx, agentcore.DelegationSummaryRequest{
		SourceSessionID: string(source.ID),
		DelegationID:    created.SessionID,
	})
	if err != nil {
		t.Fatalf("GetDelegationSummary() error: %v", err)
	}
	if summary.Status != "active" {
		t.Fatalf("status = %q, want active", summary.Status)
	}
	if summary.Terminal {
		t.Fatalf("terminal = true, want false")
	}
	if summary.TotalEvents == 0 {
		t.Fatalf("total events = 0, want > 0")
	}
	if summary.LastToolCall != "read" {
		t.Fatalf("last tool call = %q, want read", summary.LastToolCall)
	}
	if !strings.Contains(summary.Summary, "In progress") {
		t.Fatalf("summary = %q, expected in-progress digest", summary.Summary)
	}
}

func TestGatewaySessionDelegationSummaryCompletedReturnsTerminalDigest(t *testing.T) {
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
	}, t.TempDir(), nil, nil, nil, nil)

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
			Reason: "done by worker",
		},
	}); err != nil {
		t.Fatalf("SendEvent(session.completed) error: %v", err)
	}

	waitForSessionControlAction(t, ctx, store, source.ID, "delegation.completed")

	summary, err := service.GetDelegationSummary(ctx, agentcore.DelegationSummaryRequest{
		SourceSessionID: string(source.ID),
		DelegationID:    created.SessionID,
	})
	if err != nil {
		t.Fatalf("GetDelegationSummary() error: %v", err)
	}
	if summary.Status != "completed" {
		t.Fatalf("status = %q, want completed", summary.Status)
	}
	if !summary.Terminal {
		t.Fatalf("terminal = false, want true")
	}
	if !strings.Contains(summary.Summary, "Completed") {
		t.Fatalf("summary = %q, expected completed digest", summary.Summary)
	}
	if !strings.Contains(summary.Summary, "done by worker") {
		t.Fatalf("summary = %q, expected terminal reason", summary.Summary)
	}
}

func TestDelegationTerminalStatusFromEventIgnoresEventError(t *testing.T) {
	status, reason, ok := delegationTerminalStatusFromEvent(sessionrt.Event{
		Type: sessionrt.EventError,
		Payload: sessionrt.ErrorPayload{
			Message: "exa: status=429: rate limit exceeded",
		},
	})
	if ok {
		t.Fatalf("expected EventError to be non-terminal, got status=%q reason=%q", status, reason)
	}
}
