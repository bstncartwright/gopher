package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type gatewaySessionDelegationToolService struct {
	manager sessionrt.SessionManager
	agents  map[sessionrt.ActorID]*agentcore.Agent
	dataDir string
	logger  *log.Logger
}

func newGatewaySessionDelegationToolService(
	manager sessionrt.SessionManager,
	agents map[sessionrt.ActorID]*agentcore.Agent,
	dataDir string,
	logger *log.Logger,
) *gatewaySessionDelegationToolService {
	return &gatewaySessionDelegationToolService{
		manager: manager,
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
	targetAgentID := sessionrt.ActorID(strings.TrimSpace(req.TargetAgentID))
	message := strings.TrimSpace(req.Message)
	if sourceSessionID == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("source session id is required")
	}
	if sourceAgentID == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("source agent id is required")
	}
	if targetAgentID == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("target agent id is required")
	}
	if sourceAgentID == targetAgentID {
		return agentcore.DelegationSession{}, fmt.Errorf("source and target agents must be different")
	}
	if message == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("message is required")
	}
	if _, exists := s.agents[sourceAgentID]; !exists {
		return agentcore.DelegationSession{}, fmt.Errorf("unknown source agent %q", sourceAgentID)
	}
	if _, exists := s.agents[targetAgentID]; !exists {
		return agentcore.DelegationSession{}, fmt.Errorf("unknown target agent %q", targetAgentID)
	}
	sourceSession, err := s.manager.GetSession(ctx, sourceSessionID)
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("load source session: %w", err)
	}
	if sourceSession == nil {
		return agentcore.DelegationSession{}, fmt.Errorf("source session %q not found", sourceSessionID)
	}

	createdSession, err := s.manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: sourceAgentID, Type: sessionrt.ActorAgent},
			{ID: targetAgentID, Type: sessionrt.ActorAgent},
		},
	})
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("create delegation session: %w", err)
	}
	kickoff := buildDelegationKickoffMessage(string(targetAgentID), message)
	sendErr := s.manager.SendEvent(ctx, sessionrt.Event{
		SessionID: createdSession.ID,
		From:      sourceAgentID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:          sessionrt.RoleUser,
			Content:       kickoff,
			TargetActorID: targetAgentID,
		},
	})
	if sendErr != nil {
		_ = s.manager.CancelSession(context.Background(), createdSession.ID)
		return agentcore.DelegationSession{}, fmt.Errorf("enqueue delegation kickoff event: %w", sendErr)
	}

	record := map[string]any{
		"ts":                time.Now().UTC().Format(time.RFC3339Nano),
		"delegation_id":     strings.TrimSpace(string(createdSession.ID)),
		"source_session_id": strings.TrimSpace(string(sourceSessionID)),
		"source_agent_id":   strings.TrimSpace(string(sourceAgentID)),
		"target_agent_id":   strings.TrimSpace(string(targetAgentID)),
		"title":             strings.TrimSpace(req.Title),
		"kickoff_message":   kickoff,
		"status":            "active",
	}
	s.appendDelegationRecord(record)
	if s.logger != nil {
		s.logger.Printf("delegation session created source=%s target=%s source_session=%s delegated_session=%s", sourceAgentID, targetAgentID, sourceSessionID, createdSession.ID)
	}

	return agentcore.DelegationSession{
		SessionID:      strings.TrimSpace(string(createdSession.ID)),
		ConversationID: "session:" + strings.TrimSpace(string(createdSession.ID)),
		SourceAgentID:  strings.TrimSpace(string(sourceAgentID)),
		TargetAgentID:  strings.TrimSpace(string(targetAgentID)),
		KickoffMessage: kickoff,
	}, nil
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
