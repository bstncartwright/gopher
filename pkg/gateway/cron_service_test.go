package gateway

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeCronDispatcher struct {
	calls          []CronJob
	dispatchResult CronDispatchResult
	pollResult     CronDispatchResult
}

func (d *fakeCronDispatcher) Dispatch(_ context.Context, job CronJob, _ time.Time) (CronDispatchResult, error) {
	d.calls = append(d.calls, job)
	if strings.TrimSpace(d.dispatchResult.Status) != "" {
		return d.dispatchResult, nil
	}
	return CronDispatchResult{Status: CronRunStatusCompleted}, nil
}

func (d *fakeCronDispatcher) Poll(_ context.Context, _ CronJob, _ time.Time) (CronDispatchResult, error) {
	if strings.TrimSpace(d.pollResult.Status) != "" {
		return d.pollResult, nil
	}
	return CronDispatchResult{Status: CronRunStatusCompleted}, nil
}

func TestCronServiceProcessDueDispatchesAndAdvancesSchedule(t *testing.T) {
	now := time.Date(2026, 2, 17, 10, 0, 0, 0, time.UTC)
	store := NewInMemoryCronStore()
	dispatcher := &fakeCronDispatcher{}
	service, err := NewCronService(CronServiceOptions{
		Store:           store,
		Dispatcher:      dispatcher,
		Now:             func() time.Time { return now },
		DefaultTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("NewCronService() error: %v", err)
	}

	job, err := service.Create(context.Background(), CronCreateInput{
		SessionID:     "sess-1",
		Message:       "ping",
		CronExpr:      "* * * * *",
		NotifyActorID: "agent:test",
		CreatedBy:     "agent:test",
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	created, ok := store.Get(job.ID)
	if !ok {
		t.Fatalf("expected job to exist")
	}
	past := now.Add(-time.Minute)
	created.NextRunAt = &past
	if err := store.Update(created); err != nil {
		t.Fatalf("Update() error: %v", err)
	}

	if err := service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue() error: %v", err)
	}
	if len(dispatcher.calls) != 1 {
		t.Fatalf("dispatch calls = %d, want 1", len(dispatcher.calls))
	}
	updated, ok := store.Get(job.ID)
	if !ok {
		t.Fatalf("expected updated job to exist")
	}
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(now) {
		t.Fatalf("last run = %#v, want %s", updated.LastRunAt, now)
	}
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(now) {
		t.Fatalf("next run = %#v, want %s to preserve backlog", updated.NextRunAt, now)
	}
}

func TestCronServicePrepareOnStartCatchupOnce(t *testing.T) {
	now := time.Date(2026, 2, 17, 10, 0, 0, 0, time.UTC)
	store := NewInMemoryCronStore()
	dispatcher := &fakeCronDispatcher{}
	service, err := NewCronService(CronServiceOptions{
		Store:              store,
		Dispatcher:         dispatcher,
		Now:                func() time.Time { return now },
		DefaultTimezone:    "UTC",
		CatchupOnStartOnce: true,
	})
	if err != nil {
		t.Fatalf("NewCronService() error: %v", err)
	}

	past := now.Add(-10 * time.Minute)
	job := CronJob{
		ID:            "cron-1",
		SessionID:     "sess-1",
		Message:       "catchup",
		CronExpr:      "*/5 * * * *",
		Timezone:      "UTC",
		Mode:          CronModeSession,
		NotifyActorID: "agent:test",
		Enabled:       true,
		CreatedBy:     "agent:test",
		CreatedAt:     past.Add(-time.Hour),
		UpdatedAt:     past,
		NextRunAt:     &past,
	}
	if err := store.Create(job); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	if err := service.PrepareOnStart(context.Background()); err != nil {
		t.Fatalf("PrepareOnStart() error: %v", err)
	}
	if len(dispatcher.calls) != 1 {
		t.Fatalf("dispatch calls = %d, want 1", len(dispatcher.calls))
	}
	updated, ok := store.Get("cron-1")
	if !ok {
		t.Fatalf("job missing after prepare")
	}
	if updated.LastRunAt == nil || !updated.LastRunAt.Equal(now) {
		t.Fatalf("last run = %#v, want %s", updated.LastRunAt, now)
	}
	wantNext := now.Add(-5 * time.Minute)
	if updated.NextRunAt == nil || !updated.NextRunAt.Equal(wantNext) {
		t.Fatalf("next run = %#v, want %s to preserve backlog", updated.NextRunAt, wantNext)
	}
}

func TestCronServiceProcessDueTracksRunningIsolatedJobUntilPollCompletion(t *testing.T) {
	now := time.Date(2026, 2, 17, 10, 0, 0, 0, time.UTC)
	current := now
	store := NewInMemoryCronStore()
	dispatcher := &fakeCronDispatcher{
		dispatchResult: CronDispatchResult{
			Status:      CronRunStatusRunning,
			ActiveRunID: "delegation-1",
			Summary:     "Spawned subagent worker in session delegation-1.",
		},
		pollResult: CronDispatchResult{
			Status:  CronRunStatusCompleted,
			Summary: "Completed after 3 events.",
		},
	}
	service, err := NewCronService(CronServiceOptions{
		Store:           store,
		Dispatcher:      dispatcher,
		Now:             func() time.Time { return current },
		DefaultTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("NewCronService() error: %v", err)
	}

	job, err := service.Create(context.Background(), CronCreateInput{
		SessionID:     "sess-1",
		Title:         "Morning scan",
		Message:       "Summarize overnight updates.",
		CronExpr:      "* * * * *",
		Mode:          CronModeIsolated,
		NotifyActorID: "agent:test",
		CreatedBy:     "agent:test",
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	created, ok := store.Get(job.ID)
	if !ok {
		t.Fatalf("expected job to exist")
	}
	past := now.Add(-time.Minute)
	created.NextRunAt = &past
	if err := store.Update(created); err != nil {
		t.Fatalf("Update() error: %v", err)
	}

	if err := service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("ProcessDue() error: %v", err)
	}
	running, ok := store.Get(job.ID)
	if !ok {
		t.Fatalf("expected running job to exist")
	}
	if running.LastRunStatus != CronRunStatusRunning {
		t.Fatalf("last run status = %q, want running", running.LastRunStatus)
	}
	if running.ActiveRunID != "delegation-1" {
		t.Fatalf("active run id = %q, want delegation-1", running.ActiveRunID)
	}
	if running.NextRunAt != nil {
		t.Fatalf("next run = %#v, want nil while running", running.NextRunAt)
	}

	current = now.Add(30 * time.Second)
	if err := service.ProcessDue(context.Background()); err != nil {
		t.Fatalf("second ProcessDue() error: %v", err)
	}
	completed, ok := store.Get(job.ID)
	if !ok {
		t.Fatalf("expected completed job to exist")
	}
	if completed.LastRunStatus != CronRunStatusCompleted {
		t.Fatalf("last run status = %q, want completed", completed.LastRunStatus)
	}
	if completed.ActiveRunID != "" {
		t.Fatalf("active run id = %q, want empty", completed.ActiveRunID)
	}
	if completed.LastRunSummary != "Completed after 3 events." {
		t.Fatalf("last run summary = %q", completed.LastRunSummary)
	}
	if completed.NextRunAt == nil || !completed.NextRunAt.Equal(now) {
		t.Fatalf("next run = %#v, want %s to preserve backlog", completed.NextRunAt, now)
	}
}

func TestCronServiceProcessDueReplaysMissedRunsOnePerPoll(t *testing.T) {
	now := time.Date(2026, 2, 17, 10, 0, 0, 0, time.UTC)
	store := NewInMemoryCronStore()
	dispatcher := &fakeCronDispatcher{}
	service, err := NewCronService(CronServiceOptions{
		Store:           store,
		Dispatcher:      dispatcher,
		Now:             func() time.Time { return now },
		DefaultTimezone: "UTC",
	})
	if err != nil {
		t.Fatalf("NewCronService() error: %v", err)
	}

	scheduledFor := now.Add(-15 * time.Minute)
	job := CronJob{
		ID:            "cron-backlog",
		SessionID:     "sess-1",
		Message:       "catch up",
		CronExpr:      "*/5 * * * *",
		Timezone:      "UTC",
		Mode:          CronModeSession,
		NotifyActorID: "agent:test",
		Enabled:       true,
		CreatedBy:     "agent:test",
		CreatedAt:     scheduledFor.Add(-time.Hour),
		UpdatedAt:     scheduledFor,
		NextRunAt:     &scheduledFor,
	}
	if err := store.Create(job); err != nil {
		t.Fatalf("Create() error: %v", err)
	}

	wantNextRuns := []time.Time{
		now.Add(-10 * time.Minute),
		now.Add(-5 * time.Minute),
		now,
		now.Add(5 * time.Minute),
	}
	for idx, want := range wantNextRuns {
		if err := service.ProcessDue(context.Background()); err != nil {
			t.Fatalf("ProcessDue(%d) error: %v", idx, err)
		}
		updated, ok := store.Get(job.ID)
		if !ok {
			t.Fatalf("job missing after poll %d", idx)
		}
		if updated.NextRunAt == nil || !updated.NextRunAt.Equal(want) {
			t.Fatalf("poll %d next run = %#v, want %s", idx, updated.NextRunAt, want)
		}
	}
	if len(dispatcher.calls) != 4 {
		t.Fatalf("dispatch calls = %d, want 4 backlog replays", len(dispatcher.calls))
	}
}
