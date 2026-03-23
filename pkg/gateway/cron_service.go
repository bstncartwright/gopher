package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type CronDispatcher interface {
	Dispatch(ctx context.Context, job CronJob, firedAt time.Time) (CronDispatchResult, error)
	Poll(ctx context.Context, job CronJob, now time.Time) (CronDispatchResult, error)
}

type CronDispatchResult struct {
	Status      string
	Summary     string
	Error       string
	ActiveRunID string
}

type CronServiceOptions struct {
	Store              CronStore
	Dispatcher         CronDispatcher
	Now                func() time.Time
	DefaultTimezone    string
	CatchupOnStartOnce bool
}

type CronService struct {
	store              CronStore
	dispatcher         CronDispatcher
	now                func() time.Time
	defaultTimezone    string
	catchupOnStartOnce bool
	counter            uint64
}

type CronListOptions struct {
	SessionID string
}

func NewCronService(opts CronServiceOptions) (*CronService, error) {
	if opts.Store == nil {
		slog.Error("cron_service: cron store is required")
		return nil, fmt.Errorf("cron store is required")
	}
	if opts.Dispatcher == nil {
		slog.Error("cron_service: cron dispatcher is required")
		return nil, fmt.Errorf("cron dispatcher is required")
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	defaultTimezone := normalizeCronTimezone(opts.DefaultTimezone)
	if _, err := loadCronLocation(defaultTimezone); err != nil {
		slog.Error("cron_service: failed to load default timezone", "timezone", defaultTimezone, "error", err)
		return nil, err
	}
	slog.Info("cron_service: initialized", "default_timezone", defaultTimezone, "catchup_on_start_once", opts.CatchupOnStartOnce)
	return &CronService{
		store:              opts.Store,
		dispatcher:         opts.Dispatcher,
		now:                nowFn,
		defaultTimezone:    defaultTimezone,
		catchupOnStartOnce: opts.CatchupOnStartOnce,
	}, nil
}

func (s *CronService) Create(_ context.Context, input CronCreateInput) (CronJob, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		slog.Error("cron_service: session id is required")
		return CronJob{}, fmt.Errorf("session id is required")
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		slog.Error("cron_service: message is required", "session_id", sessionID)
		return CronJob{}, fmt.Errorf("message is required")
	}
	expr := strings.TrimSpace(input.CronExpr)
	if expr == "" {
		slog.Error("cron_service: cron expression is required", "session_id", sessionID)
		return CronJob{}, fmt.Errorf("cron expression is required")
	}
	mode := normalizeCronMode(input.Mode)
	if mode == "" {
		slog.Error("cron_service: invalid cron mode", "session_id", sessionID, "mode", strings.TrimSpace(input.Mode))
		return CronJob{}, fmt.Errorf("invalid cron mode %q", strings.TrimSpace(input.Mode))
	}
	notifyActorID := strings.TrimSpace(input.NotifyActorID)
	if notifyActorID == "" {
		slog.Error("cron_service: notify actor id is required", "session_id", sessionID)
		return CronJob{}, fmt.Errorf("notify actor id is required")
	}
	timezone := normalizeCronTimezone(input.Timezone)
	if input.Timezone == "" {
		timezone = s.defaultTimezone
	}
	now := s.now().UTC()
	nextRun, err := nextCronRun(expr, timezone, now)
	if err != nil {
		slog.Error("cron_service: failed to resolve next run", "session_id", sessionID, "cron_expr", expr, "timezone", timezone, "error", err)
		return CronJob{}, err
	}
	id := s.newJobID()
	job := CronJob{
		ID:            id,
		SessionID:     sessionID,
		Title:         strings.TrimSpace(input.Title),
		Message:       message,
		CronExpr:      expr,
		Timezone:      timezone,
		Mode:          mode,
		NotifyActorID: notifyActorID,
		TargetAgent:   strings.TrimSpace(input.TargetAgent),
		ModelPolicy:   strings.TrimSpace(input.ModelPolicy),
		Enabled:       true,
		CreatedBy:     strings.TrimSpace(input.CreatedBy),
		CreatedAt:     now,
		UpdatedAt:     now,
		NextRunAt:     ptrTime(nextRun),
	}
	if err := s.store.Create(job); err != nil {
		slog.Error("cron_service: failed to persist cron job", "job_id", id, "session_id", sessionID, "error", err)
		return CronJob{}, err
	}
	slog.Info("cron_service: cron job created", "job_id", job.ID, "session_id", job.SessionID, "mode", job.Mode, "next_run_at", job.NextRunAt)
	return job, nil
}

func (s *CronService) List(_ context.Context, opts CronListOptions) ([]CronJob, error) {
	jobs := s.store.List()
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		slog.Debug("cron_service: listed cron jobs", "scope", "all", "count", len(jobs))
		return jobs, nil
	}
	filtered := make([]CronJob, 0, len(jobs))
	for _, job := range jobs {
		if job.SessionID == sessionID {
			filtered = append(filtered, job)
		}
	}
	slog.Debug("cron_service: listed cron jobs", "scope", "session", "session_id", sessionID, "count", len(filtered))
	return filtered, nil
}

func (s *CronService) Delete(_ context.Context, jobID string) (bool, error) {
	ok := s.store.Delete(jobID)
	slog.Info("cron_service: deleted cron job", "job_id", strings.TrimSpace(jobID), "deleted", ok)
	return ok, nil
}

func (s *CronService) Pause(_ context.Context, jobID string) (CronJob, error) {
	job, ok := s.store.Get(jobID)
	if !ok {
		slog.Warn("cron_service: cron job not found for pause", "job_id", strings.TrimSpace(jobID))
		return CronJob{}, fmt.Errorf("cron job not found: %s", strings.TrimSpace(jobID))
	}
	now := s.now().UTC()
	job.Enabled = false
	job.UpdatedAt = now
	job.NextRunAt = nil
	job.ActiveScheduledFor = nil
	job.ActiveRunID = ""
	job.LastRunStatus = ""
	job.LastRunSummary = ""
	job.LastRunError = ""
	if err := s.store.Update(job); err != nil {
		slog.Error("cron_service: failed to pause cron job", "job_id", job.ID, "error", err)
		return CronJob{}, err
	}
	slog.Info("cron_service: cron job paused", "job_id", job.ID)
	return job, nil
}

func (s *CronService) Resume(_ context.Context, jobID string) (CronJob, error) {
	job, ok := s.store.Get(jobID)
	if !ok {
		slog.Warn("cron_service: cron job not found for resume", "job_id", strings.TrimSpace(jobID))
		return CronJob{}, fmt.Errorf("cron job not found: %s", strings.TrimSpace(jobID))
	}
	now := s.now().UTC()
	nextRun, err := nextCronRun(job.CronExpr, job.Timezone, now)
	if err != nil {
		slog.Error("cron_service: failed to compute next run during resume", "job_id", job.ID, "error", err)
		return CronJob{}, err
	}
	job.Enabled = true
	job.UpdatedAt = now
	job.NextRunAt = ptrTime(nextRun)
	job.ActiveScheduledFor = nil
	job.ActiveRunID = ""
	job.LastRunStatus = ""
	job.LastRunSummary = ""
	job.LastRunError = ""
	if err := s.store.Update(job); err != nil {
		slog.Error("cron_service: failed to resume cron job", "job_id", job.ID, "error", err)
		return CronJob{}, err
	}
	slog.Info("cron_service: cron job resumed", "job_id", job.ID, "next_run_at", job.NextRunAt)
	return job, nil
}

func (s *CronService) ProcessDue(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now().UTC()
	jobs := s.store.List()
	slog.Debug("cron_service: processing due jobs", "count", len(jobs), "now", now.Format(time.RFC3339Nano))
	s.reconcileRunning(ctx, now, jobs)
	sort.Slice(jobs, func(i, j int) bool {
		left := jobs[i].NextRunAt
		right := jobs[j].NextRunAt
		if left == nil && right == nil {
			return jobs[i].ID < jobs[j].ID
		}
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		if left.Equal(*right) {
			return jobs[i].ID < jobs[j].ID
		}
		return left.Before(*right)
	})
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(job.LastRunStatus), CronRunStatusRunning) {
			continue
		}
		if job.NextRunAt == nil {
			nextRun, err := nextCronRun(job.CronExpr, job.Timezone, now)
			if err != nil {
				continue
			}
			job.NextRunAt = ptrTime(nextRun)
			job.UpdatedAt = now
			_ = s.store.Update(job)
			continue
		}
		if job.NextRunAt.After(now) {
			continue
		}
		slog.Debug("cron_service: job due for dispatch", "job_id", job.ID, "scheduled_for", job.NextRunAt)
		s.dispatchJob(ctx, job, now)
	}
	return nil
}

func (s *CronService) PrepareOnStart(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now().UTC()
	jobs := s.store.List()
	slog.Debug("cron_service: preparing on start", "count", len(jobs), "now", now.Format(time.RFC3339Nano))
	s.reconcileRunning(ctx, now, jobs)
	for _, job := range jobs {
		if !job.Enabled {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(job.LastRunStatus), CronRunStatusRunning) {
			continue
		}
		if job.NextRunAt == nil {
			nextRun, err := nextCronRun(job.CronExpr, job.Timezone, now)
			if err != nil {
				continue
			}
			job.NextRunAt = ptrTime(nextRun)
			job.UpdatedAt = now
			_ = s.store.Update(job)
			continue
		}
		if !s.catchupOnStartOnce || job.NextRunAt.After(now) {
			continue
		}
		slog.Debug("cron_service: catching up overdue job on start", "job_id", job.ID, "scheduled_for", job.NextRunAt)
		s.dispatchJob(ctx, job, now)
	}
	return nil
}

func (s *CronService) dispatchJob(ctx context.Context, job CronJob, now time.Time) {
	dispatchJob := cloneCronJob(job)
	scheduledFor := now
	if job.NextRunAt != nil && !job.NextRunAt.IsZero() {
		scheduledFor = job.NextRunAt.UTC()
	}
	slog.Info("cron_service: dispatching job", "job_id", job.ID, "session_id", job.SessionID, "mode", job.Mode, "now", now.Format(time.RFC3339Nano))
	job.LastRunStatus = CronRunStatusRunning
	job.LastRunSummary = ""
	job.LastRunError = ""
	job.ActiveRunID = ""
	job.ActiveScheduledFor = ptrTime(scheduledFor)
	job.UpdatedAt = now
	job.NextRunAt = nil
	if err := s.store.Update(job); err != nil {
		slog.Error("cron_service: failed to mark job running", "job_id", job.ID, "error", err)
		return
	}

	result, err := s.dispatcher.Dispatch(ctx, dispatchJob, now)
	if err != nil {
		slog.Error("cron_service: job dispatch failed", "job_id", job.ID, "error", err)
		s.finalizeJob(job, CronDispatchResult{
			Status:  CronRunStatusFailed,
			Error:   err.Error(),
			Summary: err.Error(),
		}, now, scheduledFor)
		return
	}
	if !validCronRunStatus(result.Status) || strings.TrimSpace(result.Status) == "" {
		result.Status = CronRunStatusCompleted
	}
	if strings.EqualFold(strings.TrimSpace(result.Status), CronRunStatusRunning) {
		job.ActiveRunID = strings.TrimSpace(result.ActiveRunID)
		job.LastRunStatus = CronRunStatusRunning
		job.LastRunSummary = strings.TrimSpace(result.Summary)
		job.LastRunError = ""
		job.UpdatedAt = now
		if err := s.store.Update(job); err != nil {
			slog.Error("cron_service: failed to persist running job state", "job_id", job.ID, "error", err)
			return
		}
		slog.Info("cron_service: job running", "job_id", job.ID, "active_run_id", job.ActiveRunID)
		return
	}
	s.finalizeJob(job, result, now, scheduledFor)
}

func (s *CronService) reconcileRunning(ctx context.Context, now time.Time, jobs []CronJob) {
	for _, job := range jobs {
		if !strings.EqualFold(strings.TrimSpace(job.LastRunStatus), CronRunStatusRunning) {
			continue
		}
		slog.Debug("cron_service: reconciling running job", "job_id", job.ID, "active_run_id", job.ActiveRunID)
		if strings.TrimSpace(job.ActiveRunID) == "" {
			slog.Warn("cron_service: running job missing active run id", "job_id", job.ID)
			s.finalizeJob(job, CronDispatchResult{
				Status:  CronRunStatusFailed,
				Error:   "scheduled task run lost active run id",
				Summary: "scheduled task run lost active run id",
			}, now, resolvedScheduledFor(job, now))
			continue
		}
		result, err := s.dispatcher.Poll(ctx, job, now)
		if err != nil {
			slog.Error("cron_service: failed to poll running job", "job_id", job.ID, "error", err)
			s.finalizeJob(job, CronDispatchResult{
				Status:  CronRunStatusFailed,
				Error:   err.Error(),
				Summary: err.Error(),
			}, now, resolvedScheduledFor(job, now))
			continue
		}
		if strings.EqualFold(strings.TrimSpace(result.Status), CronRunStatusRunning) || strings.TrimSpace(result.Status) == "" {
			continue
		}
		slog.Info("cron_service: running job completed during poll", "job_id", job.ID, "status", result.Status)
		s.finalizeJob(job, result, now, resolvedScheduledFor(job, now))
	}
}

func (s *CronService) finalizeJob(job CronJob, result CronDispatchResult, now time.Time, scheduledFor time.Time) {
	status := strings.ToLower(strings.TrimSpace(result.Status))
	if !validCronRunStatus(status) || status == "" {
		status = CronRunStatusCompleted
	}
	nextRun, err := nextCronRun(job.CronExpr, job.Timezone, scheduledFor)
	if err != nil {
		slog.Error("cron_service: failed to compute next run while finalizing job", "job_id", job.ID, "error", err)
		return
	}
	job.ActiveRunID = ""
	job.ActiveScheduledFor = nil
	job.LastRunStatus = status
	job.LastRunSummary = strings.TrimSpace(result.Summary)
	job.LastRunError = strings.TrimSpace(result.Error)
	job.LastRunAt = ptrTime(now)
	job.NextRunAt = ptrTime(nextRun)
	job.UpdatedAt = now
	if err := s.store.Update(job); err != nil {
		slog.Error("cron_service: failed to persist finalized job", "job_id", job.ID, "status", status, "error", err)
		return
	}
	overdue := job.NextRunAt != nil && !job.NextRunAt.After(now)
	slog.Info("cron_service: job finalized", "job_id", job.ID, "status", status, "next_run_at", job.NextRunAt, "scheduled_for", scheduledFor.Format(time.RFC3339Nano), "backlog_remaining", overdue)
}

func (s *CronService) newJobID() string {
	seq := atomic.AddUint64(&s.counter, 1)
	return fmt.Sprintf("cron-%d-%d", s.now().UTC().UnixNano(), seq)
}

func ptrTime(value time.Time) *time.Time {
	utc := value.UTC()
	return &utc
}

func resolvedScheduledFor(job CronJob, fallback time.Time) time.Time {
	if job.ActiveScheduledFor != nil && !job.ActiveScheduledFor.IsZero() {
		return job.ActiveScheduledFor.UTC()
	}
	return fallback.UTC()
}
