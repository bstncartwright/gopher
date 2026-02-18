package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type DMPipelineOptions struct {
	Manager          sessionrt.SessionManager
	Transport        transport.Transport
	AgentID          sessionrt.ActorID
	AgentByRecipient map[string]sessionrt.ActorID
	RecipientByAgent map[sessionrt.ActorID]string
	Conversations    *ConversationSessionMap
	Logger           *log.Logger
}

type conversationRoute struct {
	AgentID     sessionrt.ActorID
	RecipientID string
}

type DMPipeline struct {
	manager       sessionrt.SessionManager
	transport     transport.Transport
	agentID       sessionrt.ActorID
	agentByRecip  map[string]sessionrt.ActorID
	recipByAgent  map[sessionrt.ActorID]string
	conversations *ConversationSessionMap
	logger        *log.Logger

	setupMu      sync.Mutex
	subscribedMu sync.Mutex
	subscribed   map[sessionrt.SessionID]struct{}
	fallbackMu   sync.Mutex
	lastFallback map[string]time.Time
	processingMu sync.Mutex
	processing   map[string]int
	routeMu      sync.RWMutex
	routes       map[string]conversationRoute
}

const (
	dmRateLimitFallbackReply = "I hit a temporary rate limit while processing that. Please try again in a moment."
	dmErrorFallbackReply     = "I ran into an upstream error while processing that message. Please try again."
	dmFallbackMinInterval    = 5 * time.Second
	dmTypingTimeout          = 3 * time.Second
)

type typingTransport interface {
	SendTyping(ctx context.Context, conversationID string, typing bool) error
}

func NewDMPipeline(opts DMPipelineOptions) (*DMPipeline, error) {
	if opts.Manager == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	if opts.Transport == nil {
		return nil, fmt.Errorf("transport is required")
	}
	if strings.TrimSpace(string(opts.AgentID)) == "" {
		return nil, fmt.Errorf("agent id is required")
	}
	conversations := opts.Conversations
	if conversations == nil {
		conversations = NewConversationSessionMap()
	}
	agentByRecip := normalizeRecipientAgents(opts.AgentByRecipient)
	recipByAgent := normalizeAgentRecipients(opts.RecipientByAgent)
	for recipientID, actorID := range agentByRecip {
		if _, exists := recipByAgent[actorID]; !exists {
			recipByAgent[actorID] = recipientID
		}
	}
	pipeline := &DMPipeline{
		manager:       opts.Manager,
		transport:     opts.Transport,
		agentID:       opts.AgentID,
		agentByRecip:  agentByRecip,
		recipByAgent:  recipByAgent,
		conversations: conversations,
		logger:        opts.Logger,
		subscribed:    map[sessionrt.SessionID]struct{}{},
		lastFallback:  map[string]time.Time{},
		processing:    map[string]int{},
		routes:        map[string]conversationRoute{},
	}
	pipeline.transport.SetInboundHandler(pipeline.HandleInbound)
	return pipeline, nil
}

func (p *DMPipeline) HandleInbound(ctx context.Context, inbound transport.InboundMessage) error {
	conversationID := strings.TrimSpace(inbound.ConversationID)
	if conversationID == "" {
		return nil
	}

	agentID, recipientID := p.routeForInbound(inbound.RecipientID)
	sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.SenderID, agentID, recipientID)
	if err != nil {
		return err
	}
	p.startProcessing(conversationID)

	// Appservice transactions should be acknowledged quickly; dispatch session work async.
	p.dispatchInboundEvent(sessionrt.Event{
		SessionID: sessionID,
		From:      matrixActorID(inbound.SenderID),
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: inbound.Text,
		},
	}, conversationID, inbound.SenderID)
	return nil
}

func (p *DMPipeline) dispatchInboundEvent(event sessionrt.Event, conversationID, senderID string) {
	go func() {
		if err := p.manager.SendEvent(context.Background(), event); err != nil {
			p.finishProcessing(conversationID)
			if p.logger != nil {
				p.logger.Printf("send dm session event failed conversation_id=%q sender=%q err=%v", conversationID, senderID, err)
			}
			p.sendErrorFallback(conversationID, p.recipientForConversation(conversationID), err.Error())
		}
	}()
}

func (p *DMPipeline) resolveConversationSession(ctx context.Context, conversationID, senderID string, desiredAgentID sessionrt.ActorID, recipientID string) (sessionrt.SessionID, error) {
	if strings.TrimSpace(string(desiredAgentID)) == "" {
		desiredAgentID = p.agentID
	}
	if existing, ok := p.conversations.Get(conversationID); ok {
		if p.isSessionActive(ctx, existing) && p.conversationAgentCompatible(conversationID, desiredAgentID) {
			if err := p.ensureSubscription(conversationID, existing); err != nil {
				return "", err
			}
			p.setConversationRoute(conversationID, desiredAgentID, recipientID)
			return existing, nil
		}
	}

	p.setupMu.Lock()
	defer p.setupMu.Unlock()

	if existing, ok := p.conversations.Get(conversationID); ok {
		if p.isSessionActive(ctx, existing) && p.conversationAgentCompatible(conversationID, desiredAgentID) {
			if err := p.ensureSubscription(conversationID, existing); err != nil {
				return "", err
			}
			p.setConversationRoute(conversationID, desiredAgentID, recipientID)
			return existing, nil
		}
	}

	created, err := p.manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: desiredAgentID, Type: sessionrt.ActorAgent},
			{ID: matrixActorID(senderID), Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create dm session: %w", err)
	}
	if err := p.ensureSubscription(conversationID, created.ID); err != nil {
		_ = p.manager.CancelSession(context.Background(), created.ID)
		return "", err
	}
	p.conversations.Set(conversationID, created.ID)
	p.setConversationRoute(conversationID, desiredAgentID, recipientID)
	return created.ID, nil
}

func (p *DMPipeline) isSessionActive(ctx context.Context, sessionID sessionrt.SessionID) bool {
	session, err := p.manager.GetSession(ctx, sessionID)
	if err != nil || session == nil {
		return false
	}
	return session.Status == sessionrt.SessionActive
}

func (p *DMPipeline) ensureSubscription(conversationID string, sessionID sessionrt.SessionID) error {
	p.subscribedMu.Lock()
	if _, exists := p.subscribed[sessionID]; exists {
		p.subscribedMu.Unlock()
		return nil
	}
	p.subscribedMu.Unlock()

	stream, err := p.manager.Subscribe(context.Background(), sessionID)
	if err != nil {
		return fmt.Errorf("subscribe session events: %w", err)
	}

	p.subscribedMu.Lock()
	if _, exists := p.subscribed[sessionID]; exists {
		p.subscribedMu.Unlock()
		return nil
	}
	p.subscribed[sessionID] = struct{}{}
	p.subscribedMu.Unlock()

	go func() {
		for event := range stream {
			if !p.isCurrentConversationSession(conversationID, sessionID) {
				continue
			}
			if event.Type == sessionrt.EventError {
				p.finishProcessing(conversationID)
				p.sendErrorFallback(conversationID, p.recipientForConversation(conversationID), errorTextFromPayload(event.Payload))
				continue
			}

			if event.Type != sessionrt.EventMessage {
				continue
			}
			msg, ok := event.Payload.(sessionrt.Message)
			if !ok {
				payload, mapOK := event.Payload.(map[string]any)
				if !mapOK {
					continue
				}
				roleRaw, roleOK := payload["role"].(string)
				textRaw, textOK := payload["content"].(string)
				if !roleOK || !textOK {
					continue
				}
				msg = sessionrt.Message{Role: sessionrt.Role(roleRaw), Content: textRaw}
			}
			if msg.Role != sessionrt.RoleAgent {
				continue
			}
			p.finishProcessing(conversationID)
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
				ConversationID: conversationID,
				SenderID:       p.recipientForConversation(conversationID),
				Text:           msg.Content,
			}); err != nil && p.logger != nil {
				p.logger.Printf("send dm response failed: %v", err)
			}
		}
	}()
	return nil
}

func (p *DMPipeline) startProcessing(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	shouldEnable := false
	p.processingMu.Lock()
	if p.processing[conversationID] == 0 {
		shouldEnable = true
	}
	p.processing[conversationID]++
	p.processingMu.Unlock()

	if shouldEnable {
		p.sendTyping(conversationID, true)
	}
}

func (p *DMPipeline) finishProcessing(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	shouldDisable := false
	p.processingMu.Lock()
	count := p.processing[conversationID]
	switch {
	case count <= 0:
		p.processingMu.Unlock()
		return
	case count == 1:
		delete(p.processing, conversationID)
		shouldDisable = true
	default:
		p.processing[conversationID] = count - 1
	}
	p.processingMu.Unlock()

	if shouldDisable {
		p.sendTyping(conversationID, false)
	}
}

func (p *DMPipeline) sendTyping(conversationID string, typing bool) {
	typingAPI, ok := p.transport.(typingTransport)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dmTypingTimeout)
	defer cancel()
	if err := typingAPI.SendTyping(ctx, conversationID, typing); err != nil && p.logger != nil {
		p.logger.Printf("send dm typing failed conversation_id=%q typing=%t err=%v", conversationID, typing, err)
	}
}

func (p *DMPipeline) isCurrentConversationSession(conversationID string, sessionID sessionrt.SessionID) bool {
	current, ok := p.conversations.Get(conversationID)
	return ok && current == sessionID
}

func (p *DMPipeline) routeForInbound(recipientID string) (sessionrt.ActorID, string) {
	recipientNorm := normalizeRecipientID(recipientID)
	if recipientNorm != "" {
		if actorID, ok := p.agentByRecip[recipientNorm]; ok {
			return actorID, recipientNorm
		}
	}
	agentID := p.agentID
	fallbackRecipient := strings.TrimSpace(p.recipByAgent[agentID])
	if recipientNorm != "" {
		fallbackRecipient = recipientNorm
	}
	return agentID, fallbackRecipient
}

func (p *DMPipeline) setConversationRoute(conversationID string, agentID sessionrt.ActorID, recipientID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	if strings.TrimSpace(string(agentID)) == "" {
		agentID = p.agentID
	}
	recipientID = strings.TrimSpace(recipientID)
	if recipientID == "" {
		recipientID = strings.TrimSpace(p.recipByAgent[agentID])
	}
	p.routeMu.Lock()
	defer p.routeMu.Unlock()
	p.routes[conversationID] = conversationRoute{
		AgentID:     agentID,
		RecipientID: recipientID,
	}
}

func (p *DMPipeline) conversationAgentCompatible(conversationID string, desired sessionrt.ActorID) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return true
	}
	desired = sessionrt.ActorID(strings.TrimSpace(string(desired)))
	if desired == "" {
		return true
	}
	p.routeMu.RLock()
	defer p.routeMu.RUnlock()
	route, ok := p.routes[conversationID]
	if !ok || strings.TrimSpace(string(route.AgentID)) == "" {
		return true
	}
	return route.AgentID == desired
}

func (p *DMPipeline) recipientForConversation(conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}
	p.routeMu.RLock()
	route, ok := p.routes[conversationID]
	p.routeMu.RUnlock()
	if !ok {
		return ""
	}
	if strings.TrimSpace(route.RecipientID) != "" {
		return strings.TrimSpace(route.RecipientID)
	}
	if strings.TrimSpace(string(route.AgentID)) != "" {
		return strings.TrimSpace(p.recipByAgent[route.AgentID])
	}
	return ""
}

func errorTextFromPayload(payload any) string {
	switch value := payload.(type) {
	case sessionrt.ErrorPayload:
		return strings.TrimSpace(value.Message)
	case map[string]any:
		raw, _ := value["message"].(string)
		return strings.TrimSpace(raw)
	default:
		return ""
	}
}

func fallbackReplyForError(message string) string {
	text := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(text, "rate limit") || strings.Contains(text, "429") {
		return dmRateLimitFallbackReply
	}
	return dmErrorFallbackReply
}

func (p *DMPipeline) sendErrorFallback(conversationID, senderID, rawErr string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	now := time.Now().UTC()
	p.fallbackMu.Lock()
	last, ok := p.lastFallback[conversationID]
	if ok && now.Sub(last) < dmFallbackMinInterval {
		p.fallbackMu.Unlock()
		return
	}
	p.lastFallback[conversationID] = now
	p.fallbackMu.Unlock()

	reply := fallbackReplyForError(rawErr)
	if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: conversationID,
		SenderID:       strings.TrimSpace(senderID),
		Text:           reply,
	}); err != nil && p.logger != nil {
		p.logger.Printf("send dm error fallback failed: %v", err)
	}
}

func matrixActorID(sender string) sessionrt.ActorID {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return "matrix:unknown"
	}
	return sessionrt.ActorID("matrix:" + sender)
}

func normalizeRecipientAgents(in map[string]sessionrt.ActorID) map[string]sessionrt.ActorID {
	out := map[string]sessionrt.ActorID{}
	for recipientID, actorID := range in {
		recip := normalizeRecipientID(recipientID)
		actor := sessionrt.ActorID(strings.TrimSpace(string(actorID)))
		if recip == "" || strings.TrimSpace(string(actor)) == "" {
			continue
		}
		out[recip] = actor
	}
	return out
}

func normalizeAgentRecipients(in map[sessionrt.ActorID]string) map[sessionrt.ActorID]string {
	out := map[sessionrt.ActorID]string{}
	for actorID, recipientID := range in {
		actor := sessionrt.ActorID(strings.TrimSpace(string(actorID)))
		recip := normalizeRecipientID(recipientID)
		if strings.TrimSpace(string(actor)) == "" || recip == "" {
			continue
		}
		out[actor] = recip
	}
	return out
}

func normalizeRecipientID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToLower(value)
}
