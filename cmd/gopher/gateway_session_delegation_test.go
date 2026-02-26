package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type noopAgentExecutor struct{}

func (noopAgentExecutor) Step(context.Context, sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{}, nil
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
	service := newGatewaySessionDelegationToolService(manager, map[sessionrt.ActorID]*agentcore.Agent{
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

	events, err := store.List(ctx, sessionrt.SessionID(result.SessionID))
	if err != nil {
		t.Fatalf("store.List(delegation) error: %v", err)
	}
	foundKickoff := false
	for _, event := range events {
		if event.Type != sessionrt.EventMessage {
			continue
		}
		msg, ok := event.Payload.(sessionrt.Message)
		if !ok {
			continue
		}
		if msg.TargetActorID == "worker" && strings.Contains(msg.Content, "Delegation for worker:") {
			if msg.Role != sessionrt.RoleAgent {
				t.Fatalf("kickoff role = %q, want agent", msg.Role)
			}
			foundKickoff = true
			break
		}
	}
	if !foundKickoff {
		t.Fatalf("expected kickoff message targeted to worker, events=%+v", events)
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

	service := newGatewaySessionDelegationToolService(manager, map[sessionrt.ActorID]*agentcore.Agent{
		"milo": {},
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
