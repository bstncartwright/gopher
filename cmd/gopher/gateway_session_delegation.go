package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/pelletier/go-toml/v2"
)

type gatewaySessionDelegationStore interface {
	List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error)
	ListSessions(ctx context.Context) ([]sessionrt.SessionRecord, error)
}

type ephemeralDelegationState struct {
	DelegationID     string
	AgentID          sessionrt.ActorID
	SourceAgentID    sessionrt.ActorID
	SourceWorkspace  string
	WorkerWorkspace  string
	DiffArtifactPath string
	CreatedAt        time.Time
	LastActivity     time.Time
}

type gatewaySessionDelegationToolService struct {
	manager sessionrt.SessionManager
	store   gatewaySessionDelegationStore
	router  *agentcore.ActorExecutorRouter

	mu             sync.RWMutex
	agents         map[sessionrt.ActorID]*agentcore.Agent
	ephemeral      map[string]ephemeralDelegationState
	reservedWorker map[sessionrt.ActorID]struct{}

	dataDir string
	logger  *log.Logger
	ttl     time.Duration
}

const (
	delegationAsyncSendTimeout = 10 * time.Minute
	delegationEphemeralTTL     = 30 * time.Minute
	delegationWatchTimeout     = 24 * time.Hour
)

var validDelegationAliasPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func newGatewaySessionDelegationToolService(
	manager sessionrt.SessionManager,
	store gatewaySessionDelegationStore,
	agents map[sessionrt.ActorID]*agentcore.Agent,
	dataDir string,
	logger *log.Logger,
	router *agentcore.ActorExecutorRouter,
) *gatewaySessionDelegationToolService {
	if agents == nil {
		agents = map[sessionrt.ActorID]*agentcore.Agent{}
	}
	service := &gatewaySessionDelegationToolService{
		manager:        manager,
		store:          store,
		router:         router,
		agents:         agents,
		ephemeral:      map[string]ephemeralDelegationState{},
		reservedWorker: map[sessionrt.ActorID]struct{}{},
		dataDir:        strings.TrimSpace(dataDir),
		logger:         logger,
		ttl:            delegationEphemeralTTL,
	}
	service.refreshKnownAgentsLocked()
	return service
}

func (s *gatewaySessionDelegationToolService) CreateDelegationSession(ctx context.Context, req agentcore.DelegationCreateRequest) (agentcore.DelegationSession, error) {
	if s == nil || s.manager == nil {
		return agentcore.DelegationSession{}, fmt.Errorf("delegation service is unavailable")
	}
	s.cleanupExpiredDelegations(ctx)

	sourceSessionID := sessionrt.SessionID(strings.TrimSpace(req.SourceSessionID))
	sourceAgentID := sessionrt.ActorID(strings.TrimSpace(req.SourceAgentID))
	requestedTargetAgentID := sessionrt.ActorID(strings.TrimSpace(req.TargetAgentID))
	requestedModelPolicy := strings.TrimSpace(req.ModelPolicy)
	message := strings.TrimSpace(req.Message)
	if sourceSessionID == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("source session id is required")
	}
	if sourceAgentID == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("source agent id is required")
	}
	if message == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("message is required")
	}

	sourceAgent, exists := s.lookupAgent(sourceAgentID)
	if !exists {
		return agentcore.DelegationSession{}, fmt.Errorf("unknown source agent %q", sourceAgentID)
	}

	sourceSession, err := s.manager.GetSession(ctx, sourceSessionID)
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("load source session: %w", err)
	}
	if sourceSession == nil {
		return agentcore.DelegationSession{}, fmt.Errorf("source session %q not found", sourceSessionID)
	}

	resolvedTargetAgentID, displayTargetAgentID, createdEphemeral, err := s.resolveDelegationTarget(ctx, sourceAgentID, requestedTargetAgentID, sourceAgent, requestedModelPolicy)
	if err != nil {
		return agentcore.DelegationSession{}, err
	}
	if resolvedTargetAgentID == sourceAgentID {
		if createdEphemeral != nil {
			s.teardownEphemeralWorker(*createdEphemeral)
		}
		return agentcore.DelegationSession{}, fmt.Errorf("source and target agents must be different")
	}

	participants := []sessionrt.Participant{{ID: sourceAgentID, Type: sessionrt.ActorAgent}}
	if resolvedTargetAgentID != sourceAgentID {
		participants = append(participants, sessionrt.Participant{ID: resolvedTargetAgentID, Type: sessionrt.ActorAgent})
	}
	createOpts := sessionrt.CreateSessionOptions{Participants: participants}
	if title := strings.TrimSpace(req.Title); title != "" {
		createOpts.DisplayName = title
	}
	createdSession, err := s.manager.CreateSession(ctx, createOpts)
	if err != nil {
		if createdEphemeral != nil {
			s.teardownEphemeralWorker(*createdEphemeral)
		}
		return agentcore.DelegationSession{}, fmt.Errorf("create delegation session: %w", err)
	}

	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	diffArtifactPath := ""
	if createdEphemeral != nil {
		createdEphemeral.DelegationID = strings.TrimSpace(string(createdSession.ID))
		createdEphemeral.CreatedAt = nowTime
		createdEphemeral.LastActivity = nowTime
		s.mu.Lock()
		s.ephemeral[createdEphemeral.DelegationID] = *createdEphemeral
		s.mu.Unlock()
	}

	kickoff := buildDelegationKickoffMessage(displayTargetAgentID, message)
	s.sendSessionEventAsync(
		sessionrt.Event{
			SessionID: createdSession.ID,
			From:      sourceAgentID,
			Type:      sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:          sessionrt.RoleAgent,
				Content:       kickoff,
				TargetActorID: resolvedTargetAgentID,
			},
		},
		"delegation kickoff event",
	)

	announcement := buildDelegationAnnouncement(displayTargetAgentID, string(createdSession.ID))
	record := map[string]any{
		"ts":                       now,
		"event":                    "created",
		"delegation_id":            strings.TrimSpace(string(createdSession.ID)),
		"source_session_id":        strings.TrimSpace(string(sourceSessionID)),
		"source_agent_id":          strings.TrimSpace(string(sourceAgentID)),
		"target_agent_id":          strings.TrimSpace(displayTargetAgentID),
		"resolved_target_agent_id": strings.TrimSpace(string(resolvedTargetAgentID)),
		"title":                    strings.TrimSpace(req.Title),
		"kickoff_message":          kickoff,
		"status":                   "active",
		"created_at":               now,
		"updated_at":               now,
		"ephemeral":                createdEphemeral != nil,
		"workspace_mode":           "isolated_temp",
		"merge_mode":               "diff_for_approval",
		"diff_artifact_path":       diffArtifactPath,
		"model_policy":             requestedModelPolicy,
	}
	s.appendDelegationRecord(record)
	s.startDelegationLifecycleMonitor(
		createdSession.ID,
		sourceSessionID,
		sourceAgentID,
		sessionrt.ActorID(strings.TrimSpace(displayTargetAgentID)),
		strings.TrimSpace(req.Title),
	)
	s.sendDelegationControlEventAsync(sourceSessionID, "delegation.created", map[string]any{
		"delegation_id":         string(createdSession.ID),
		"target_agent":          displayTargetAgentID,
		"resolved_target_agent": string(resolvedTargetAgentID),
		"announcement":          announcement,
		"ephemeral":             createdEphemeral != nil,
		"workspace_mode":        "isolated_temp",
		"merge_mode":            "diff_for_approval",
		"diff_artifact_path":    diffArtifactPath,
	})
	if s.logger != nil {
		s.logger.Printf(
			"delegation session created source=%s target=%s resolved_target=%s source_session=%s delegated_session=%s ephemeral=%t",
			sourceAgentID,
			displayTargetAgentID,
			resolvedTargetAgentID,
			sourceSessionID,
			createdSession.ID,
			createdEphemeral != nil,
		)
	}

	return agentcore.DelegationSession{
		SessionID:       strings.TrimSpace(string(createdSession.ID)),
		ConversationID:  "session:" + strings.TrimSpace(string(createdSession.ID)),
		SourceSessionID: strings.TrimSpace(string(sourceSessionID)),
		SourceAgentID:   strings.TrimSpace(string(sourceAgentID)),
		TargetAgentID:   strings.TrimSpace(displayTargetAgentID),
		KickoffMessage:  kickoff,
		Status:          "active",
		Announcement:    announcement,
		Ephemeral:       createdEphemeral != nil,
		WorkspaceMode:   "isolated_temp",
		MergeMode:       "diff_for_approval",
		DiffArtifact:    diffArtifactPath,
	}, nil
}

func (s *gatewaySessionDelegationToolService) startDelegationLifecycleMonitor(
	delegationSessionID sessionrt.SessionID,
	sourceSessionID sessionrt.SessionID,
	sourceAgentID sessionrt.ActorID,
	targetAgentID sessionrt.ActorID,
	title string,
) {
	if s == nil || s.manager == nil {
		return
	}
	delegationSessionID = sessionrt.SessionID(strings.TrimSpace(string(delegationSessionID)))
	sourceSessionID = sessionrt.SessionID(strings.TrimSpace(string(sourceSessionID)))
	sourceAgentID = sessionrt.ActorID(strings.TrimSpace(string(sourceAgentID)))
	targetAgentID = sessionrt.ActorID(strings.TrimSpace(string(targetAgentID)))
	if delegationSessionID == "" || sourceSessionID == "" {
		return
	}

	watchCtx, cancel := context.WithTimeout(context.Background(), delegationWatchTimeout)
	stream, err := s.manager.Subscribe(watchCtx, delegationSessionID)
	if err != nil {
		cancel()
		if s.logger != nil {
			s.logger.Printf("delegation monitor subscribe failed delegation=%s err=%v", delegationSessionID, err)
		}
		return
	}

	go func() {
		defer cancel()
		for event := range stream {
			status, reason, ok := delegationTerminalStatusFromEvent(event)
			if !ok {
				continue
			}
			s.handleDelegationTerminalState(
				delegationSessionID,
				sourceSessionID,
				sourceAgentID,
				targetAgentID,
				title,
				status,
				reason,
				event.Timestamp,
			)
			return
		}
	}()
}

func (s *gatewaySessionDelegationToolService) handleDelegationTerminalState(
	delegationSessionID sessionrt.SessionID,
	sourceSessionID sessionrt.SessionID,
	sourceAgentID sessionrt.ActorID,
	targetAgentID sessionrt.ActorID,
	title string,
	status string,
	reason string,
	at time.Time,
) {
	if s == nil {
		return
	}
	status = strings.TrimSpace(strings.ToLower(status))
	if status != "completed" && status != "failed" {
		return
	}

	delegationID := strings.TrimSpace(string(delegationSessionID))
	if delegationID == "" {
		return
	}
	nowTime := at.UTC()
	if nowTime.IsZero() {
		nowTime = time.Now().UTC()
	}
	now := nowTime.Format(time.RFC3339Nano)

	record := s.readDelegationRecords()[delegationID]
	current := delegationStatus(record)
	if current != "" && current != "active" {
		if current == status {
			s.teardownEphemeralForDelegation(delegationID)
		}
		return
	}

	diffArtifactPath, diffErr := s.finalizeDiffForDelegation(context.Background(), delegationID, record)
	sourceAgent := stringFromMap(record, "source_agent_id")
	if sourceAgent == "" {
		sourceAgent = strings.TrimSpace(string(sourceAgentID))
	}
	targetAgent := stringFromMap(record, "target_agent_id")
	if targetAgent == "" {
		targetAgent = strings.TrimSpace(string(targetAgentID))
	}
	recordTitle := strings.TrimSpace(stringFromMap(record, "title"))
	if recordTitle == "" {
		recordTitle = strings.TrimSpace(title)
	}
	createdAt := stringFromMap(record, "created_at")
	if createdAt == "" {
		createdAt = now
	}
	rec := map[string]any{
		"ts":                       now,
		"event":                    status,
		"delegation_id":            delegationID,
		"source_session_id":        strings.TrimSpace(string(sourceSessionID)),
		"source_agent_id":          sourceAgent,
		"target_agent_id":          targetAgent,
		"resolved_target_agent_id": strings.TrimSpace(stringFromMap(record, "resolved_target_agent_id")),
		"title":                    recordTitle,
		"status":                   status,
		"created_at":               createdAt,
		"updated_at":               now,
		"ephemeral":                boolFromMap(record, "ephemeral"),
		"workspace_mode":           stringFromMap(record, "workspace_mode"),
		"merge_mode":               stringFromMap(record, "merge_mode"),
		"diff_artifact_path":       diffArtifactPath,
	}
	if strings.TrimSpace(reason) != "" {
		rec["reason"] = strings.TrimSpace(reason)
	}
	if diffErr != nil {
		rec["diff_error"] = diffErr.Error()
		if s.logger != nil {
			s.logger.Printf("delegation terminal diff finalization failed delegation=%s err=%v", delegationID, diffErr)
		}
	}
	s.appendDelegationRecord(rec)

	action := "delegation." + status
	announcement := buildDelegationTerminalAnnouncement(targetAgent, delegationID, status)
	metadata := map[string]any{
		"delegation_id":      delegationID,
		"status":             status,
		"target_agent":       targetAgent,
		"announcement":       announcement,
		"diff_artifact_path": diffArtifactPath,
	}
	if strings.TrimSpace(reason) != "" {
		metadata["reason"] = strings.TrimSpace(reason)
	}
	s.sendDelegationControlEventAsync(sourceSessionID, action, metadata)
	sourceAgentID = sessionrt.ActorID(strings.TrimSpace(string(sourceAgentID)))
	if sourceAgentID != "" {
		s.sendSessionEventAsync(sessionrt.Event{
			SessionID: sourceSessionID,
			From:      sessionrt.SystemActorID,
			Type:      sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:          sessionrt.RoleAgent,
				Content:       buildDelegationTerminalMessage(targetAgent, delegationID, status, reason, diffArtifactPath),
				TargetActorID: sourceAgentID,
			},
		}, "delegation terminal announcement")
	}
	s.touchDelegationActivity(delegationID, nowTime)
	s.teardownEphemeralForDelegation(delegationID)
}

func (s *gatewaySessionDelegationToolService) resolveDelegationTarget(ctx context.Context, sourceAgentID, requestedTargetAgentID sessionrt.ActorID, sourceAgent *agentcore.Agent, requestedModelPolicy string) (sessionrt.ActorID, string, *ephemeralDelegationState, error) {
	if s == nil {
		return "", "", nil, fmt.Errorf("delegation service is unavailable")
	}
	sourceAgentID = sessionrt.ActorID(strings.TrimSpace(string(sourceAgentID)))
	requestedTargetAgentID = sessionrt.ActorID(strings.TrimSpace(string(requestedTargetAgentID)))
	if sourceAgentID == "" {
		return "", "", nil, fmt.Errorf("source agent id is required")
	}

	if requestedTargetAgentID != "" {
		if _, exists := s.lookupAgent(requestedTargetAgentID); exists {
			return requestedTargetAgentID, strings.TrimSpace(string(requestedTargetAgentID)), nil, nil
		}
	}

	targetAlias := requestedTargetAgentID
	if targetAlias == "" {
		targetAlias = s.reserveNextSubagentAlias()
	} else {
		if err := validateDelegationAlias(string(targetAlias)); err != nil {
			return "", "", nil, err
		}
		if !s.reserveAlias(targetAlias) {
			if _, exists := s.lookupAgent(targetAlias); exists {
				return targetAlias, strings.TrimSpace(string(targetAlias)), nil, nil
			}
			return "", "", nil, fmt.Errorf("target agent %q is currently reserved", strings.TrimSpace(string(targetAlias)))
		}
	}
	defer s.releaseAlias(targetAlias)

	ephemeral, err := s.spawnEphemeralWorker(ctx, sourceAgentID, targetAlias, sourceAgent, requestedModelPolicy)
	if err != nil {
		return "", "", nil, err
	}
	return ephemeral.AgentID, strings.TrimSpace(string(ephemeral.AgentID)), ephemeral, nil
}

func (s *gatewaySessionDelegationToolService) ListDelegationSessions(ctx context.Context, req agentcore.DelegationListRequest) ([]agentcore.DelegationListItem, error) {
	if s == nil {
		return nil, fmt.Errorf("delegation service is unavailable")
	}
	s.cleanupExpiredDelegations(ctx)

	records := s.readDelegationRecords()
	if len(records) == 0 {
		return []agentcore.DelegationListItem{}, nil
	}

	reqSourceSessionID := strings.TrimSpace(req.SourceSessionID)
	sessionRecords := s.readSessionRecordMap(ctx)
	now := time.Now()
	items := make([]agentcore.DelegationListItem, 0, len(records))
	for delegationID, record := range records {
		sourceSessionID := stringFromMap(record, "source_session_id")
		if reqSourceSessionID != "" && sourceSessionID != reqSourceSessionID {
			continue
		}
		status := delegationStatus(record)
		sessionRecord, hasSession := sessionRecords[sessionrt.SessionID(delegationID)]
		if hasSession {
			status = delegationStatusFromSessionRecord(status, sessionRecord)
			if status == "active" && delegationSessionRecordIsStale(sessionRecord, now) {
				status = "stale"
			}
		}
		if !req.IncludeInactive && status != "active" {
			continue
		}

		createdAt := stringFromMap(record, "created_at")
		updatedAt := stringFromMap(record, "updated_at")
		if createdAt == "" {
			createdAt = stringFromMap(record, "ts")
		}
		if updatedAt == "" {
			updatedAt = stringFromMap(record, "ts")
		}
		lastSeq := uint64FromMap(record, "last_seq")
		if hasSession {
			if !sessionRecord.UpdatedAt.IsZero() {
				updatedAt = sessionRecord.UpdatedAt.UTC().Format(time.RFC3339Nano)
			}
			if sessionRecord.LastSeq > 0 {
				lastSeq = sessionRecord.LastSeq
			}
		}

		item := agentcore.DelegationListItem{
			SessionID:       delegationID,
			ConversationID:  "session:" + delegationID,
			SourceSessionID: sourceSessionID,
			SourceAgentID:   stringFromMap(record, "source_agent_id"),
			TargetAgentID:   stringFromMap(record, "target_agent_id"),
			Title:           stringFromMap(record, "title"),
			Status:          status,
			CreatedAt:       createdAt,
			UpdatedAt:       updatedAt,
			LastSeq:         lastSeq,
			Ephemeral:       boolFromMap(record, "ephemeral"),
			DiffArtifact:    stringFromMap(record, "diff_artifact_path"),
		}
		items = append(items, item)
		s.touchDelegationActivity(delegationID, now)
	}

	sort.Slice(items, func(i, j int) bool {
		left := parseRFC3339(items[i].UpdatedAt)
		right := parseRFC3339(items[j].UpdatedAt)
		if left.Equal(right) {
			return items[i].SessionID < items[j].SessionID
		}
		return left.After(right)
	})
	return items, nil
}

func (s *gatewaySessionDelegationToolService) KillDelegationSession(ctx context.Context, req agentcore.DelegationKillRequest) (agentcore.DelegationKillResult, error) {
	if s == nil || s.manager == nil {
		return agentcore.DelegationKillResult{}, fmt.Errorf("delegation service is unavailable")
	}
	s.cleanupExpiredDelegations(ctx)

	delegationID := strings.TrimSpace(req.DelegationID)
	if delegationID == "" {
		return agentcore.DelegationKillResult{}, fmt.Errorf("delegation_id is required")
	}
	record, ok := s.readDelegationRecords()[delegationID]
	if !ok {
		return agentcore.DelegationKillResult{}, fmt.Errorf("unknown delegation session %q", delegationID)
	}
	sourceSessionID := stringFromMap(record, "source_session_id")
	reqSourceSessionID := strings.TrimSpace(req.SourceSessionID)
	if reqSourceSessionID != "" && sourceSessionID != reqSourceSessionID {
		return agentcore.DelegationKillResult{}, fmt.Errorf("delegation session %q does not belong to source session %q", delegationID, reqSourceSessionID)
	}

	status := delegationStatus(record)
	if status != "active" {
		return agentcore.DelegationKillResult{
			SessionID:       delegationID,
			SourceSessionID: sourceSessionID,
			Status:          status,
			Killed:          false,
		}, nil
	}

	if err := s.manager.CancelSession(ctx, sessionrt.SessionID(delegationID)); err != nil {
		if errors.Is(err, sessionrt.ErrSessionNotActive) || errors.Is(err, sessionrt.ErrSessionNotFound) {
			return agentcore.DelegationKillResult{
				SessionID:       delegationID,
				SourceSessionID: sourceSessionID,
				Status:          "inactive",
				Killed:          false,
			}, nil
		}
		return agentcore.DelegationKillResult{}, fmt.Errorf("cancel delegation session: %w", err)
	}

	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	diffArtifactPath, diffErr := s.finalizeDiffForDelegation(ctx, delegationID, record)
	recordStatus := "cancelled"
	if diffErr != nil && diffArtifactPath == "" {
		recordStatus = "cancelled"
	}
	rec := map[string]any{
		"ts":                 now,
		"event":              "cancelled",
		"delegation_id":      delegationID,
		"source_session_id":  sourceSessionID,
		"source_agent_id":    stringFromMap(record, "source_agent_id"),
		"target_agent_id":    stringFromMap(record, "target_agent_id"),
		"title":              stringFromMap(record, "title"),
		"status":             recordStatus,
		"created_at":         stringFromMap(record, "created_at"),
		"updated_at":         now,
		"ephemeral":          boolFromMap(record, "ephemeral"),
		"workspace_mode":     stringFromMap(record, "workspace_mode"),
		"merge_mode":         stringFromMap(record, "merge_mode"),
		"diff_artifact_path": diffArtifactPath,
	}
	if diffErr != nil {
		rec["diff_error"] = diffErr.Error()
	}
	s.appendDelegationRecord(rec)
	s.sendDelegationControlEventAsync(sessionrt.SessionID(sourceSessionID), "delegation.cancelled", map[string]any{
		"delegation_id":      delegationID,
		"diff_artifact_path": diffArtifactPath,
	})
	s.touchDelegationActivity(delegationID, nowTime)

	return agentcore.DelegationKillResult{
		SessionID:       delegationID,
		SourceSessionID: sourceSessionID,
		Status:          "cancelled",
		Killed:          true,
	}, nil
}

func (s *gatewaySessionDelegationToolService) GetDelegationLog(ctx context.Context, req agentcore.DelegationLogRequest) (agentcore.DelegationLogResult, error) {
	if s == nil || s.store == nil {
		return agentcore.DelegationLogResult{}, fmt.Errorf("delegation log service is unavailable")
	}
	s.cleanupExpiredDelegations(ctx)

	delegationID := strings.TrimSpace(req.DelegationID)
	if delegationID == "" {
		return agentcore.DelegationLogResult{}, fmt.Errorf("delegation_id is required")
	}
	record, ok := s.readDelegationRecords()[delegationID]
	if !ok {
		return agentcore.DelegationLogResult{}, fmt.Errorf("unknown delegation session %q", delegationID)
	}
	reqSourceSessionID := strings.TrimSpace(req.SourceSessionID)
	sourceSessionID := stringFromMap(record, "source_session_id")
	if reqSourceSessionID != "" && sourceSessionID != reqSourceSessionID {
		return agentcore.DelegationLogResult{}, fmt.Errorf("delegation session %q does not belong to source session %q", delegationID, reqSourceSessionID)
	}

	events, err := s.store.List(ctx, sessionrt.SessionID(delegationID))
	if err != nil {
		return agentcore.DelegationLogResult{}, fmt.Errorf("load delegation log: %w", err)
	}
	total := len(events)
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	end := offset + limit
	if end > total {
		end = total
	}

	entries := make([]agentcore.DelegationLogEntry, 0, end-offset)
	for _, event := range events[offset:end] {
		entries = append(entries, delegationLogEntryFromEvent(event))
	}
	s.touchDelegationActivity(delegationID, time.Now().UTC())
	return agentcore.DelegationLogResult{
		SessionID: delegationID,
		Total:     total,
		Offset:    offset,
		Count:     len(entries),
		Entries:   entries,
	}, nil
}

func (s *gatewaySessionDelegationToolService) readSessionRecordMap(ctx context.Context) map[sessionrt.SessionID]sessionrt.SessionRecord {
	out := map[sessionrt.SessionID]sessionrt.SessionRecord{}
	if s == nil || s.store == nil {
		return out
	}
	records, err := s.store.ListSessions(ctx)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("delegation list sessions error: %v", err)
		}
		return out
	}
	for _, record := range records {
		out[record.SessionID] = record
	}
	return out
}

func (s *gatewaySessionDelegationToolService) readDelegationRecords() map[string]map[string]any {
	out := map[string]map[string]any{}
	if s == nil {
		return out
	}
	dataDir := filepath.Clean(strings.TrimSpace(s.dataDir))
	if dataDir == "" {
		return out
	}
	path := filepath.Join(dataDir, "control", "delegations.jsonl")
	blob, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		delegationID := stringFromMap(record, "delegation_id")
		if delegationID == "" {
			continue
		}
		out[delegationID] = record
	}
	return out
}

func (s *gatewaySessionDelegationToolService) sendDelegationControlEventAsync(sourceSessionID sessionrt.SessionID, action string, metadata map[string]any) {
	if s == nil || s.manager == nil {
		return
	}
	if strings.TrimSpace(string(sourceSessionID)) == "" || strings.TrimSpace(action) == "" {
		return
	}
	event := sessionrt.Event{
		SessionID: sourceSessionID,
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventControl,
		Payload: sessionrt.ControlPayload{
			Action:   strings.TrimSpace(action),
			Metadata: metadata,
		},
	}
	s.sendSessionEventAsync(event, "delegation control event")
}

func (s *gatewaySessionDelegationToolService) sendSessionEventAsync(event sessionrt.Event, label string) {
	if s == nil || s.manager == nil {
		return
	}
	go func() {
		sendCtx, cancel := context.WithTimeout(context.Background(), delegationAsyncSendTimeout)
		defer cancel()
		if err := s.manager.SendEvent(sendCtx, event); err != nil && s.logger != nil {
			s.logger.Printf("%s send failed session=%s err=%v", strings.TrimSpace(label), strings.TrimSpace(string(event.SessionID)), err)
		}
	}()
}

func (s *gatewaySessionDelegationToolService) appendDelegationRecord(record map[string]any) {
	if s == nil {
		return
	}
	dataDir := filepath.Clean(strings.TrimSpace(s.dataDir))
	if dataDir == "" {
		return
	}
	path := filepath.Join(dataDir, "control", "delegations.jsonl")
	blob, err := json.Marshal(record)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(blob, '\n'))
}

func (s *gatewaySessionDelegationToolService) cleanupExpiredDelegations(ctx context.Context) {
	if s == nil || s.ttl <= 0 {
		return
	}
	now := time.Now().UTC()
	expired := make([]ephemeralDelegationState, 0, 4)

	s.mu.RLock()
	for _, state := range s.ephemeral {
		if state.LastActivity.IsZero() {
			continue
		}
		if now.Sub(state.LastActivity) >= s.ttl {
			expired = append(expired, state)
		}
	}
	s.mu.RUnlock()

	if len(expired) == 0 {
		return
	}
	for _, state := range expired {
		s.expireDelegation(ctx, state, now)
	}
}

func (s *gatewaySessionDelegationToolService) expireDelegation(ctx context.Context, state ephemeralDelegationState, now time.Time) {
	if strings.TrimSpace(state.DelegationID) == "" {
		return
	}
	records := s.readDelegationRecords()
	record := records[state.DelegationID]
	diffArtifactPath, diffErr := s.finalizeDiffForDelegation(ctx, state.DelegationID, record)

	status := delegationStatus(record)
	if status == "" {
		status = "active"
	}
	if status == "active" {
		if s.manager != nil {
			_ = s.manager.CancelSession(ctx, sessionrt.SessionID(state.DelegationID))
		}
		s.appendDelegationRecord(map[string]any{
			"ts":                 now.Format(time.RFC3339Nano),
			"event":              "expired",
			"delegation_id":      state.DelegationID,
			"source_session_id":  stringFromMap(record, "source_session_id"),
			"source_agent_id":    stringFromMap(record, "source_agent_id"),
			"target_agent_id":    stringFromMap(record, "target_agent_id"),
			"title":              stringFromMap(record, "title"),
			"status":             "expired",
			"created_at":         stringFromMap(record, "created_at"),
			"updated_at":         now.Format(time.RFC3339Nano),
			"ephemeral":          true,
			"workspace_mode":     "isolated_temp",
			"merge_mode":         "diff_for_approval",
			"diff_artifact_path": diffArtifactPath,
		})
	}
	if diffErr != nil && s.logger != nil {
		s.logger.Printf("delegation diff finalization failed delegation=%s err=%v", state.DelegationID, diffErr)
	}

	s.teardownEphemeralWorker(state)

	s.mu.Lock()
	delete(s.ephemeral, state.DelegationID)
	s.mu.Unlock()
}

func (s *gatewaySessionDelegationToolService) finalizeDiffForDelegation(ctx context.Context, delegationID string, record map[string]any) (string, error) {
	_ = ctx
	delegationID = strings.TrimSpace(delegationID)
	if delegationID == "" {
		return "", nil
	}
	if existing := stringFromMap(record, "diff_artifact_path"); existing != "" {
		return existing, nil
	}

	s.mu.RLock()
	state, exists := s.ephemeral[delegationID]
	s.mu.RUnlock()
	if !exists {
		return "", nil
	}
	if strings.TrimSpace(state.DiffArtifactPath) != "" {
		return strings.TrimSpace(state.DiffArtifactPath), nil
	}
	if strings.TrimSpace(state.SourceWorkspace) == "" || strings.TrimSpace(state.WorkerWorkspace) == "" {
		return "", nil
	}

	blob, err := diffDirectories(state.SourceWorkspace, state.WorkerWorkspace)
	if err != nil {
		return "", err
	}
	if len(blob) == 0 {
		blob = []byte("No differences.\n")
	}

	baseDir := filepath.Clean(strings.TrimSpace(s.dataDir))
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	path := filepath.Join(baseDir, "control", "delegation_diffs", delegationID+".patch")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create delegation diff dir: %w", err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		return "", fmt.Errorf("write delegation diff: %w", err)
	}

	s.mu.Lock()
	if latest, ok := s.ephemeral[delegationID]; ok {
		latest.DiffArtifactPath = path
		s.ephemeral[delegationID] = latest
	}
	s.mu.Unlock()
	return path, nil
}

func (s *gatewaySessionDelegationToolService) spawnEphemeralWorker(_ context.Context, sourceAgentID, targetAlias sessionrt.ActorID, sourceAgent *agentcore.Agent, requestedModelPolicy string) (*ephemeralDelegationState, error) {
	if s.router == nil {
		return nil, fmt.Errorf("delegation runtime router is unavailable for dynamic subagents")
	}
	if sourceAgent == nil {
		return nil, fmt.Errorf("source agent %q is required", sourceAgentID)
	}
	sourceWorkspace := strings.TrimSpace(sourceAgent.Workspace)
	if sourceWorkspace == "" {
		return nil, fmt.Errorf("source agent %q has no workspace", sourceAgentID)
	}
	if err := validateDelegationAlias(string(targetAlias)); err != nil {
		return nil, err
	}

	workspace, err := createDelegationWorkspaceSnapshot(sourceWorkspace, strings.TrimSpace(string(targetAlias)), strings.TrimSpace(s.dataDir))
	if err != nil {
		return nil, err
	}

	cfg := sourceAgent.Config
	cfg.AgentID = strings.TrimSpace(string(targetAlias))
	cfg.ModelPolicy = defaultAgentModelPolicy
	if strings.TrimSpace(requestedModelPolicy) != "" {
		cfg.ModelPolicy = strings.TrimSpace(requestedModelPolicy)
	}
	if strings.TrimSpace(cfg.Name) == "" || strings.TrimSpace(cfg.Name) == strings.TrimSpace(sourceAgent.Name) {
		cfg.Name = strings.TrimSpace(string(targetAlias))
	}
	policies := sourceAgent.Policies
	cfg.Policies = &policies

	if err := writeAgentConfig(filepath.Join(workspace, "config.toml"), cfg); err != nil {
		_ = os.RemoveAll(workspace)
		return nil, err
	}

	worker, err := agentcore.LoadAgent(workspace)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("load ephemeral worker %q: %w", targetAlias, err)
	}
	worker.Cron = sourceAgent.Cron
	worker.Delegation = s
	worker.HeartbeatService = sourceAgent.HeartbeatService
	worker.CaptureThinkingDeltas = sourceAgent.CaptureThinkingDeltas

	adapter := agentcore.NewSessionRuntimeAdapter(worker)
	if err := s.router.RegisterActor(targetAlias, adapter); err != nil {
		_ = os.RemoveAll(workspace)
		return nil, err
	}

	s.mu.Lock()
	s.agents[targetAlias] = worker
	s.refreshKnownAgentsLocked()
	s.mu.Unlock()

	return &ephemeralDelegationState{
		AgentID:         targetAlias,
		SourceAgentID:   sourceAgentID,
		SourceWorkspace: sourceWorkspace,
		WorkerWorkspace: workspace,
	}, nil
}

func (s *gatewaySessionDelegationToolService) teardownEphemeralWorker(state ephemeralDelegationState) {
	target := sessionrt.ActorID(strings.TrimSpace(string(state.AgentID)))
	if target == "" {
		return
	}
	if s.router != nil {
		_ = s.router.UnregisterActor(target)
	}

	s.mu.Lock()
	delete(s.agents, target)
	s.refreshKnownAgentsLocked()
	s.mu.Unlock()

	if strings.TrimSpace(state.WorkerWorkspace) != "" {
		_ = os.RemoveAll(state.WorkerWorkspace)
	}
}

func (s *gatewaySessionDelegationToolService) lookupAgent(actorID sessionrt.ActorID) (*agentcore.Agent, bool) {
	id := sessionrt.ActorID(strings.TrimSpace(string(actorID)))
	if id == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	agent, exists := s.agents[id]
	return agent, exists
}

func (s *gatewaySessionDelegationToolService) reserveNextSubagentAlias() sessionrt.ActorID {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 1; i < 100000; i++ {
		candidate := sessionrt.ActorID(fmt.Sprintf("subagent%d", i))
		if _, exists := s.agents[candidate]; exists {
			continue
		}
		if _, reserved := s.reservedWorker[candidate]; reserved {
			continue
		}
		s.reservedWorker[candidate] = struct{}{}
		return candidate
	}
	return ""
}

func (s *gatewaySessionDelegationToolService) reserveAlias(alias sessionrt.ActorID) bool {
	alias = sessionrt.ActorID(strings.TrimSpace(string(alias)))
	if alias == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.agents[alias]; exists {
		return false
	}
	if _, exists := s.reservedWorker[alias]; exists {
		return false
	}
	s.reservedWorker[alias] = struct{}{}
	return true
}

func (s *gatewaySessionDelegationToolService) releaseAlias(alias sessionrt.ActorID) {
	alias = sessionrt.ActorID(strings.TrimSpace(string(alias)))
	if alias == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reservedWorker, alias)
}

func (s *gatewaySessionDelegationToolService) refreshKnownAgentsLocked() {
	if s == nil {
		return
	}
	known := make([]string, 0, len(s.agents))
	for actorID := range s.agents {
		known = append(known, strings.TrimSpace(string(actorID)))
	}
	sort.Strings(known)
	for _, agent := range s.agents {
		if agent == nil {
			continue
		}
		agent.KnownAgents = append([]string(nil), known...)
	}
}

func (s *gatewaySessionDelegationToolService) touchDelegationActivity(delegationID string, at time.Time) {
	delegationID = strings.TrimSpace(delegationID)
	if delegationID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.ephemeral[delegationID]
	if !exists {
		return
	}
	state.LastActivity = at
	s.ephemeral[delegationID] = state
}

func (s *gatewaySessionDelegationToolService) teardownEphemeralForDelegation(delegationID string) {
	if s == nil {
		return
	}
	delegationID = strings.TrimSpace(delegationID)
	if delegationID == "" {
		return
	}
	s.mu.Lock()
	state, exists := s.ephemeral[delegationID]
	if exists {
		delete(s.ephemeral, delegationID)
	}
	s.mu.Unlock()
	if exists {
		s.teardownEphemeralWorker(state)
	}
}

func validateDelegationAlias(alias string) error {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return fmt.Errorf("target agent id is required")
	}
	if !validDelegationAliasPattern.MatchString(alias) {
		return fmt.Errorf("invalid target agent id %q (allowed: letters, numbers, -, _)", alias)
	}
	return nil
}

func createDelegationWorkspaceSnapshot(sourceWorkspace, targetAlias, dataDir string) (string, error) {
	sourceWorkspace = filepath.Clean(strings.TrimSpace(sourceWorkspace))
	if sourceWorkspace == "" {
		return "", fmt.Errorf("source workspace is required")
	}
	base := filepath.Join(os.TempDir(), "gopher", "delegation_workspaces")
	if strings.TrimSpace(dataDir) != "" {
		base = filepath.Join(filepath.Clean(strings.TrimSpace(dataDir)), "control", "delegation_workspaces")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("create delegation workspace root: %w", err)
	}
	workspace, err := os.MkdirTemp(base, strings.TrimSpace(targetAlias)+"-")
	if err != nil {
		return "", fmt.Errorf("create delegation workspace: %w", err)
	}
	if err := copyDirectory(sourceWorkspace, workspace); err != nil {
		_ = os.RemoveAll(workspace)
		return "", fmt.Errorf("copy source workspace: %w", err)
	}
	return workspace, nil
}

func copyDirectory(src, dst string) error {
	src = filepath.Clean(strings.TrimSpace(src))
	dst = filepath.Clean(strings.TrimSpace(dst))
	if src == "" || dst == "" {
		return fmt.Errorf("source and destination are required")
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		targetPath := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		if d.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return nil
	})
}

func writeAgentConfig(path string, value any) error {
	var (
		blob []byte
		err  error
	)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".toml":
		blob, err = toml.Marshal(value)
	case ".json":
		blob, err = json.MarshalIndent(value, "", "  ")
	default:
		return fmt.Errorf("unsupported config format %q", filepath.Ext(path))
	}
	if err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	blob = append(blob, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s parent: %w", path, err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func diffDirectories(sourceDir, targetDir string) ([]byte, error) {
	sourceDir = filepath.Clean(strings.TrimSpace(sourceDir))
	targetDir = filepath.Clean(strings.TrimSpace(targetDir))
	if sourceDir == "" || targetDir == "" {
		return nil, fmt.Errorf("diff directories require source and target")
	}

	if out, gitErr := runDiffCommand("git", []string{"diff", "--no-index", "--", sourceDir, targetDir}); gitErr == nil {
		return out, nil
	} else {
		if out, diffErr := runDiffCommand("diff", []string{"-ruN", sourceDir, targetDir}); diffErr == nil {
			return out, nil
		} else {
			if isCommandNotFound(gitErr) && isCommandNotFound(diffErr) {
				return nil, fmt.Errorf("failed to create diff: neither git nor diff command is available")
			}
			return nil, fmt.Errorf("failed to create diff with git and diff: git=%v diff=%v", gitErr, diffErr)
		}
	}
}

func runDiffCommand(bin string, args []string) ([]byte, error) {
	cmd := exec.Command(bin, args...)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code == 1 {
			return output, nil
		}
		return nil, fmt.Errorf("%s %s: %w\n%s", bin, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil, err
}

func isCommandNotFound(err error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return errors.Is(execErr.Err, fs.ErrNotExist)
	}
	return false
}

func buildDelegationKickoffMessage(targetAgentID, message string) string {
	targetAgentID = strings.TrimSpace(targetAgentID)
	message = strings.TrimSpace(message)
	if targetAgentID == "" {
		return message
	}
	if message == "" {
		return "Delegation request for " + targetAgentID
	}
	return fmt.Sprintf("Delegation for %s:\n%s", targetAgentID, message)
}

func buildDelegationAnnouncement(targetAgentID, delegationSessionID string) string {
	targetAgentID = strings.TrimSpace(targetAgentID)
	delegationSessionID = strings.TrimSpace(delegationSessionID)
	if targetAgentID == "" || delegationSessionID == "" {
		return "Subagent session spawned."
	}
	return fmt.Sprintf("Spawned subagent %s in session %s.", targetAgentID, delegationSessionID)
}

func buildDelegationTerminalAnnouncement(targetAgentID, delegationSessionID, status string) string {
	targetAgentID = strings.TrimSpace(targetAgentID)
	delegationSessionID = strings.TrimSpace(delegationSessionID)
	status = strings.TrimSpace(strings.ToLower(status))
	if targetAgentID == "" || delegationSessionID == "" {
		return "Subagent session finished."
	}
	switch status {
	case "completed":
		return fmt.Sprintf("Subagent %s finished session %s.", targetAgentID, delegationSessionID)
	case "failed":
		return fmt.Sprintf("Subagent %s failed in session %s.", targetAgentID, delegationSessionID)
	default:
		return fmt.Sprintf("Subagent %s ended session %s.", targetAgentID, delegationSessionID)
	}
}

func buildDelegationTerminalMessage(targetAgentID, delegationSessionID, status, reason, diffArtifactPath string) string {
	message := buildDelegationTerminalAnnouncement(targetAgentID, delegationSessionID, status)
	reason = strings.TrimSpace(reason)
	if reason != "" {
		message += "\nReason: " + reason
	}
	diffArtifactPath = strings.TrimSpace(diffArtifactPath)
	if diffArtifactPath != "" {
		message += "\nDiff artifact: " + diffArtifactPath
	}
	return message
}

func delegationTerminalStatusFromEvent(event sessionrt.Event) (status string, reason string, ok bool) {
	switch event.Type {
	case sessionrt.EventControl:
		ctrl, valid := controlPayloadFromAny(event.Payload)
		if !valid {
			return "", "", false
		}
		switch strings.TrimSpace(ctrl.Action) {
		case sessionrt.ControlActionSessionCompleted:
			return "completed", strings.TrimSpace(ctrl.Reason), true
		case sessionrt.ControlActionSessionFailed:
			return "failed", strings.TrimSpace(ctrl.Reason), true
		default:
			return "", "", false
		}
	case sessionrt.EventError:
		errMessage := errorMessageFromAny(event.Payload)
		return "failed", strings.TrimSpace(errMessage), true
	default:
		return "", "", false
	}
}

func controlPayloadFromAny(payload any) (sessionrt.ControlPayload, bool) {
	switch typed := payload.(type) {
	case sessionrt.ControlPayload:
		return typed, true
	case *sessionrt.ControlPayload:
		if typed == nil {
			return sessionrt.ControlPayload{}, false
		}
		return *typed, true
	case map[string]any:
		action, _ := typed["action"].(string)
		reason, _ := typed["reason"].(string)
		if strings.TrimSpace(action) == "" {
			return sessionrt.ControlPayload{}, false
		}
		return sessionrt.ControlPayload{
			Action: strings.TrimSpace(action),
			Reason: strings.TrimSpace(reason),
		}, true
	default:
		blob, err := json.Marshal(payload)
		if err != nil {
			return sessionrt.ControlPayload{}, false
		}
		decoded := sessionrt.ControlPayload{}
		if err := json.Unmarshal(blob, &decoded); err != nil {
			return sessionrt.ControlPayload{}, false
		}
		if strings.TrimSpace(decoded.Action) == "" {
			return sessionrt.ControlPayload{}, false
		}
		return decoded, true
	}
}

func errorMessageFromAny(payload any) string {
	switch typed := payload.(type) {
	case sessionrt.ErrorPayload:
		return strings.TrimSpace(typed.Message)
	case *sessionrt.ErrorPayload:
		if typed == nil {
			return ""
		}
		return strings.TrimSpace(typed.Message)
	case map[string]any:
		msg, _ := typed["message"].(string)
		return strings.TrimSpace(msg)
	default:
		blob, err := json.Marshal(payload)
		if err != nil {
			return ""
		}
		decoded := sessionrt.ErrorPayload{}
		if err := json.Unmarshal(blob, &decoded); err != nil {
			return ""
		}
		return strings.TrimSpace(decoded.Message)
	}
}

func delegationStatus(record map[string]any) string {
	status := strings.ToLower(strings.TrimSpace(stringFromMap(record, "status")))
	if status == "" {
		return "active"
	}
	return status
}

func delegationStatusFromSessionRecord(current string, record sessionrt.SessionRecord) string {
	if strings.TrimSpace(current) != "" && current != "active" {
		return current
	}
	switch record.Status {
	case sessionrt.SessionActive:
		return "active"
	case sessionrt.SessionPaused:
		return "cancelled"
	case sessionrt.SessionCompleted:
		return "completed"
	case sessionrt.SessionFailed:
		return "failed"
	default:
		if current == "" {
			return "unknown"
		}
		return current
	}
}

func delegationLogEntryFromEvent(event sessionrt.Event) agentcore.DelegationLogEntry {
	entry := agentcore.DelegationLogEntry{
		Seq:       event.Seq,
		Type:      strings.TrimSpace(string(event.Type)),
		From:      strings.TrimSpace(string(event.From)),
		Timestamp: event.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	switch payload := event.Payload.(type) {
	case sessionrt.Message:
		entry.Role = strings.TrimSpace(string(payload.Role))
		entry.Content = strings.TrimSpace(payload.Content)
		entry.TargetActorID = strings.TrimSpace(string(payload.TargetActorID))
	case map[string]any:
		entry.Role = strings.TrimSpace(stringFromMap(payload, "role"))
		entry.Content = strings.TrimSpace(stringFromMap(payload, "content"))
		entry.TargetActorID = strings.TrimSpace(stringFromMap(payload, "target_actor_id"))
	}
	return entry
}

func stringFromMap(record map[string]any, key string) string {
	if record == nil {
		return ""
	}
	value, exists := record[key]
	if !exists || value == nil {
		return ""
	}
	if asString, ok := value.(string); ok {
		return strings.TrimSpace(asString)
	}
	return ""
}

func boolFromMap(record map[string]any, key string) bool {
	if record == nil {
		return false
	}
	value, exists := record[key]
	if !exists || value == nil {
		return false
	}
	if asBool, ok := value.(bool); ok {
		return asBool
	}
	if asString, ok := value.(string); ok {
		parsed, _ := strconv.ParseBool(strings.TrimSpace(asString))
		return parsed
	}
	return false
}

func uint64FromMap(record map[string]any, key string) uint64 {
	if record == nil {
		return 0
	}
	value, exists := record[key]
	if !exists || value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case int64:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case int:
		if v < 0 {
			return 0
		}
		return uint64(v)
	case uint64:
		return v
	default:
		return 0
	}
}

func parseRFC3339(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func delegationSessionRecordIsStale(record sessionrt.SessionRecord, now time.Time) bool {
	lastActivity := record.UpdatedAt
	if lastActivity.IsZero() {
		lastActivity = record.CreatedAt
	}
	return sessionrt.IsStaleByDailyReset(lastActivity, now, sessionrt.DefaultDailyResetPolicy())
}
