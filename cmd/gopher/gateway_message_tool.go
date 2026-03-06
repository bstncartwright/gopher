package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type gatewayMessagePipeline interface {
	ConversationForSession(sessionID sessionrt.SessionID) (string, bool)
	LastInboundEventForSession(sessionID sessionrt.SessionID) (string, bool)
	SenderForConversation(conversationID string) string
}

type gatewayMessageHeartbeatFilter interface {
	FilterHeartbeatOutbound(conversationID, text string) (normalized string, suppress bool, isHeartbeat bool)
}

type gatewayMessageToolService struct {
	pipeline       gatewayMessagePipeline
	transport      transport.Transport
	draftMu        sync.Mutex
	draftBySession map[sessionrt.SessionID]int64
	draftSeq       uint64
}

func newGatewayMessageToolService(pipeline *gateway.DMPipeline, transportImpl transport.Transport) *gatewayMessageToolService {
	return &gatewayMessageToolService{
		pipeline:       pipeline,
		transport:      transportImpl,
		draftBySession: map[sessionrt.SessionID]int64{},
	}
}

func (s *gatewayMessageToolService) SendMessage(ctx context.Context, req agentcore.MessageSendRequest) (agentcore.MessageSendResult, error) {
	if s == nil || s.pipeline == nil || s.transport == nil {
		return agentcore.MessageSendResult{}, fmt.Errorf("message service is unavailable")
	}
	sessionID := sessionrt.SessionID(strings.TrimSpace(req.SessionID))
	if strings.TrimSpace(string(sessionID)) == "" {
		return agentcore.MessageSendResult{}, fmt.Errorf("session id is required")
	}
	conversationID, ok := s.pipeline.ConversationForSession(sessionID)
	if !ok || strings.TrimSpace(conversationID) == "" {
		return agentcore.MessageSendResult{}, fmt.Errorf("conversation is not bound for session %q", sessionID)
	}

	text := stripReplyToCurrentTag(strings.TrimSpace(req.Text))
	text, suppress := filterHeartbeatOutbound(s.pipeline, conversationID, text)
	attachments := make([]transport.OutboundAttachment, 0, len(req.Attachments))
	for idx, attachment := range req.Attachments {
		pathValue := strings.TrimSpace(attachment.Path)
		if pathValue == "" {
			return agentcore.MessageSendResult{}, fmt.Errorf("attachments[%d].path is required", idx)
		}
		attachments = append(attachments, transport.OutboundAttachment{
			Path:     pathValue,
			Name:     strings.TrimSpace(attachment.Name),
			MIMEType: strings.TrimSpace(attachment.MIMEType),
		})
	}
	if text == "" && len(attachments) == 0 {
		if suppress {
			s.clearSessionDraft(sessionID)
			return agentcore.MessageSendResult{
				Sent:           true,
				ConversationID: strings.TrimSpace(conversationID),
			}, nil
		}
		return agentcore.MessageSendResult{}, fmt.Errorf("text or attachments is required")
	}

	senderID := s.pipeline.SenderForConversation(conversationID)
	if err := s.transport.SendMessage(ctx, transport.OutboundMessage{
		ConversationID: strings.TrimSpace(conversationID),
		SenderID:       strings.TrimSpace(senderID),
		Text:           text,
		Attachments:    attachments,
	}); err != nil {
		return agentcore.MessageSendResult{}, err
	}
	s.clearSessionDraft(sessionID)

	return agentcore.MessageSendResult{
		Sent:            true,
		ConversationID:  strings.TrimSpace(conversationID),
		Text:            text,
		AttachmentCount: len(attachments),
	}, nil
}

type gatewayMessageDraftTransport interface {
	SendMessageDraft(ctx context.Context, conversationID string, draftID int64, text string) error
}

func (s *gatewayMessageToolService) SendMessageDraft(ctx context.Context, req agentcore.MessageDraftRequest) (agentcore.MessageDraftResult, error) {
	if s == nil || s.pipeline == nil || s.transport == nil {
		return agentcore.MessageDraftResult{}, fmt.Errorf("message service is unavailable")
	}
	streamer, ok := s.transport.(gatewayMessageDraftTransport)
	if !ok {
		return agentcore.MessageDraftResult{}, fmt.Errorf("streaming drafts are unsupported by active transport")
	}
	sessionID := sessionrt.SessionID(strings.TrimSpace(req.SessionID))
	if strings.TrimSpace(string(sessionID)) == "" {
		return agentcore.MessageDraftResult{}, fmt.Errorf("session id is required")
	}
	conversationID, ok := s.pipeline.ConversationForSession(sessionID)
	if !ok || strings.TrimSpace(conversationID) == "" {
		return agentcore.MessageDraftResult{}, fmt.Errorf("conversation is not bound for session %q", sessionID)
	}
	text := stripReplyToCurrentTag(strings.TrimSpace(req.Text))
	text, suppress := filterHeartbeatOutbound(s.pipeline, conversationID, text)
	if text == "" {
		if suppress {
			return agentcore.MessageDraftResult{
				Drafted:        true,
				ConversationID: strings.TrimSpace(conversationID),
				DraftID:        req.DraftID,
			}, nil
		}
		return agentcore.MessageDraftResult{}, fmt.Errorf("text is required")
	}

	draftID := req.DraftID
	if draftID <= 0 {
		draftID = s.sessionDraftID(sessionID)
	}
	if draftID <= 0 {
		return agentcore.MessageDraftResult{}, fmt.Errorf("draft id must be greater than 0")
	}
	if err := streamer.SendMessageDraft(ctx, strings.TrimSpace(conversationID), draftID, text); err != nil {
		return agentcore.MessageDraftResult{}, err
	}
	s.rememberSessionDraft(sessionID, draftID)
	return agentcore.MessageDraftResult{
		Drafted:        true,
		ConversationID: strings.TrimSpace(conversationID),
		DraftID:        draftID,
		Text:           text,
	}, nil
}

func (s *gatewayMessageToolService) SendReaction(ctx context.Context, req agentcore.ReactionSendRequest) (agentcore.ReactionSendResult, error) {
	if s == nil || s.pipeline == nil || s.transport == nil {
		return agentcore.ReactionSendResult{}, fmt.Errorf("reaction service is unavailable")
	}
	reactionSender, ok := s.transport.(transport.ReactionSender)
	if !ok {
		return agentcore.ReactionSendResult{}, fmt.Errorf("reactions are unsupported by active transport")
	}
	sessionID := sessionrt.SessionID(strings.TrimSpace(req.SessionID))
	if strings.TrimSpace(string(sessionID)) == "" {
		return agentcore.ReactionSendResult{}, fmt.Errorf("session id is required")
	}
	conversationID, ok := s.pipeline.ConversationForSession(sessionID)
	if !ok || strings.TrimSpace(conversationID) == "" {
		return agentcore.ReactionSendResult{}, fmt.Errorf("conversation is not bound for session %q", sessionID)
	}
	targetEventID, ok := s.pipeline.LastInboundEventForSession(sessionID)
	if !ok || strings.TrimSpace(targetEventID) == "" {
		return agentcore.ReactionSendResult{}, fmt.Errorf("no inbound message available to react to for session %q", sessionID)
	}
	targetEventID = strings.TrimSpace(targetEventID)
	emoji := strings.TrimSpace(req.Emoji)
	if emoji == "" {
		return agentcore.ReactionSendResult{}, fmt.Errorf("emoji is required")
	}

	senderID := s.pipeline.SenderForConversation(conversationID)
	if err := reactionSender.SendReaction(ctx, transport.OutboundReaction{
		ConversationID: strings.TrimSpace(conversationID),
		SenderID:       strings.TrimSpace(senderID),
		TargetEventID:  targetEventID,
		Emoji:          emoji,
	}); err != nil {
		slog.Warn(
			"gateway_message_tool: send reaction failed",
			"session_id", sessionID,
			"conversation_id", strings.TrimSpace(conversationID),
			"target_event_id", targetEventID,
			"sender_id", strings.TrimSpace(senderID),
			"emoji", emoji,
			"error", err,
		)
		return agentcore.ReactionSendResult{}, fmt.Errorf(
			"send reaction %q for session %q to %s/%s: %w",
			emoji,
			sessionID,
			strings.TrimSpace(conversationID),
			targetEventID,
			err,
		)
	}

	return agentcore.ReactionSendResult{
		Sent:           true,
		ConversationID: strings.TrimSpace(conversationID),
		TargetEventID:  targetEventID,
		Emoji:          emoji,
	}, nil
}

func (s *gatewayMessageToolService) sessionDraftID(sessionID sessionrt.SessionID) int64 {
	if s == nil {
		return 0
	}
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	if s.draftBySession != nil {
		if existing, ok := s.draftBySession[sessionID]; ok && existing > 0 {
			return existing
		}
	}
	seq := atomic.AddUint64(&s.draftSeq, 1)
	draftID := int64((seq % (1<<31 - 1)) + 1)
	if s.draftBySession == nil {
		s.draftBySession = map[sessionrt.SessionID]int64{}
	}
	s.draftBySession[sessionID] = draftID
	return draftID
}

func (s *gatewayMessageToolService) rememberSessionDraft(sessionID sessionrt.SessionID, draftID int64) {
	if s == nil || draftID <= 0 {
		return
	}
	s.draftMu.Lock()
	if s.draftBySession == nil {
		s.draftBySession = map[sessionrt.SessionID]int64{}
	}
	s.draftBySession[sessionID] = draftID
	s.draftMu.Unlock()
}

func (s *gatewayMessageToolService) clearSessionDraft(sessionID sessionrt.SessionID) {
	if s == nil {
		return
	}
	s.draftMu.Lock()
	delete(s.draftBySession, sessionID)
	s.draftMu.Unlock()
}

func stripReplyToCurrentTag(text string) string {
	cleaned := strings.TrimSpace(text)
	for strings.HasPrefix(cleaned, "[[reply_to_current]]") {
		cleaned = strings.TrimSpace(strings.TrimPrefix(cleaned, "[[reply_to_current]]"))
	}
	return cleaned
}

func filterHeartbeatOutbound(pipeline gatewayMessagePipeline, conversationID, text string) (string, bool) {
	filter, ok := pipeline.(gatewayMessageHeartbeatFilter)
	if !ok {
		return text, false
	}
	normalized, suppress, _ := filter.FilterHeartbeatOutbound(conversationID, text)
	if suppress {
		slog.Debug(
			"gateway_message_tool: suppressed heartbeat-only outbound",
			"conversation_id", strings.TrimSpace(conversationID),
		)
		return "", true
	}
	if strings.TrimSpace(normalized) == "" {
		return text, false
	}
	return normalized, false
}
