package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/a2a"
	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/config"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type gatewayA2ABackend struct {
	cfg    config.A2AConfig
	client a2a.Client
	now    func() time.Time

	mu      sync.RWMutex
	remotes map[string]*gatewayA2ARemoteState
}

type gatewayA2ARemoteState struct {
	Config        config.A2ARemoteConfig
	Card          *a2a.AgentCard
	Endpoint      string
	LastRefresh   time.Time
	LastError     string
	SkillsSummary string
}

type gatewayA2ARemoteSnapshot struct {
	TargetID    string
	DisplayName string
	Description string
	Endpoint    string
	LastRefresh time.Time
	LastError   string
	Healthy     bool
}

func newGatewayA2ABackend(cfg config.A2AConfig, client a2a.Client) *gatewayA2ABackend {
	if client == nil {
		client = a2a.NewHTTPClient()
	}
	remotes := make(map[string]*gatewayA2ARemoteState, len(cfg.Remotes))
	for _, remote := range cfg.Remotes {
		remoteCopy := remote
		remotes[remote.ID] = &gatewayA2ARemoteState{Config: remoteCopy}
	}
	return &gatewayA2ABackend{
		cfg:     cfg,
		client:  client,
		now:     time.Now,
		remotes: remotes,
	}
}

func (b *gatewayA2ABackend) Start(ctx context.Context) {
	if b == nil || !b.cfg.Enabled {
		return
	}
	go func() {
		b.refreshAll(context.Background())
		ticker := time.NewTicker(b.cfg.CardRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				b.refreshAll(context.Background())
			}
		}
	}()
}

func (b *gatewayA2ABackend) refreshAll(ctx context.Context) {
	b.mu.RLock()
	ids := make([]string, 0, len(b.remotes))
	for id := range b.remotes {
		ids = append(ids, id)
	}
	b.mu.RUnlock()
	sort.Strings(ids)
	for _, id := range ids {
		_, _ = b.refreshRemote(ctx, id)
	}
}

func (b *gatewayA2ABackend) HasTarget(actorID sessionrt.ActorID) bool {
	if b == nil {
		return false
	}
	id := strings.TrimSpace(strings.TrimPrefix(string(actorID), a2aTargetPrefix))
	if id == "" {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	_, ok := b.remotes[id]
	return ok
}

func (b *gatewayA2ABackend) PromptTargets() []agentcore.RemoteDelegationTarget {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]agentcore.RemoteDelegationTarget, 0, len(b.remotes))
	for _, remote := range b.remotes {
		description := strings.TrimSpace(remote.SkillsSummary)
		if description == "" && remote.Card != nil {
			description = strings.TrimSpace(remote.Card.Description)
		}
		out = append(out, agentcore.RemoteDelegationTarget{
			ID:          a2aTargetPrefix + remote.Config.ID,
			Description: description,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (b *gatewayA2ABackend) Snapshots() []gatewayA2ARemoteSnapshot {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]gatewayA2ARemoteSnapshot, 0, len(b.remotes))
	for _, remote := range b.remotes {
		description := strings.TrimSpace(remote.SkillsSummary)
		if description == "" && remote.Card != nil {
			description = strings.TrimSpace(remote.Card.Description)
		}
		out = append(out, gatewayA2ARemoteSnapshot{
			TargetID:    a2aTargetPrefix + remote.Config.ID,
			DisplayName: displayA2ARemoteName(remote.Config),
			Description: description,
			Endpoint:    strings.TrimSpace(remote.Endpoint),
			LastRefresh: remote.LastRefresh,
			LastError:   strings.TrimSpace(remote.LastError),
			Healthy:     remote.Card != nil && strings.TrimSpace(remote.LastError) == "",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TargetID < out[j].TargetID })
	return out
}

func (b *gatewayA2ABackend) SendMessage(ctx context.Context, target sessionrt.ActorID, req a2a.MessageSendRequest) (a2a.Task, *gatewayA2ARemoteState, error) {
	remote, err := b.resolveRemote(ctx, target)
	if err != nil {
		return a2a.Task{}, nil, err
	}
	requestCtx, cancel := withTimeout(ctx, remote.Config.RequestTimeout)
	defer cancel()
	task, err := b.client.SendMessage(requestCtx, remote.Endpoint, buildA2ARemote(remote), req)
	return task, remote, err
}

func (b *gatewayA2ABackend) GetTask(ctx context.Context, remoteID string, taskID string) (a2a.Task, *gatewayA2ARemoteState, error) {
	remote, err := b.resolveRemote(ctx, sessionrt.ActorID(a2aTargetPrefix+remoteID))
	if err != nil {
		return a2a.Task{}, nil, err
	}
	requestCtx, cancel := withTimeout(ctx, remote.Config.RequestTimeout)
	defer cancel()
	task, err := b.client.GetTask(requestCtx, remote.Endpoint, buildA2ARemote(remote), taskID)
	return task, remote, err
}

func (b *gatewayA2ABackend) SubscribeTask(ctx context.Context, remoteID string, taskID string, emit func(a2a.Task) error) error {
	remote, err := b.resolveRemote(ctx, sessionrt.ActorID(a2aTargetPrefix+remoteID))
	if err != nil {
		return err
	}
	requestCtx, cancel := withTimeout(ctx, b.cfg.StreamIdleTimeout)
	defer cancel()
	return b.client.SubscribeTask(requestCtx, remote.Endpoint, buildA2ARemote(remote), taskID, emit)
}

func (b *gatewayA2ABackend) CancelTask(ctx context.Context, remoteID string, taskID string) error {
	remote, err := b.resolveRemote(ctx, sessionrt.ActorID(a2aTargetPrefix+remoteID))
	if err != nil {
		return err
	}
	requestCtx, cancel := withTimeout(ctx, remote.Config.RequestTimeout)
	defer cancel()
	return b.client.CancelTask(requestCtx, remote.Endpoint, buildA2ARemote(remote), taskID)
}

func (b *gatewayA2ABackend) resolveRemote(ctx context.Context, target sessionrt.ActorID) (*gatewayA2ARemoteState, error) {
	if b == nil || !b.cfg.Enabled {
		return nil, fmt.Errorf("a2a backend is disabled")
	}
	id := strings.TrimSpace(strings.TrimPrefix(string(target), a2aTargetPrefix))
	if id == "" {
		return nil, fmt.Errorf("a2a target id is required")
	}
	remote, err := b.refreshRemote(ctx, id)
	if err != nil {
		return nil, err
	}
	if remote.Card == nil || strings.TrimSpace(remote.Endpoint) == "" {
		return nil, fmt.Errorf("a2a target %q is unavailable", a2aTargetPrefix+id)
	}
	return remote, nil
}

func (b *gatewayA2ABackend) refreshRemote(ctx context.Context, id string) (*gatewayA2ARemoteState, error) {
	b.mu.RLock()
	current, ok := b.remotes[id]
	b.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown a2a target %q", a2aTargetPrefix+id)
	}
	if !current.Config.Enabled {
		return nil, fmt.Errorf("a2a target %q is disabled", a2aTargetPrefix+id)
	}

	requestCtx, cancel := withTimeout(ctx, b.cfg.DiscoveryTimeout)
	defer cancel()
	card, err := b.client.Discover(requestCtx, buildA2ARemote(current))
	now := b.now().UTC()

	b.mu.Lock()
	defer b.mu.Unlock()
	remote := b.remotes[id]
	remote.LastRefresh = now
	if err != nil {
		remote.LastError = err.Error()
		remote.Card = nil
		remote.Endpoint = ""
		return cloneA2ARemoteState(remote), fmt.Errorf("discover a2a target %q: %w", a2aTargetPrefix+id, err)
	}
	if validateErr := card.ValidateHTTPJSON(); validateErr != nil {
		remote.LastError = validateErr.Error()
		remote.Card = nil
		remote.Endpoint = ""
		return cloneA2ARemoteState(remote), fmt.Errorf("discover a2a target %q: %w", a2aTargetPrefix+id, validateErr)
	}
	endpoint, _ := card.ResolveHTTPJSONEndpoint()
	remote.LastError = ""
	remote.Card = &card
	remote.Endpoint = normalizeRemoteEndpoint(endpoint, remote.Config.BaseURL)
	remote.SkillsSummary = summarizeA2ASkills(card)
	return cloneA2ARemoteState(remote), nil
}

func cloneA2ARemoteState(remote *gatewayA2ARemoteState) *gatewayA2ARemoteState {
	if remote == nil {
		return nil
	}
	copyState := *remote
	return &copyState
}

const a2aTargetPrefix = "a2a:"

func isA2ATargetActorID(actorID sessionrt.ActorID) bool {
	return strings.HasPrefix(strings.TrimSpace(string(actorID)), a2aTargetPrefix)
}

func displayA2ARemoteName(remote config.A2ARemoteConfig) string {
	if value := strings.TrimSpace(remote.DisplayName); value != "" {
		return value
	}
	return strings.TrimSpace(remote.ID)
}

func summarizeA2ASkills(card a2a.AgentCard) string {
	if len(card.Skills) == 0 {
		return strings.TrimSpace(card.Description)
	}
	parts := make([]string, 0, len(card.Skills))
	for _, skill := range card.Skills {
		label := strings.TrimSpace(skill.Name)
		if label == "" {
			label = strings.TrimSpace(skill.ID)
		}
		description := strings.TrimSpace(skill.Description)
		if label == "" && description == "" {
			continue
		}
		if label == "" {
			parts = append(parts, description)
			continue
		}
		if description == "" {
			parts = append(parts, label)
			continue
		}
		parts = append(parts, label+": "+description)
	}
	return strings.Join(parts, "; ")
}

func normalizeRemoteEndpoint(cardURL string, baseURL string) string {
	cardURL = strings.TrimSpace(cardURL)
	baseURL = strings.TrimSpace(baseURL)
	if cardURL == "" {
		return strings.TrimRight(baseURL, "/")
	}
	parsed, err := url.Parse(cardURL)
	if err != nil {
		return strings.TrimRight(cardURL, "/")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/.well-known/agent-card.json")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/.well-known/agent.json")
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return strings.TrimRight(parsed.String(), "/")
}

func buildA2ARemote(remote *gatewayA2ARemoteState) a2a.Remote {
	if remote == nil {
		return a2a.Remote{}
	}
	return a2a.Remote{
		BaseURL:                   remote.Config.BaseURL,
		CardURL:                   remote.Config.CardURL,
		Headers:                   remote.Config.Headers,
		AllowInsecureTLS:          remote.Config.AllowInsecureTLS,
		CompatLegacyWellKnownPath: true,
	}
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

func (s *gatewaySessionDelegationToolService) SetA2ABackend(ctx context.Context, backend *gatewayA2ABackend) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.a2a = backend
	s.refreshKnownAgentsLocked()
	s.mu.Unlock()
	if backend == nil {
		return
	}
	backend.Start(ctx)
	s.resumeA2ADelegations(ctx)
	go s.resumeA2ALoop(ctx)
}

func (s *gatewaySessionDelegationToolService) A2ASnapshots() []gatewayA2ARemoteSnapshot {
	if s == nil || s.a2a == nil {
		return nil
	}
	return s.a2a.Snapshots()
}

func (s *gatewaySessionDelegationToolService) ReplyDelegationSession(ctx context.Context, req agentcore.DelegationReplyRequest) (agentcore.DelegationReplyResult, error) {
	if s == nil || s.manager == nil || s.a2a == nil {
		return agentcore.DelegationReplyResult{}, fmt.Errorf("delegation reply is unavailable")
	}
	delegationID := strings.TrimSpace(req.DelegationID)
	if delegationID == "" {
		return agentcore.DelegationReplyResult{}, fmt.Errorf("delegation_id is required")
	}
	message := strings.TrimSpace(req.Message)
	if message == "" {
		return agentcore.DelegationReplyResult{}, fmt.Errorf("message is required")
	}
	record, ok := s.readDelegationRecords()[delegationID]
	if !ok {
		return agentcore.DelegationReplyResult{}, fmt.Errorf("unknown delegation session %q", delegationID)
	}
	if !s.isA2ADelegationRecord(record) {
		return agentcore.DelegationReplyResult{}, fmt.Errorf("delegation %q does not support reply", delegationID)
	}
	sourceSessionID := strings.TrimSpace(req.SourceSessionID)
	if sourceSessionID != "" && stringFromMap(record, "source_session_id") != sourceSessionID {
		return agentcore.DelegationReplyResult{}, fmt.Errorf("delegation session %q does not belong to source session %q", delegationID, sourceSessionID)
	}
	if err := s.sendSessionEvent(ctx, sessionrt.Event{
		SessionID: sessionrt.SessionID(delegationID),
		From:      sessionrt.ActorID(stringFromMap(record, "source_agent_id")),
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: message,
		},
	}); err != nil {
		return agentcore.DelegationReplyResult{}, fmt.Errorf("append delegation reply: %w", err)
	}
	task, remote, err := s.a2a.SendMessage(ctx, sessionrt.ActorID(stringFromMap(record, "target_agent_id")), a2a.MessageSendRequest{
		TaskID:    stringFromMap(record, "task_id"),
		ContextID: stringFromMap(record, "context_id"),
		Message: a2a.Message{
			Role: "user",
			Parts: []a2a.Part{{
				Text: message,
			}},
		},
	})
	if err != nil {
		return agentcore.DelegationReplyResult{}, err
	}
	s.appendDelegationRecord(s.buildA2ARecord(record, map[string]any{
		"ts":                 time.Now().UTC().Format(time.RFC3339Nano),
		"event":              "a2a.reply",
		"status":             "active",
		"updated_at":         time.Now().UTC().Format(time.RFC3339Nano),
		"task_id":            task.NormalizedID(),
		"context_id":         task.ContextID,
		"waiting_for_input":  false,
		"last_input_request": "",
		"raw_card_version":   remote.Card.Version,
	}))
	s.handleA2ATaskUpdate(context.Background(), delegationID, record, remote, task)
	return agentcore.DelegationReplyResult{
		SessionID:       delegationID,
		SourceSessionID: stringFromMap(record, "source_session_id"),
		Status:          "active",
		Accepted:        true,
		WaitingForInput: false,
	}, nil
}

func (s *gatewaySessionDelegationToolService) createA2ADelegationSession(ctx context.Context, sourceSessionID sessionrt.SessionID, sourceAgentID, targetAgentID sessionrt.ActorID, message string, title string) (agentcore.DelegationSession, error) {
	if s.a2a == nil {
		return agentcore.DelegationSession{}, fmt.Errorf("a2a delegation is unavailable")
	}
	remote, err := s.a2a.resolveRemote(ctx, targetAgentID)
	if err != nil {
		return agentcore.DelegationSession{}, err
	}
	createOpts := sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{{
			ID:   targetAgentID,
			Type: sessionrt.ActorAgent,
			Metadata: map[string]string{
				"name": displayA2ARemoteName(remote.Config),
			},
		}},
	}
	if title != "" {
		createOpts.DisplayName = title
	}
	createdSession, err := s.manager.CreateSession(ctx, createOpts)
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("create a2a delegation session: %w", err)
	}
	kickoff := buildDelegationKickoffMessage(string(targetAgentID), message)
	if err := s.sendSessionEvent(ctx, sessionrt.Event{
		SessionID: createdSession.ID,
		From:      sourceAgentID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: kickoff,
		},
	}); err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("append a2a kickoff: %w", err)
	}

	task, resolvedRemote, err := s.a2a.SendMessage(ctx, targetAgentID, a2a.MessageSendRequest{
		Message: a2a.Message{
			Role: "user",
			Parts: []a2a.Part{{
				Text: message,
			}},
		},
	})
	now := time.Now().UTC()
	record := map[string]any{
		"ts":                 now.Format(time.RFC3339Nano),
		"event":              "created",
		"delegation_id":      string(createdSession.ID),
		"source_session_id":  string(sourceSessionID),
		"source_agent_id":    string(sourceAgentID),
		"target_agent_id":    string(targetAgentID),
		"title":              title,
		"status":             "active",
		"created_at":         now.Format(time.RFC3339Nano),
		"updated_at":         now.Format(time.RFC3339Nano),
		"target_kind":        "a2a",
		"remote_id":          strings.TrimPrefix(string(targetAgentID), a2aTargetPrefix),
		"task_id":            task.NormalizedID(),
		"context_id":         task.ContextID,
		"waiting_for_input":  false,
		"last_input_request": "",
	}
	if resolvedRemote != nil && resolvedRemote.Card != nil {
		record["raw_card_version"] = resolvedRemote.Card.Version
	}
	if err != nil {
		record["status"] = "failed"
		record["event"] = "failed"
		record["reason"] = err.Error()
		s.appendDelegationRecord(record)
		_ = s.sendSessionEvent(ctx, sessionrt.Event{
			SessionID: createdSession.ID,
			From:      sessionrt.SystemActorID,
			Type:      sessionrt.EventControl,
			Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionFailed, Reason: err.Error()},
		})
		return agentcore.DelegationSession{}, err
	}
	s.appendDelegationRecord(record)
	announcement := buildDelegationAnnouncement(string(targetAgentID), string(createdSession.ID))
	s.sendDelegationControlEventAsync(sourceSessionID, "delegation.created", map[string]any{
		"delegation_id": string(createdSession.ID),
		"target_agent":  string(targetAgentID),
		"announcement":  announcement,
		"target_kind":   "a2a",
		"task_id":       task.NormalizedID(),
		"context_id":    task.ContextID,
	})
	s.handleA2ATaskUpdate(context.Background(), string(createdSession.ID), record, resolvedRemote, task)
	return agentcore.DelegationSession{
		SessionID:       string(createdSession.ID),
		ConversationID:  "session:" + string(createdSession.ID),
		SourceSessionID: string(sourceSessionID),
		SourceAgentID:   string(sourceAgentID),
		TargetAgentID:   string(targetAgentID),
		KickoffMessage:  kickoff,
		Status:          "active",
		Announcement:    announcement,
	}, nil
}

func (s *gatewaySessionDelegationToolService) handleA2ATaskUpdate(ctx context.Context, delegationID string, record map[string]any, remote *gatewayA2ARemoteState, task a2a.Task) {
	if s == nil || remote == nil {
		return
	}
	delegationID = strings.TrimSpace(delegationID)
	if delegationID == "" {
		return
	}
	taskID := task.NormalizedID()
	status := string(task.NormalizedStatus())
	now := time.Now().UTC()
	if taskID != "" || task.ContextID != "" || status != "" {
		s.appendDelegationRecord(s.buildA2ARecord(record, map[string]any{
			"ts":                 now.Format(time.RFC3339Nano),
			"event":              "a2a.update",
			"status":             "active",
			"updated_at":         now.Format(time.RFC3339Nano),
			"task_id":            taskID,
			"context_id":         task.ContextID,
			"last_event_at":      now.Format(time.RFC3339Nano),
			"raw_card_version":   remote.Card.Version,
			"artifacts_metadata": summarizeA2AArtifacts(task.Artifacts),
		}))
	}
	if status != "" {
		_ = s.sendSessionEvent(ctx, sessionrt.Event{
			SessionID: sessionrt.SessionID(delegationID),
			From:      sessionrt.SystemActorID,
			Type:      sessionrt.EventControl,
			Payload: sessionrt.ControlPayload{
				Action: "a2a.task.updated",
				Metadata: map[string]any{
					"status":     status,
					"task_id":    taskID,
					"context_id": task.ContextID,
				},
			},
		})
	}
	if text := strings.TrimSpace(task.LatestText()); text != "" {
		_ = s.sendSessionEvent(ctx, sessionrt.Event{
			SessionID: sessionrt.SessionID(delegationID),
			From:      sessionrt.ActorID(a2aTargetPrefix + remote.Config.ID),
			Type:      sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleAgent,
				Content: text,
			},
		})
	}
	switch task.NormalizedStatus() {
	case a2a.TaskStateInputRequired:
		prompt := strings.TrimSpace(task.LatestText())
		record := s.buildA2ARecord(record, map[string]any{
			"ts":                 now.Format(time.RFC3339Nano),
			"event":              "a2a.input_required",
			"status":             "active",
			"updated_at":         now.Format(time.RFC3339Nano),
			"waiting_for_input":  true,
			"last_input_request": prompt,
		})
		s.appendDelegationRecord(record)
		s.stopA2AMonitor(delegationID)
		s.sendDelegationControlEventAsync(sessionrt.SessionID(stringFromMap(record, "source_session_id")), "delegation.input_required", map[string]any{
			"delegation_id": delegationID,
			"target_agent":  stringFromMap(record, "target_agent_id"),
			"message":       prompt,
		})
		sourceAgentID := sessionrt.ActorID(stringFromMap(record, "source_agent_id"))
		if sourceAgentID != "" {
			s.sendSessionEventAsync(sessionrt.Event{
				SessionID: sessionrt.SessionID(stringFromMap(record, "source_session_id")),
				From:      sessionrt.SystemActorID,
				Type:      sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:          sessionrt.RoleAgent,
					Content:       "Remote delegation " + delegationID + " needs input: " + prompt + "\nReply with `delegate` action:\"reply\" if you can answer directly, or ask the user if needed.",
					TargetActorID: sourceAgentID,
				},
			}, "a2a input required")
		}
	case a2a.TaskStateCompleted:
		_ = s.sendSessionEvent(ctx, sessionrt.Event{
			SessionID: sessionrt.SessionID(delegationID),
			From:      sessionrt.SystemActorID,
			Type:      sessionrt.EventControl,
			Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCompleted},
		})
		s.handleDelegationTerminalState(sessionrt.SessionID(delegationID), sessionrt.SessionID(stringFromMap(record, "source_session_id")), sessionrt.ActorID(stringFromMap(record, "source_agent_id")), sessionrt.ActorID(stringFromMap(record, "target_agent_id")), stringFromMap(record, "title"), "completed", task.LatestText(), task.LatestText(), now)
		s.stopA2AMonitor(delegationID)
	case a2a.TaskStateFailed:
		reason := strings.TrimSpace(task.LatestText())
		_ = s.sendSessionEvent(ctx, sessionrt.Event{
			SessionID: sessionrt.SessionID(delegationID),
			From:      sessionrt.SystemActorID,
			Type:      sessionrt.EventControl,
			Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionFailed, Reason: reason},
		})
		s.handleDelegationTerminalState(sessionrt.SessionID(delegationID), sessionrt.SessionID(stringFromMap(record, "source_session_id")), sessionrt.ActorID(stringFromMap(record, "source_agent_id")), sessionrt.ActorID(stringFromMap(record, "target_agent_id")), stringFromMap(record, "title"), "failed", reason, "", now)
		s.stopA2AMonitor(delegationID)
	case a2a.TaskStateCanceled:
		s.stopA2AMonitor(delegationID)
	}
	if !task.Terminal() && task.NormalizedStatus() != a2a.TaskStateInputRequired && taskID != "" {
		s.startA2AMonitor(delegationID)
	}
}

func (s *gatewaySessionDelegationToolService) killA2ADelegationSession(ctx context.Context, delegationID string, record map[string]any) (agentcore.DelegationKillResult, error) {
	warning := ""
	if remoteID := stringFromMap(record, "remote_id"); remoteID != "" && stringFromMap(record, "task_id") != "" && s.a2a != nil {
		if err := s.a2a.CancelTask(ctx, remoteID, stringFromMap(record, "task_id")); err != nil {
			warning = err.Error()
		}
	}
	s.stopA2AMonitor(delegationID)
	if err := s.manager.CancelSession(ctx, sessionrt.SessionID(delegationID)); err != nil && !isExpectedInactiveDelegationError(err) {
		return agentcore.DelegationKillResult{}, fmt.Errorf("cancel a2a delegation session: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	rec := s.buildA2ARecord(record, map[string]any{
		"ts":         now,
		"event":      "cancelled",
		"status":     "cancelled",
		"updated_at": now,
	})
	if warning != "" {
		rec["warning"] = warning
	}
	s.appendDelegationRecord(rec)
	metadata := map[string]any{"delegation_id": delegationID}
	if warning != "" {
		metadata["warning"] = warning
	}
	s.sendDelegationControlEventAsync(sessionrt.SessionID(stringFromMap(record, "source_session_id")), "delegation.cancelled", metadata)
	return agentcore.DelegationKillResult{
		SessionID:       delegationID,
		SourceSessionID: stringFromMap(record, "source_session_id"),
		Status:          "cancelled",
		Killed:          true,
	}, nil
}

func (s *gatewaySessionDelegationToolService) sendSessionEvent(ctx context.Context, event sessionrt.Event) error {
	if s == nil || s.manager == nil {
		return fmt.Errorf("session manager is unavailable")
	}
	sendCtx := ctx
	if sendCtx == nil {
		sendCtx = context.Background()
	}
	return s.manager.SendEvent(sendCtx, event)
}

func (s *gatewaySessionDelegationToolService) buildA2ARecord(base map[string]any, override map[string]any) map[string]any {
	record := map[string]any{}
	for key, value := range base {
		record[key] = value
	}
	for key, value := range override {
		record[key] = value
	}
	return record
}

func (s *gatewaySessionDelegationToolService) isA2ADelegationRecord(record map[string]any) bool {
	return strings.TrimSpace(stringFromMap(record, "target_kind")) == "a2a" || strings.TrimSpace(stringFromMap(record, "remote_id")) != ""
}

func (s *gatewaySessionDelegationToolService) startA2AMonitor(delegationID string) {
	if s == nil || s.a2a == nil {
		return
	}
	delegationID = strings.TrimSpace(delegationID)
	if delegationID == "" {
		return
	}
	s.mu.Lock()
	if _, exists := s.a2aMonitors[delegationID]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.a2aMonitors[delegationID] = cancel
	s.mu.Unlock()

	go func() {
		defer s.stopA2AMonitor(delegationID)
		for {
			record, ok := s.readDelegationRecords()[delegationID]
			if !ok || !s.isA2ADelegationRecord(record) || delegationStatus(record) != "active" || boolFromMap(record, "waiting_for_input") {
				return
			}
			taskID := stringFromMap(record, "task_id")
			remoteID := stringFromMap(record, "remote_id")
			if taskID == "" || remoteID == "" {
				return
			}
			streamErr := s.a2a.SubscribeTask(ctx, remoteID, taskID, func(task a2a.Task) error {
				remote, _ := s.a2a.resolveRemote(context.Background(), sessionrt.ActorID(a2aTargetPrefix+remoteID))
				s.handleA2ATaskUpdate(context.Background(), delegationID, record, remote, task)
				return nil
			})
			if streamErr == nil || ctx.Err() != nil {
				return
			}
			task, remote, err := s.a2a.GetTask(context.Background(), remoteID, taskID)
			if err == nil {
				s.handleA2ATaskUpdate(context.Background(), delegationID, record, remote, task)
				if task.Terminal() || task.NormalizedStatus() == a2a.TaskStateInputRequired {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.a2a.cfg.TaskPollInterval):
			}
		}
	}()
}

func (s *gatewaySessionDelegationToolService) stopA2AMonitor(delegationID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cancel, exists := s.a2aMonitors[delegationID]
	if exists {
		delete(s.a2aMonitors, delegationID)
	}
	s.mu.Unlock()
	if exists {
		cancel()
	}
}

func (s *gatewaySessionDelegationToolService) resumeA2ALoop(ctx context.Context) {
	if s == nil || s.a2a == nil {
		return
	}
	ticker := time.NewTicker(s.a2a.cfg.ResumeScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.resumeA2ADelegations(ctx)
		}
	}
}

func (s *gatewaySessionDelegationToolService) resumeA2ADelegations(_ context.Context) {
	if s == nil || s.a2a == nil {
		return
	}
	for delegationID, record := range s.readDelegationRecords() {
		if !s.isA2ADelegationRecord(record) || delegationStatus(record) != "active" || boolFromMap(record, "waiting_for_input") || stringFromMap(record, "task_id") == "" {
			continue
		}
		s.startA2AMonitor(delegationID)
	}
}

func summarizeA2AArtifacts(artifacts []a2a.Artifact) []map[string]any {
	if len(artifacts) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, map[string]any{
			"id":          strings.TrimSpace(artifact.ArtifactID),
			"name":        strings.TrimSpace(artifact.Name),
			"description": strings.TrimSpace(artifact.Description),
			"text":        strings.TrimSpace(a2a.Task{Artifacts: []a2a.Artifact{artifact}}.LatestText()),
		})
	}
	return out
}

func isExpectedInactiveDelegationError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), sessionrt.ErrSessionNotActive.Error()) || strings.Contains(err.Error(), sessionrt.ErrSessionNotFound.Error()))
}
