package gateway

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const defaultHeartbeatPollInterval = time.Second

type HeartbeatSchedule struct {
	AgentID     sessionrt.ActorID
	Every       time.Duration
	Prompt      string
	AckMaxChars int
}

type HeartbeatRunnerOptions struct {
	Manager      sessionrt.SessionManager
	Pipeline     *DMPipeline
	Schedules    []HeartbeatSchedule
	Now          func() time.Time
	PollInterval time.Duration
	Logger       *log.Logger
}

type HeartbeatRunner struct {
	manager      sessionrt.SessionManager
	pipeline     *DMPipeline
	schedules    []HeartbeatSchedule
	now          func() time.Time
	pollInterval time.Duration
	logger       *log.Logger
	nextRun      map[sessionrt.ActorID]time.Time

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

func NewHeartbeatRunner(opts HeartbeatRunnerOptions) (*HeartbeatRunner, error) {
	if opts.Manager == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	if opts.Pipeline == nil {
		return nil, fmt.Errorf("dm pipeline is required")
	}
	if len(opts.Schedules) == 0 {
		return nil, fmt.Errorf("at least one heartbeat schedule is required")
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultHeartbeatPollInterval
	}

	normalized := make([]HeartbeatSchedule, 0, len(opts.Schedules))
	seen := map[sessionrt.ActorID]struct{}{}
	for _, schedule := range opts.Schedules {
		agentID := sessionrt.ActorID(strings.TrimSpace(string(schedule.AgentID)))
		if strings.TrimSpace(string(agentID)) == "" {
			return nil, fmt.Errorf("heartbeat schedule agent id is required")
		}
		if _, exists := seen[agentID]; exists {
			return nil, fmt.Errorf("duplicate heartbeat schedule for agent %q", agentID)
		}
		seen[agentID] = struct{}{}
		if schedule.Every <= 0 {
			return nil, fmt.Errorf("heartbeat schedule for agent %q must have every > 0", agentID)
		}
		schedule.AgentID = agentID
		schedule.Prompt = strings.TrimSpace(schedule.Prompt)
		if schedule.Prompt == "" {
			schedule.Prompt = "Run heartbeat checks. If no action is needed, reply exactly HEARTBEAT_OK."
		}
		if schedule.AckMaxChars <= 0 {
			schedule.AckMaxChars = heartbeatAckDefaultChars
		}
		normalized = append(normalized, schedule)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return string(normalized[i].AgentID) < string(normalized[j].AgentID)
	})

	return &HeartbeatRunner{
		manager:      opts.Manager,
		pipeline:     opts.Pipeline,
		schedules:    normalized,
		now:          nowFn,
		pollInterval: pollInterval,
		logger:       opts.Logger,
		nextRun:      map[sessionrt.ActorID]time.Time{},
	}, nil
}

func (r *HeartbeatRunner) Start(ctx context.Context) error {
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
	now := r.now().UTC()
	for _, schedule := range r.schedules {
		r.nextRun[schedule.AgentID] = now.Add(schedule.Every)
	}
	go r.loop(loopCtx)
	r.running = true
	return nil
}

func (r *HeartbeatRunner) Stop() {
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

func (r *HeartbeatRunner) loop(ctx context.Context) {
	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.processDue(ctx)
		}
	}
}

func (r *HeartbeatRunner) processDue(ctx context.Context) {
	now := r.now().UTC()
	targets := r.pipeline.HeartbeatTargets()
	targetsByAgent := make(map[sessionrt.ActorID][]HeartbeatTarget, len(r.schedules))
	for _, target := range targets {
		targetsByAgent[target.AgentID] = append(targetsByAgent[target.AgentID], target)
	}

	for _, schedule := range r.schedules {
		next, ok := r.nextRun[schedule.AgentID]
		if !ok {
			next = now
		}
		if next.After(now) {
			continue
		}

		agentTargets := targetsByAgent[schedule.AgentID]
		for _, target := range agentTargets {
			if r.pipeline.IsConversationProcessing(target.ConversationID) {
				continue
			}
			r.pipeline.MarkHeartbeatPending(target.ConversationID, schedule.AckMaxChars)
			err := r.manager.SendEvent(ctx, sessionrt.Event{
				SessionID: target.SessionID,
				From:      sessionrt.SystemActorID,
				Type:      sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleUser,
					Content: schedule.Prompt,
				},
			})
			if err != nil {
				r.pipeline.UnmarkHeartbeatPending(target.ConversationID)
				if r.logger != nil {
					r.logger.Printf("heartbeat dispatch failed agent=%s conversation=%s session=%s err=%v", schedule.AgentID, target.ConversationID, target.SessionID, err)
				}
			}
		}

		r.nextRun[schedule.AgentID] = now.Add(schedule.Every)
	}
}
