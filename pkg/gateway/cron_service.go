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
	Dispatch(ctx context.Context, job CronJob, firedAt time.Time) error
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

func (s *CronService) Create(ctx context.Context, input CronCreateInput) (CronJob, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
		ID:        id,
		SessionID: sessionID,
		Message:   message,
		CronExpr:  expr,
		Timezone:  timezone,
		Enabled:   true,
		CreatedBy: strings.TrimSpace(input.CreatedBy),
		CreatedAt: now,
		UpdatedAt: now,
		NextRunAt: ptrTime(nextRun),
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
		if err := s.dispatcher.Dispatch(ctx, job, now); err != nil {
			continue
		}
		nextRun, err := nextCronRun(job.CronExpr, job.Timezone, now)
		if err != nil {
			continue
		}
		job.LastRunAt = ptrTime(now)
		job.NextRunAt = ptrTime(nextRun)
		job.UpdatedAt = now
		_ = s.store.Update(job)
	}
	return nil
}

func (s *CronService) PrepareOnStart(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	now := s.now().UTC()
	jobs := s.store.List()
	for _, job := range jobs {
		if !job.Enabled {
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
		if err := s.dispatcher.Dispatch(ctx, job, now); err != nil {
			continue
		}
		nextRun, err := nextCronRun(job.CronExpr, job.Timezone, now)
		if err != nil {
			continue
		}
		job.LastRunAt = ptrTime(now)
		job.NextRunAt = ptrTime(nextRun)
		job.UpdatedAt = now
		_ = s.store.Update(job)
	}
	return nil
}

func (s *CronService) newJobID() string {
	seq := atomic.AddUint64(&s.counter, 1)
	return fmt.Sprintf("cron-%d-%d", s.now().UTC().UnixNano(), seq)
}

func ptrTime(value time.Time) *time.Time {
	utc := value.UTC()
	return &utc
}
