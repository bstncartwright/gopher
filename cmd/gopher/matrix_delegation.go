package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	matrixtransport "github.com/bstncartwright/gopher/pkg/transport/matrix"
)

type gatewayDelegationToolService struct {
	manager    sessionrt.SessionManager
	pipeline   *gateway.DMPipeline
	transport  *matrixtransport.Transport
	identities agentMatrixIdentitySet
	logger     *log.Logger
}

func newGatewayDelegationToolService(
	manager sessionrt.SessionManager,
	pipeline *gateway.DMPipeline,
	transport *matrixtransport.Transport,
	identities agentMatrixIdentitySet,
	logger *log.Logger,
) *gatewayDelegationToolService {
	return &gatewayDelegationToolService{
		manager:    manager,
		pipeline:   pipeline,
		transport:  transport,
		identities: identities,
		logger:     logger,
	}
}

func (s *gatewayDelegationToolService) CreateDelegationSession(ctx context.Context, req agentcore.DelegationCreateRequest) (agentcore.DelegationSession, error) {
	if s == nil || s.manager == nil || s.pipeline == nil || s.transport == nil {
		return agentcore.DelegationSession{}, fmt.Errorf("delegation service is unavailable")
	}

	sourceSessionID := sessionrt.SessionID(strings.TrimSpace(req.SourceSessionID))
	sourceAgentID := sessionrt.ActorID(strings.TrimSpace(req.SourceAgentID))
	targetAgentID := sessionrt.ActorID(strings.TrimSpace(req.TargetAgentID))
	message := strings.TrimSpace(req.Message)
	title := strings.TrimSpace(req.Title)

	if strings.TrimSpace(string(sourceSessionID)) == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("source session id is required")
	}
	if strings.TrimSpace(string(sourceAgentID)) == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("source agent id is required")
	}
	if strings.TrimSpace(string(targetAgentID)) == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("target agent id is required")
	}
	if sourceAgentID == targetAgentID {
		return agentcore.DelegationSession{}, fmt.Errorf("source and target agents must be different")
	}
	if message == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("message is required")
	}

	sourceUserID, ok := s.identities.UserByActorID[sourceAgentID]
	if !ok || strings.TrimSpace(sourceUserID) == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("unknown source agent %q", sourceAgentID)
	}
	targetUserID, ok := s.identities.UserByActorID[targetAgentID]
	if !ok || strings.TrimSpace(targetUserID) == "" {
		return agentcore.DelegationSession{}, fmt.Errorf("unknown target agent %q", targetAgentID)
	}

	sourceSession, err := s.manager.GetSession(ctx, sourceSessionID)
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("load source session: %w", err)
	}
	humanActorID, humanUserID, err := firstHumanMatrixParticipant(sourceSession)
	if err != nil {
		return agentcore.DelegationSession{}, err
	}

	createdSession, err := s.manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: sourceAgentID, Type: sessionrt.ActorAgent},
			{ID: targetAgentID, Type: sessionrt.ActorAgent},
			{ID: humanActorID, Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("create delegation session: %w", err)
	}
	if title == "" {
		title = fmt.Sprintf("Delegation %s -> %s", strings.TrimSpace(string(sourceAgentID)), strings.TrimSpace(string(targetAgentID)))
	}
	roomID, err := s.transport.CreatePublicRoom(ctx, matrixtransport.CreatePublicRoomOptions{
		Name:          title,
		Topic:         "Delegation session " + strings.TrimSpace(string(createdSession.ID)),
		CreatorUserID: sourceUserID,
		InviteUserIDs: []string{humanUserID, targetUserID},
	})
	if err != nil {
		_ = s.manager.CancelSession(context.Background(), createdSession.ID)
		return agentcore.DelegationSession{}, fmt.Errorf("create delegation room: %w", err)
	}
	if err := s.pipeline.BindConversation(roomID, createdSession.ID, targetAgentID, targetUserID, gateway.ConversationModeDelegation); err != nil {
		_ = s.manager.CancelSession(context.Background(), createdSession.ID)
		return agentcore.DelegationSession{}, fmt.Errorf("bind delegation room to session: %w", err)
	}

	kickoff := buildDelegationKickoffMessage(targetUserID, message)
	if err := s.transport.SendMessageAs(ctx, roomID, sourceUserID, kickoff); err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("send delegation kickoff message: %w", err)
	}
	if err := s.manager.SendEvent(ctx, sessionrt.Event{
		SessionID: createdSession.ID,
		From:      sourceAgentID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: kickoff,
		},
	}); err != nil {
		return agentcore.DelegationSession{}, fmt.Errorf("enqueue delegation kickoff event: %w", err)
	}
	if s.logger != nil {
		s.logger.Printf("delegation session created source=%s target=%s session=%s room=%s", sourceAgentID, targetAgentID, createdSession.ID, roomID)
	}
	return agentcore.DelegationSession{
		SessionID:      strings.TrimSpace(string(createdSession.ID)),
		ConversationID: roomID,
		SourceAgentID:  strings.TrimSpace(string(sourceAgentID)),
		TargetAgentID:  strings.TrimSpace(string(targetAgentID)),
		SourceUserID:   strings.TrimSpace(sourceUserID),
		TargetUserID:   strings.TrimSpace(targetUserID),
		HumanUserID:    strings.TrimSpace(humanUserID),
		KickoffMessage: kickoff,
	}, nil
}

func firstHumanMatrixParticipant(session *sessionrt.Session) (sessionrt.ActorID, string, error) {
	if session == nil {
		return "", "", fmt.Errorf("source session is required")
	}
	humanIDs := make([]string, 0, len(session.Participants))
	for actorID, participant := range session.Participants {
		if participant.Type != sessionrt.ActorHuman {
			continue
		}
		humanIDs = append(humanIDs, strings.TrimSpace(string(actorID)))
	}
	sort.Strings(humanIDs)
	for _, actorRaw := range humanIDs {
		actorID := sessionrt.ActorID(actorRaw)
		userID := matrixUserIDFromActorID(actorID)
		if userID == "" {
			continue
		}
		return actorID, userID, nil
	}
	return "", "", fmt.Errorf("source session has no matrix human participant")
}

func matrixUserIDFromActorID(actorID sessionrt.ActorID) string {
	value := strings.TrimSpace(string(actorID))
	if !strings.HasPrefix(value, "matrix:") {
		return ""
	}
	userID := strings.TrimSpace(strings.TrimPrefix(value, "matrix:"))
	if userID == "" {
		return ""
	}
	return userID
}

func buildDelegationKickoffMessage(targetUserID, message string) string {
	targetUserID = strings.TrimSpace(targetUserID)
	message = strings.TrimSpace(message)
	if targetUserID == "" {
		return message
	}
	return strings.TrimSpace(targetUserID + " " + message)
}
