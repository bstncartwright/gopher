package main

import (
	"context"
	"errors"
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
	sent        []transport.OutboundMessage
	reactions   []transport.OutboundReaction
	drafts      []gatewayMessageDraftCall
	reactionErr error
}

type gatewayMessageDraftCall struct {
	ConversationID string
	DraftID        int64
	Text           string
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
	return t.reactionErr
}
func (t *fakeGatewayMessageTransport) SendMessageDraft(_ context.Context, conversationID string, draftID int64, text string) error {
	t.drafts = append(t.drafts, gatewayMessageDraftCall{
		ConversationID: conversationID,
		DraftID:        draftID,
		Text:           text,
	})
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

func TestGatewayMessageToolServiceStripsReplyTagBeforeSending(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
		senderByConversation:  map[string]string{"telegram:123": "telegram-bot"},
	}
	tr := &fakeGatewayMessageTransport{}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: tr}

	result, err := service.SendMessage(context.Background(), agentcore.MessageSendRequest{
		SessionID: "sess-1",
		Text:      "[[reply_to_current]] hello",
	})
	if err != nil {
		t.Fatalf("SendMessage() error: %v", err)
	}
	if result.Text != "hello" {
		t.Fatalf("result text = %q, want hello", result.Text)
	}
	if len(tr.sent) != 1 {
		t.Fatalf("transport send count = %d, want 1", len(tr.sent))
	}
	if tr.sent[0].Text != "hello" {
		t.Fatalf("transport text = %q, want hello", tr.sent[0].Text)
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

func TestGatewayMessageToolServiceStreamsDraftToBoundConversation(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
	}
	tr := &fakeGatewayMessageTransport{}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: tr}

	result, err := service.SendMessageDraft(context.Background(), agentcore.MessageDraftRequest{
		SessionID: "sess-1",
		Text:      "drafting",
	})
	if err != nil {
		t.Fatalf("SendMessageDraft() error: %v", err)
	}
	if !result.Drafted {
		t.Fatalf("drafted = false, want true")
	}
	if result.ConversationID != "telegram:123" {
		t.Fatalf("conversation id = %q, want telegram:123", result.ConversationID)
	}
	if result.DraftID <= 0 {
		t.Fatalf("draft id = %d, want > 0", result.DraftID)
	}
	if len(tr.drafts) != 1 {
		t.Fatalf("transport draft count = %d, want 1", len(tr.drafts))
	}
	if tr.drafts[0].DraftID != result.DraftID {
		t.Fatalf("transport draft id = %d, want %d", tr.drafts[0].DraftID, result.DraftID)
	}
}

func TestGatewayMessageToolServiceStripsReplyTagBeforeDrafting(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
	}
	tr := &fakeGatewayMessageTransport{}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: tr}

	result, err := service.SendMessageDraft(context.Background(), agentcore.MessageDraftRequest{
		SessionID: "sess-1",
		Text:      "[[reply_to_current]] drafting",
	})
	if err != nil {
		t.Fatalf("SendMessageDraft() error: %v", err)
	}
	if result.Text != "drafting" {
		t.Fatalf("result text = %q, want drafting", result.Text)
	}
	if len(tr.drafts) != 1 {
		t.Fatalf("transport draft count = %d, want 1", len(tr.drafts))
	}
	if tr.drafts[0].Text != "drafting" {
		t.Fatalf("transport text = %q, want drafting", tr.drafts[0].Text)
	}
}

func TestGatewayMessageToolServiceReusesSessionDraftID(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
	}
	tr := &fakeGatewayMessageTransport{}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: tr}

	first, err := service.SendMessageDraft(context.Background(), agentcore.MessageDraftRequest{
		SessionID: "sess-1",
		Text:      "one",
	})
	if err != nil {
		t.Fatalf("SendMessageDraft(first) error: %v", err)
	}
	second, err := service.SendMessageDraft(context.Background(), agentcore.MessageDraftRequest{
		SessionID: "sess-1",
		Text:      "two",
	})
	if err != nil {
		t.Fatalf("SendMessageDraft(second) error: %v", err)
	}
	if first.DraftID != second.DraftID {
		t.Fatalf("draft ids = %d/%d, want equal", first.DraftID, second.DraftID)
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

func TestGatewayMessageToolServiceReactionWrapsTransportErrorWithContext(t *testing.T) {
	pipeline := &fakeGatewayMessagePipeline{
		conversationBySession: map[sessionrt.SessionID]string{"sess-1": "telegram:123"},
		lastInboundBySession:  map[sessionrt.SessionID]string{"sess-1": "42"},
		senderByConversation:  map[string]string{"telegram:123": "telegram-bot"},
	}
	tr := &fakeGatewayMessageTransport{reactionErr: errors.New("telegram setMessageReaction returned ok=false (error_code=400): Bad Request: reaction is invalid")}
	service := &gatewayMessageToolService{pipeline: pipeline, transport: tr}

	_, err := service.SendReaction(context.Background(), agentcore.ReactionSendRequest{
		SessionID: "sess-1",
		Emoji:     "✅",
	})
	if err == nil {
		t.Fatalf("expected wrapped transport error")
	}
	if !strings.Contains(err.Error(), `send reaction "✅"`) {
		t.Fatalf("error = %q, want emoji context", err.Error())
	}
	if !strings.Contains(err.Error(), "telegram:123/42") {
		t.Fatalf("error = %q, want conversation and target context", err.Error())
	}
	if !strings.Contains(err.Error(), "reaction is invalid") {
		t.Fatalf("error = %q, want telegram cause", err.Error())
	}
}
