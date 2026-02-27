package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type gatewaySessionDelegationStore interface {
	List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error)
	ListSessions(ctx context.Context) ([]sessionrt.SessionRecord, error)
}

type gatewaySessionDelegationToolService struct {
	manager sessionrt.SessionManager
	store   gatewaySessionDelegationStore
	agents  map[sessionrt.ActorID]*agentcore.Agent
	dataDir string
	logger  *log.Logger
}

const delegationAsyncSendTimeout = 10 * time.Minute

func newGatewaySessionDelegationToolService(
	manager sessionrt.SessionManager,
	store gatewaySessionDelegationStore,
	agents map[sessionrt.ActorID]*agentcore.Agent,
	dataDir string,
	logger *log.Logger,
) *gatewaySessionDelegationToolService {
	return &gatewaySessionDelegationToolService{
		manager: manager,
		store:   store,
		agents:  agents,
		dataDir: strings.TrimSpace(dataDir),
		logger:  logger,
	}
}

func (s *gatewaySessionDelegationToolService) CreateDelegationSession(ctx context.Context, req agentcore.DelegationCreateRequest) (agentcore.DelegationSession, error) {
	if s == nil || s.manager == nil {
		return agentcore.DelegationSession{}, fmt.Errorf("delegation service is unavailable")
	}
	sourceSessionID := sessionrt.SessionID(strings.TrimSpace(req.SourceSessionID))
	sourceAgentID := sessionrt.ActorID(strings.TrimSpace(req.SourceAgentID))
	requestedTargetAgentID := sessionrt.ActorID(strings.TrimSpace(req.TargetAgentID))
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
	if _, exists := s.agents[sourceAgentID]; !exists {
		return agentcore.DelegationSession{}, fmt.Errorf("unknown source agent %q", sourceAgentID)
	}
	resolvedTargetAgentID, displayTargetAgentID, err := s.resolveDelegationTarget(sourceAgentID, requestedTargetAgentID)
	if err != nil {
		return agentcore.DelegationSession{}, err
	}
	if resolvedTargetAgentID == sourceAgentID {
		return agentcore.DelegationSession{}, fmt.Errorf("source and target agents must be different")
	}
	sourceSession, err := s.manager.GetSession(ctx, sourceSessionID)
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("load source session: %w", err)
	}
	if sourceSession == nil {
		return agentcore.DelegationSession{}, fmt.Errorf("source session %q not found", sourceSessionID)
	}

	participants := []sessionrt.Participant{{ID: sourceAgentID, Type: sessionrt.ActorAgent}}
	if resolvedTargetAgentID != sourceAgentID {
		participants = append(participants, sessionrt.Participant{ID: resolvedTargetAgentID, Type: sessionrt.ActorAgent})
	}
	createdSession, err := s.manager.CreateSession(ctx, sessionrt.CreateSessionOptions{Participants: participants})
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("create delegation session: %w", err)
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

	now := time.Now().UTC().Format(time.RFC3339Nano)
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
	}
	s.appendDelegationRecord(record)
	s.sendDelegationControlEventAsync(sourceSessionID, "delegation.created", map[string]any{
		"delegation_id":         string(createdSession.ID),
		"target_agent":          displayTargetAgentID,
		"resolved_target_agent": string(resolvedTargetAgentID),
		"announcement":          announcement,
	})
	if s.logger != nil {
		s.logger.Printf(
			"delegation session created source=%s target=%s resolved_target=%s source_session=%s delegated_session=%s",
			sourceAgentID,
			displayTargetAgentID,
			resolvedTargetAgentID,
			sourceSessionID,
			createdSession.ID,
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
	}, nil
}

func (s *gatewaySessionDelegationToolService) resolveDelegationTarget(sourceAgentID, requestedTargetAgentID sessionrt.ActorID) (sessionrt.ActorID, string, error) {
	if s == nil {
		return "", "", fmt.Errorf("delegation service is unavailable")
	}
	sourceAgentID = sessionrt.ActorID(strings.TrimSpace(string(sourceAgentID)))
	requestedTargetAgentID = sessionrt.ActorID(strings.TrimSpace(string(requestedTargetAgentID)))
	if sourceAgentID == "" {
		return "", "", fmt.Errorf("source agent id is required")
	}
	known := make([]string, 0, len(s.agents))
	for id := range s.agents {
		known = append(known, strings.TrimSpace(string(id)))
	}
	sort.Strings(known)
	if requestedTargetAgentID == "" {
		if len(known) == 0 {
			return "", "", fmt.Errorf("target agent id is required")
		}
		return "", "", fmt.Errorf("target agent id is required (known agents: %s)", strings.Join(known, ", "))
	}
	if _, exists := s.agents[requestedTargetAgentID]; exists {
		return requestedTargetAgentID, strings.TrimSpace(string(requestedTargetAgentID)), nil
	}
	if len(known) == 0 {
		return "", "", fmt.Errorf("unknown target agent %q", requestedTargetAgentID)
	}
	return "", "", fmt.Errorf("unknown target agent %q (known agents: %s)", requestedTargetAgentID, strings.Join(known, ", "))
}

func (s *gatewaySessionDelegationToolService) ListDelegationSessions(ctx context.Context, req agentcore.DelegationListRequest) ([]agentcore.DelegationListItem, error) {
	if s == nil {
		return nil, fmt.Errorf("delegation service is unavailable")
	}
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

		items = append(items, agentcore.DelegationListItem{
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
		})
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

	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.appendDelegationRecord(map[string]any{
		"ts":                now,
		"event":             "cancelled",
		"delegation_id":     delegationID,
		"source_session_id": sourceSessionID,
		"source_agent_id":   stringFromMap(record, "source_agent_id"),
		"target_agent_id":   stringFromMap(record, "target_agent_id"),
		"title":             stringFromMap(record, "title"),
		"status":            "cancelled",
		"created_at":        stringFromMap(record, "created_at"),
		"updated_at":        now,
	})
	s.sendDelegationControlEventAsync(sessionrt.SessionID(sourceSessionID), "delegation.cancelled", map[string]any{
		"delegation_id": delegationID,
	})

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

func (s *gatewaySessionDelegationToolService) sendDelegationControlEvent(ctx context.Context, sourceSessionID sessionrt.SessionID, action string, metadata map[string]any) error {
	if s == nil || s.manager == nil {
		return nil
	}
	if strings.TrimSpace(string(sourceSessionID)) == "" || strings.TrimSpace(action) == "" {
		return nil
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
	if err := s.manager.SendEvent(ctx, event); err != nil {
		if s.logger != nil {
			s.logger.Printf("delegation control event send failed action=%s source_session=%s err=%v", action, sourceSessionID, err)
		}
		return err
	}
	return nil
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
