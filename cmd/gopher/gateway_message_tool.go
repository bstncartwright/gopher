package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type gatewayMessagePipeline interface {
	ConversationForSession(sessionID sessionrt.SessionID) (string, bool)
	SenderForConversation(conversationID string) string
}

type gatewayMessageToolService struct {
	pipeline  gatewayMessagePipeline
	transport transport.Transport
}

func newGatewayMessageToolService(pipeline *gateway.DMPipeline, transportImpl transport.Transport) *gatewayMessageToolService {
	return &gatewayMessageToolService{
		pipeline:  pipeline,
		transport: transportImpl,
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

	text := strings.TrimSpace(req.Text)
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

	return agentcore.MessageSendResult{
		Sent:            true,
		ConversationID:  strings.TrimSpace(conversationID),
		Text:            text,
		AttachmentCount: len(attachments),
	}, nil
}
