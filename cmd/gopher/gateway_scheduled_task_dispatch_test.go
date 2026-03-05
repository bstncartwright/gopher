package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type fakeDelegationToolService struct {
	lastCreate    agentcore.DelegationCreateRequest
	createResult  agentcore.DelegationSession
	summaryResult agentcore.DelegationSummaryResult
}

func (f *fakeDelegationToolService) CreateDelegationSession(_ context.Context, req agentcore.DelegationCreateRequest) (agentcore.DelegationSession, error) {
	f.lastCreate = req
	return f.createResult, nil
}

func (f *fakeDelegationToolService) ListDelegationSessions(_ context.Context, _ agentcore.DelegationListRequest) ([]agentcore.DelegationListItem, error) {
	return nil, nil
}

func (f *fakeDelegationToolService) KillDelegationSession(_ context.Context, _ agentcore.DelegationKillRequest) (agentcore.DelegationKillResult, error) {
	return agentcore.DelegationKillResult{}, nil
}

func (f *fakeDelegationToolService) GetDelegationLog(_ context.Context, _ agentcore.DelegationLogRequest) (agentcore.DelegationLogResult, error) {
	return agentcore.DelegationLogResult{}, nil
}

func (f *fakeDelegationToolService) GetDelegationSummary(_ context.Context, _ agentcore.DelegationSummaryRequest) (agentcore.DelegationSummaryResult, error) {
	return f.summaryResult, nil
}

func TestScheduledTaskCronDispatcherIsolatedDispatchAndPoll(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{Store: store})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	session, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "agent:milo", Type: sessionrt.ActorAgent},
			{ID: "human:a", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	delegation := &fakeDelegationToolService{
		createResult: agentcore.DelegationSession{
			SessionID:    "delegation-1",
			Announcement: "Spawned subagent worker in session delegation-1.",
		},
		summaryResult: agentcore.DelegationSummaryResult{
			SessionID: "delegation-1",
			Status:    "completed",
			Summary:   "Completed after 2 events.",
		},
	}
	dispatcher, err := newScheduledTaskCronDispatcher(manager, delegation)
	if err != nil {
		t.Fatalf("newScheduledTaskCronDispatcher() error: %v", err)
	}

	nextRun := time.Date(2026, 3, 5, 16, 0, 0, 0, time.UTC)
	job := gateway.CronJob{
		ID:            "cron-1",
		SessionID:     string(session.ID),
		Title:         "Morning scan",
		Message:       "Summarize overnight updates.",
		CronExpr:      "* * * * *",
		Timezone:      "America/Denver",
		Mode:          gateway.CronModeIsolated,
		NotifyActorID: "agent:milo",
		TargetAgent:   "worker",
		ModelPolicy:   "fast",
		NextRunAt:     &nextRun,
	}

	result, err := dispatcher.Dispatch(context.Background(), job, nextRun)
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if result.Status != gateway.CronRunStatusRunning {
		t.Fatalf("dispatch status = %q, want running", result.Status)
	}
	if result.ActiveRunID != "delegation-1" {
		t.Fatalf("active run id = %q, want delegation-1", result.ActiveRunID)
	}
	if !delegation.lastCreate.SuppressTerminalMessage {
		t.Fatalf("expected scheduled tasks to suppress default delegation terminal message")
	}
	if !strings.Contains(delegation.lastCreate.Message, "[scheduled task]") {
		t.Fatalf("expected scheduled task kickoff wrapper, got %q", delegation.lastCreate.Message)
	}
	if !strings.Contains(delegation.lastCreate.Message, "mode: isolated") {
		t.Fatalf("expected isolated mode in kickoff wrapper, got %q", delegation.lastCreate.Message)
	}

	job.ActiveRunID = result.ActiveRunID
	pollResult, err := dispatcher.Poll(context.Background(), job, nextRun.Add(time.Minute))
	if err != nil {
		t.Fatalf("Poll() error: %v", err)
	}
	if pollResult.Status != gateway.CronRunStatusCompleted {
		t.Fatalf("poll status = %q, want completed", pollResult.Status)
	}

	events, err := store.List(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type != sessionrt.EventMessage {
			continue
		}
		msg, ok := event.Payload.(sessionrt.Message)
		if !ok {
			continue
		}
		if msg.Role != sessionrt.RoleAgent || msg.TargetActorID != "agent:milo" {
			continue
		}
		if strings.Contains(msg.Content, "[scheduled task result]") && strings.Contains(msg.Content, "run_status: completed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected targeted scheduled task result message")
	}
}
