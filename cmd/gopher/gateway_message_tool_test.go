package main

import (
	"context"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type fakeGatewayMessagePipeline struct {
	conversationBySession map[sessionrt.SessionID]string
	lastInboundBySession  map[sessionrt.SessionID]string
	senderByConversation  map[string]string
}

func (p *fakeGatewayMessagePipeline) ConversationForSession(sessionID sessionrt.SessionID) (string, bool) {
	value, ok := p.conversationBySession[sessionID]
	return value, ok
}

func (p *fakeGatewayMessagePipeline) LastInboundEventForSession(sessionID sessionrt.SessionID) (string, bool) {
	value, ok := p.lastInboundBySession[sessionID]
	return value, ok
}

func (p *fakeGatewayMessagePipeline) SenderForConversation(conversationID string) string {
	return p.senderByConversation[conversationID]
}

type fakeGatewayMessageTransport struct {
	sent      []transport.OutboundMessage
	reactions []transport.OutboundReaction
}

func (t *fakeGatewayMessageTransport) Start(context.Context) error { return nil }
func (t *fakeGatewayMessageTransport) Stop() error                 { return nil }
func (t *fakeGatewayMessageTransport) SetInboundHandler(transport.InboundHandler) {
}
func (t *fakeGatewayMessageTransport) SendTyping(context.Context, string, bool) error { return nil }
func (t *fakeGatewayMessageTransport) SendMessage(_ context.Context, message transport.OutboundMessage) error {
	t.sent = append(t.sent, message)
	return nil
}
func (t *fakeGatewayMessageTransport) SendReaction(_ context.Context, reaction transport.OutboundReaction) error {
	t.reactions = append(t.reactions, reaction)
	return nil
}

func TestGatewayMessageToolServiceSendsToBoundConversation(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
		senderByConversation:  map[string]string{"telegram:123": "telegram-bot"},
	}
	tr := &fakeGatewayMessageTransport{}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: tr}

	result, err := service.SendMessage(context.Background(), agentcore.MessageSendRequest{
		SessionID: "sess-1",
		Text:      "hello",
		Attachments: []agentcore.MessageAttachment{{
			Path:     "/tmp/report.md",
			Name:     "report.md",
			MIMEType: "text/markdown",
		}},
	})
	if err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if !result.Sent {
		t.Fatalf("sent = false, want true")
	}
	if result.ConversationID != "telegram:123" {
		t.Fatalf("conversation id = %q, want telegram:123", result.ConversationID)
	}
	if result.AttachmentCount != 1 {
		t.Fatalf("attachment_count = %d, want 1", result.AttachmentCount)
	}
	if len(tr.sent) != 1 {
		t.Fatalf("transport send count = %d, want 1", len(tr.sent))
	}
	if tr.sent[0].SenderID != "telegram-bot" {
		t.Fatalf("sender id = %q, want telegram-bot", tr.sent[0].SenderID)
	}
}

func TestGatewayMessageToolServiceFailsWithoutBoundConversation(t *testing.T) {
	service := &gatewayMessageToolService{
		pipeline:  &fakeGatewayMessagePipeline{conversationBySession: map[sessionrt.SessionID]string{}},
		transport: &fakeGatewayMessageTransport{},
	}

	_, err := service.SendMessage(context.Background(), agentcore.MessageSendRequest{
		SessionID: "missing",
		Text:      "hello",
	})
	if err == nil {
		t.Fatalf("expected missing conversation error")
	}
	if !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("error = %q, want bound-conversation failure", err.Error())
	}
}

func TestGatewayMessageToolServiceRequiresPayload(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
	}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: &fakeGatewayMessageTransport{}}

	_, err := service.SendMessage(context.Background(), agentcore.MessageSendRequest{SessionID: "sess-1"})
	if err == nil {
		t.Fatalf("expected payload validation error")
	}
}

func TestGatewayMessageToolServiceSendsReactionToBoundConversation(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
		lastInboundBySession:  map[sessionrt.SessionID]string{"sess-1": "42"},
		senderByConversation:  map[string]string{"telegram:123": "telegram-bot"},
	}
	tr := &fakeGatewayMessageTransport{}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: tr}

	result, err := service.SendReaction(context.Background(), agentcore.ReactionSendRequest{
		SessionID: "sess-1",
		Emoji:     "👍",
	})
	if err != nil {
		t.Fatalf("SendReaction() error: %v", err)
	}
	if !result.Sent {
		t.Fatalf("sent = false, want true")
	}
	if result.ConversationID != "telegram:123" {
		t.Fatalf("conversation id = %q, want telegram:123", result.ConversationID)
	}
	if len(tr.reactions) != 1 {
		t.Fatalf("transport reaction count = %d, want 1", len(tr.reactions))
	}
	if tr.reactions[0].TargetEventID != "42" {
		t.Fatalf("target_event_id = %q, want 42", tr.reactions[0].TargetEventID)
	}
}

func TestGatewayMessageToolServiceReactionFailsWithoutBoundConversation(t *testing.T) {
	service := &gatewayMessageToolService{
		pipeline:  &fakeGatewayMessagePipeline{conversationBySession: map[sessionrt.SessionID]string{}},
		transport: &fakeGatewayMessageTransport{},
	}

	_, err := service.SendReaction(context.Background(), agentcore.ReactionSendRequest{
		SessionID: "missing",
		Emoji:     "👍",
	})
	if err == nil {
		t.Fatalf("expected missing conversation error")
	}
	if !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("error = %q, want bound-conversation failure", err.Error())
	}
}

func TestGatewayMessageToolServiceReactionFailsWithoutInboundTarget(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
		lastInboundBySession:  map[sessionrt.SessionID]string{},
		senderByConversation:  map[string]string{"telegram:123": "telegram-bot"},
	}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: &fakeGatewayMessageTransport{}}

	_, err := service.SendReaction(context.Background(), agentcore.ReactionSendRequest{
		SessionID: "sess-1",
		Emoji:     "👍",
	})
	if err == nil {
		t.Fatalf("expected missing target error")
	}
	if !strings.Contains(err.Error(), "no inbound message") {
		t.Fatalf("error = %q, want missing inbound target", err.Error())
	}
}
