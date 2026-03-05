package gateway

import (
	"context"
	"fmt"
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
		return nil, fmt.Errorf("cron store is required")
	}
	if opts.Dispatcher == nil {
		return nil, fmt.Errorf("cron dispatcher is required")
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	defaultTimezone := normalizeCronTimezone(opts.DefaultTimezone)
	if _, err := loadCronLocation(defaultTimezone); err != nil {
		return nil, err
	}
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
		return CronJob{}, fmt.Errorf("session id is required")
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		return CronJob{}, fmt.Errorf("message is required")
	}
	expr := strings.TrimSpace(input.CronExpr)
	if expr == "" {
		return CronJob{}, fmt.Errorf("cron expression is required")
	}
	mode := normalizeCronMode(input.Mode)
	if mode == "" {
		return CronJob{}, fmt.Errorf("invalid cron mode %q", strings.TrimSpace(input.Mode))
	}
	notifyActorID := strings.TrimSpace(input.NotifyActorID)
	if notifyActorID == "" {
		return CronJob{}, fmt.Errorf("notify actor id is required")
	}
	timezone := normalizeCronTimezone(input.Timezone)
	if input.Timezone == "" {
		timezone = s.defaultTimezone
	}
	now := s.now().UTC()
	nextRun, err := nextCronRun(expr, timezone, now)
	if err != nil {
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
		return CronJob{}, err
	}
	return job, nil
}

func (s *CronService) List(_ context.Context, opts CronListOptions) ([]CronJob, error) {
	jobs := s.store.List()
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		return jobs, nil
	}
	filtered := make([]CronJob, 0, len(jobs))
	for _, job := range jobs {
		if job.SessionID == sessionID {
			filtered = append(filtered, job)
		}
	}
	return filtered, nil
}

func (s *CronService) Delete(_ context.Context, jobID string) (bool, error) {
	ok := s.store.Delete(jobID)
	return ok, nil
}

func (s *CronService) Pause(_ context.Context, jobID string) (CronJob, error) {
	job, ok := s.store.Get(jobID)
	if !ok {
		return CronJob{}, fmt.Errorf("cron job not found: %s", strings.TrimSpace(jobID))
	}
	now := s.now().UTC()
	job.Enabled = false
	job.UpdatedAt = now
	job.NextRunAt = nil
	job.ActiveRunID = ""
	job.LastRunStatus = ""
	job.LastRunSummary = ""
	job.LastRunError = ""
	if err := s.store.Update(job); err != nil {
		return CronJob{}, err
	}
	return job, nil
}

func (s *CronService) Resume(_ context.Context, jobID string) (CronJob, error) {
	job, ok := s.store.Get(jobID)
	if !ok {
		return CronJob{}, fmt.Errorf("cron job not found: %s", strings.TrimSpace(jobID))
	}
	now := s.now().UTC()
	nextRun, err := nextCronRun(job.CronExpr, job.Timezone, now)
	if err != nil {
		return CronJob{}, err
	}
	job.Enabled = true
	job.UpdatedAt = now
	job.NextRunAt = ptrTime(nextRun)
	job.ActiveRunID = ""
	job.LastRunStatus = ""
	job.LastRunSummary = ""
	job.LastRunError = ""
	if err := s.store.Update(job); err != nil {
		return CronJob{}, err
	}
	return job, nil
}

func (s *CronService) ProcessDue(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now().UTC()
	jobs := s.store.List()
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
		s.dispatchJob(ctx, job, now)
	}
	return nil
}

func (s *CronService) dispatchJob(ctx context.Context, job CronJob, now time.Time) {
	dispatchJob := cloneCronJob(job)
	job.LastRunStatus = CronRunStatusRunning
	job.LastRunSummary = ""
	job.LastRunError = ""
	job.ActiveRunID = ""
	job.UpdatedAt = now
	job.NextRunAt = nil
	if err := s.store.Update(job); err != nil {
		return
	}

	result, err := s.dispatcher.Dispatch(ctx, dispatchJob, now)
	if err != nil {
		s.finalizeJob(job, CronDispatchResult{
			Status:  CronRunStatusFailed,
			Error:   err.Error(),
			Summary: err.Error(),
		}, now)
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
		_ = s.store.Update(job)
		return
	}
	s.finalizeJob(job, result, now)
}

func (s *CronService) reconcileRunning(ctx context.Context, now time.Time, jobs []CronJob) {
	for _, job := range jobs {
		if !strings.EqualFold(strings.TrimSpace(job.LastRunStatus), CronRunStatusRunning) {
			continue
		}
		if strings.TrimSpace(job.ActiveRunID) == "" {
			s.finalizeJob(job, CronDispatchResult{
				Status:  CronRunStatusFailed,
				Error:   "scheduled task run lost active run id",
				Summary: "scheduled task run lost active run id",
			}, now)
			continue
		}
		result, err := s.dispatcher.Poll(ctx, job, now)
		if err != nil {
			s.finalizeJob(job, CronDispatchResult{
				Status:  CronRunStatusFailed,
				Error:   err.Error(),
				Summary: err.Error(),
			}, now)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(result.Status), CronRunStatusRunning) || strings.TrimSpace(result.Status) == "" {
			continue
		}
		s.finalizeJob(job, result, now)
	}
}

func (s *CronService) finalizeJob(job CronJob, result CronDispatchResult, now time.Time) {
	status := strings.ToLower(strings.TrimSpace(result.Status))
	if !validCronRunStatus(status) || status == "" {
		status = CronRunStatusCompleted
	}
	nextRun, err := nextCronRun(job.CronExpr, job.Timezone, now)
	if err != nil {
		return
	}
	job.ActiveRunID = ""
	job.LastRunStatus = status
	job.LastRunSummary = strings.TrimSpace(result.Summary)
	job.LastRunError = strings.TrimSpace(result.Error)
	job.LastRunAt = ptrTime(now)
	job.NextRunAt = ptrTime(nextRun)
	job.UpdatedAt = now
	_ = s.store.Update(job)
}

func (s *CronService) newJobID() string {
	seq := atomic.AddUint64(&s.counter, 1)
	return fmt.Sprintf("cron-%d-%d", s.now().UTC().UnixNano(), seq)
}

func ptrTime(value time.Time) *time.Time {
	utc := value.UTC()
	return &utc
}
