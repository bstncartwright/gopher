package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const defaultHeartbeatPollInterval = time.Second
const (
	defaultHeartbeatSleepStartHour = 22
	defaultHeartbeatSleepEndHour   = 8
)

type HeartbeatSchedule struct {
	AgentID     sessionrt.ActorID
	Every       time.Duration
	Prompt      string
	AckMaxChars int
	Timezone    string

	location *time.Location
}

type HeartbeatRunnerOptions struct {
	Manager      sessionrt.SessionManager
	Pipeline     *DMPipeline
	Schedules    []HeartbeatSchedule
	Now          func() time.Time
	PollInterval time.Duration
}

type HeartbeatRunner struct {
	manager      sessionrt.SessionManager
	pipeline     *DMPipeline
	schedules    []HeartbeatSchedule
	now          func() time.Time
	pollInterval time.Duration
	nextRun      map[sessionrt.ActorID]time.Time

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
}

func NewHeartbeatRunner(opts HeartbeatRunnerOptions) (*HeartbeatRunner, error) {
	if opts.Manager == nil {
		slog.Error("heartbeat_runner: session manager is required")
		return nil, fmt.Errorf("session manager is required")
	}
	if opts.Pipeline == nil {
		slog.Error("heartbeat_runner: dm pipeline is required")
		return nil, fmt.Errorf("dm pipeline is required")
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
		validated, err := normalizeHeartbeatSchedule(schedule)
		if err != nil {
			slog.Error("heartbeat_runner: invalid schedule", "agent_id", schedule.AgentID, "error", err)
			return nil, err
		}
		if _, exists := seen[validated.AgentID]; exists {
			slog.Error("heartbeat_runner: duplicate schedule", "agent_id", validated.AgentID)
			return nil, fmt.Errorf("duplicate heartbeat schedule for agent %q", validated.AgentID)
		}
		seen[validated.AgentID] = struct{}{}
		normalized = append(normalized, validated)
	}
	sort.Slice(normalized, func(i, j int) bool {
		return string(normalized[i].AgentID) < string(normalized[j].AgentID)
	})

	slog.Info("heartbeat_runner: created", "schedules_count", len(normalized), "poll_interval", pollInterval)
	return &HeartbeatRunner{
		manager:      opts.Manager,
		pipeline:     opts.Pipeline,
		schedules:    normalized,
		now:          nowFn,
		pollInterval: pollInterval,
		nextRun:      map[sessionrt.ActorID]time.Time{},
	}, nil
}

func normalizeHeartbeatSchedule(schedule HeartbeatSchedule) (HeartbeatSchedule, error) {
	agentID := sessionrt.ActorID(strings.TrimSpace(string(schedule.AgentID)))
	if strings.TrimSpace(string(agentID)) == "" {
		return HeartbeatSchedule{}, fmt.Errorf("heartbeat schedule agent id is required")
	}
	if schedule.Every <= 0 {
		return HeartbeatSchedule{}, fmt.Errorf("heartbeat schedule for agent %q must have every > 0", agentID)
	}
	schedule.AgentID = agentID
	schedule.Prompt = strings.TrimSpace(schedule.Prompt)
	if schedule.Prompt == "" {
		schedule.Prompt = "Run heartbeat checks. If no action is needed, reply exactly HEARTBEAT_OK."
	}
	if schedule.AckMaxChars <= 0 {
		schedule.AckMaxChars = heartbeatAckDefaultChars
	}
	schedule.Timezone = strings.TrimSpace(schedule.Timezone)
	schedule.location = nil
	if schedule.Timezone != "" {
		location, err := time.LoadLocation(schedule.Timezone)
		if err != nil {
			slog.Warn("heartbeat_runner: invalid timezone ignored", "agent_id", agentID, "timezone", schedule.Timezone, "error", err)
			schedule.Timezone = ""
		} else {
			schedule.location = location
		}
	}
	return schedule, nil
}

func (r *HeartbeatRunner) UpsertSchedule(schedule HeartbeatSchedule) error {
	normalized, err := normalizeHeartbeatSchedule(schedule)
	if err != nil {
		slog.Error("heartbeat_runner: upsert schedule failed validation", "agent_id", schedule.AgentID, "error", err)
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	replaced := false
	for i := range r.schedules {
		if r.schedules[i].AgentID == normalized.AgentID {
			r.schedules[i] = normalized
			replaced = true
			break
		}
	}
	if !replaced {
		r.schedules = append(r.schedules, normalized)
	}
	sort.Slice(r.schedules, func(i, j int) bool {
		return string(r.schedules[i].AgentID) < string(r.schedules[j].AgentID)
	})
	r.nextRun[normalized.AgentID] = now.Add(normalized.Every)
	slog.Info("heartbeat_runner: schedule upserted", "agent_id", normalized.AgentID, "every", normalized.Every, "replaced", replaced)
	return nil
}

func (r *HeartbeatRunner) RemoveSchedule(agentID sessionrt.ActorID) bool {
	target := sessionrt.ActorID(strings.TrimSpace(string(agentID)))
	if strings.TrimSpace(string(target)) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	index := -1
	for i := range r.schedules {
		if r.schedules[i].AgentID == target {
			index = i
			break
		}
	}
	if index == -1 {
		delete(r.nextRun, target)
		slog.Debug("heartbeat_runner: schedule not found for removal", "agent_id", target)
		return false
	}
	r.schedules = append(r.schedules[:index], r.schedules[index+1:]...)
	delete(r.nextRun, target)
	slog.Info("heartbeat_runner: schedule removed", "agent_id", target)
	return true
}

func (r *HeartbeatRunner) schedulesSnapshot() []HeartbeatSchedule {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]HeartbeatSchedule, len(r.schedules))
	copy(out, r.schedules)
	return out
}

func (r *HeartbeatRunner) nextRunFor(agentID sessionrt.ActorID, fallback time.Time) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	next, ok := r.nextRun[agentID]
	if !ok {
		return fallback
	}
	return next
}

func (r *HeartbeatRunner) setNextRun(agentID sessionrt.ActorID, next time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, schedule := range r.schedules {
		if schedule.AgentID == agentID {
			r.nextRun[agentID] = next
			return
		}
	}
	delete(r.nextRun, agentID)
}

func (r *HeartbeatRunner) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		slog.Debug("heartbeat_runner: already running")
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	loopCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	now := r.now().UTC()
	for _, schedule := range r.schedules {
		r.nextRun[schedule.AgentID] = now.Add(schedule.Every)
	}
	go r.loop(loopCtx)
	r.running = true
	slog.Info("heartbeat_runner: started", "schedules_count", len(r.schedules), "poll_interval", r.pollInterval)
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
	slog.Info("heartbeat_runner: stopped")
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
	sessionCache := map[sessionrt.SessionID]*sessionrt.Session{}
	sessionErrs := map[sessionrt.SessionID]error{}
	schedules := r.schedulesSnapshot()
	slog.Debug("heartbeat_runner: processing due schedules", "schedules_count", len(schedules), "targets_count", len(targets))

	for _, schedule := range schedules {
		next := r.nextRunFor(schedule.AgentID, now)
		if next.After(now) {
			continue
		}
		if isWithinHeartbeatSleepWindow(now, schedule.location) {
			slog.Debug("heartbeat_runner: skip sleep hours", "agent_id", schedule.AgentID, "timezone", schedule.Timezone, "local_hour", now.In(schedule.location).Hour())
			r.setNextRun(schedule.AgentID, now.Add(schedule.Every))
			continue
		}

		for _, target := range targets {
			if r.pipeline.IsConversationProcessing(target.ConversationID) {
				slog.Debug("heartbeat_runner: skip conversation processing", "agent_id", schedule.AgentID, "conversation_id", target.ConversationID, "session_id", target.SessionID)
				continue
			}
			session, err := r.loadSession(ctx, target.SessionID, sessionCache, sessionErrs)
			if err != nil {
				slog.Warn("heartbeat_runner: skip session load failed", "agent_id", schedule.AgentID, "conversation_id", target.ConversationID, "session_id", target.SessionID, "error", err)
				continue
			}
			if !heartbeatHasAgentParticipant(session, schedule.AgentID) {
				slog.Debug("heartbeat_runner: skip not participant", "agent_id", schedule.AgentID, "conversation_id", target.ConversationID, "session_id", target.SessionID)
				continue
			}
			if !r.pipeline.CanDispatchHeartbeat(target.ConversationID, schedule.AgentID) {
				slog.Debug("heartbeat_runner: skip not in room", "agent_id", schedule.AgentID, "conversation_id", target.ConversationID, "session_id", target.SessionID)
				continue
			}
			r.pipeline.MarkHeartbeatPending(target.ConversationID, schedule.AckMaxChars)
			sendErr := r.manager.SendEvent(ctx, sessionrt.Event{
				SessionID: target.SessionID,
				From:      sessionrt.SystemActorID,
				Type:      sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:          sessionrt.RoleUser,
					Content:       schedule.Prompt,
					TargetActorID: schedule.AgentID,
				},
			})
			if sendErr != nil {
				r.pipeline.UnmarkHeartbeatPending(target.ConversationID)
				slog.Error("heartbeat_runner: dispatch failed", "agent_id", schedule.AgentID, "conversation_id", target.ConversationID, "session_id", target.SessionID, "error", sendErr)
				continue
			}
			slog.Info("heartbeat_runner: dispatched", "agent_id", schedule.AgentID, "conversation_id", target.ConversationID, "session_id", target.SessionID)
		}

		r.setNextRun(schedule.AgentID, now.Add(schedule.Every))
	}
}

func (r *HeartbeatRunner) loadSession(ctx context.Context, sessionID sessionrt.SessionID, cache map[sessionrt.SessionID]*sessionrt.Session, errs map[sessionrt.SessionID]error) (*sessionrt.Session, error) {
	if session, ok := cache[sessionID]; ok {
		return session, nil
	}
	if err, ok := errs[sessionID]; ok {
		return nil, err
	}
	session, err := r.manager.GetSession(ctx, sessionID)
	if err != nil {
		errs[sessionID] = err
		return nil, err
	}
	if session == nil {
		err := fmt.Errorf("session is nil")
		errs[sessionID] = err
		return nil, err
	}
	cache[sessionID] = session
	return session, nil
}

func heartbeatHasAgentParticipant(session *sessionrt.Session, agentID sessionrt.ActorID) bool {
	if session == nil || strings.TrimSpace(string(agentID)) == "" {
		return false
	}
	participant, ok := session.Participants[agentID]
	if !ok {
		return false
	}
	return participant.Type == sessionrt.ActorAgent
}

func isWithinHeartbeatSleepWindow(now time.Time, location *time.Location) bool {
	if location == nil {
		return false
	}
	hour := now.In(location).Hour()
	if defaultHeartbeatSleepStartHour == defaultHeartbeatSleepEndHour {
		return true
	}
	if defaultHeartbeatSleepStartHour < defaultHeartbeatSleepEndHour {
		return hour >= defaultHeartbeatSleepStartHour && hour < defaultHeartbeatSleepEndHour
	}
	return hour >= defaultHeartbeatSleepStartHour || hour < defaultHeartbeatSleepEndHour
}
