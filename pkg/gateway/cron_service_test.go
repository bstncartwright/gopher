package gateway

import (
	"context"
	"testing"
	"time"
)

type fakeCronDispatcher struct {
	calls []CronJob
}

func (d *fakeCronDispatcher) Dispatch(_ context.Context, job CronJob, _ time.Time) error {
	d.calls = append(d.calls, job)
	return nil
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
		SessionID: "sess-1",
		Message:   "ping",
		CronExpr:  "* * * * *",
		CreatedBy: "agent:test",
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
	if updated.NextRunAt == nil || !updated.NextRunAt.After(now) {
		t.Fatalf("next run = %#v, want future time", updated.NextRunAt)
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
		ID:        "cron-1",
		SessionID: "sess-1",
		Message:   "catchup",
		CronExpr:  "*/5 * * * *",
		Timezone:  "UTC",
		Enabled:   true,
		CreatedBy: "agent:test",
		CreatedAt: past.Add(-time.Hour),
		UpdatedAt: past,
		NextRunAt: &past,
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
	if updated.NextRunAt == nil || !updated.NextRunAt.After(now) {
		t.Fatalf("next run = %#v, want future", updated.NextRunAt)
	}
}
