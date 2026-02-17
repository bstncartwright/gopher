package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const defaultCronPollInterval = time.Second

type CronRunnerOptions struct {
	Service      *CronService
	PollInterval time.Duration
}

type CronRunner struct {
	service      *CronService
	pollInterval time.Duration

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

func NewCronRunner(opts CronRunnerOptions) (*CronRunner, error) {
	if opts.Service == nil {
		return nil, fmt.Errorf("cron service is required")
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = defaultCronPollInterval
	}
	return &CronRunner{
		service:      opts.Service,
		pollInterval: interval,
	}, nil
}

func (r *CronRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	loopCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	if err := r.service.PrepareOnStart(ctx); err != nil {
		cancel()
		return err
	}
	go r.loop(loopCtx)
	r.running = true
	return nil
}

func (r *CronRunner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.running = false
}

func (r *CronRunner) loop(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.service.ProcessDue(ctx)
		}
	}
}
