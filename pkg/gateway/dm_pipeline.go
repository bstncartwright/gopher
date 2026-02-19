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
	Bindings         ConversationBindingStore
	Logger           *log.Logger
}

type conversationRoute struct {
	AgentID     sessionrt.ActorID
	RecipientID string
	Mode        ConversationMode
}

type HeartbeatTarget struct {
	ConversationID string
	SessionID      sessionrt.SessionID
}

type DMPipeline struct {
	manager       sessionrt.SessionManager
	transport     transport.Transport
	agentID       sessionrt.ActorID
	agentByRecip  map[string]sessionrt.ActorID
	recipByAgent  map[sessionrt.ActorID]string
	conversations *ConversationSessionMap
	bindings      ConversationBindingStore
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
	heartbeatMu  sync.Mutex
	heartbeats   map[string]heartbeatState
}

type heartbeatState struct {
	pending     int
	ackMaxChars int
}

const (
	dmRateLimitFallbackReply = "I hit a temporary rate limit while processing that. Please try again in a moment."
	dmErrorFallbackReply     = "I ran into an upstream error while processing that message. Please try again."
	dmFallbackMinInterval    = 5 * time.Second
	dmTypingTimeout          = 3 * time.Second
	heartbeatOKToken         = "HEARTBEAT_OK"
	heartbeatAckDefaultChars = 300
)

type typingTransport interface {
	SendTyping(ctx context.Context, conversationID string, typing bool) error
}

type managedConversationUsersReader interface {
	ManagedUsersForConversation(conversationID string) []string
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
	bindings := opts.Bindings
	if bindings == nil {
		bindings = NewInMemoryConversationBindingStore()
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
		bindings:      bindings,
		logger:        opts.Logger,
		subscribed:    map[sessionrt.SessionID]struct{}{},
		lastFallback:  map[string]time.Time{},
		processing:    map[string]int{},
		routes:        map[string]conversationRoute{},
		heartbeats:    map[string]heartbeatState{},
	}
	for _, binding := range bindings.List() {
		pipeline.conversations.Set(binding.ConversationID, binding.SessionID)
		pipeline.setConversationRoute(binding.ConversationID, binding.AgentID, binding.RecipientID, binding.Mode)
	}
	pipeline.transport.SetInboundHandler(pipeline.HandleInbound)
	return pipeline, nil
}

func (p *DMPipeline) HandleInbound(ctx context.Context, inbound transport.InboundMessage) error {
	conversationID := strings.TrimSpace(inbound.ConversationID)
	if conversationID == "" {
		return nil
	}
	if inbound.SenderManaged {
		mode := p.conversationModeFor(conversationID)
		if mode != ConversationModeDelegation {
			return nil
		}
		if existingSessionID, ok := p.conversations.Get(conversationID); !ok || !p.isSessionActive(ctx, existingSessionID) {
			return nil
		}
	}

	agentID, recipientID := p.routeForInbound(inbound.RecipientID)
	sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.SenderID, agentID, recipientID)
	if err != nil {
		return err
	}
	p.startProcessing(conversationID)

	from := matrixActorID(inbound.SenderID)
	if inbound.SenderManaged {
		if managedActor, ok := p.actorForManagedSender(inbound.SenderID); ok {
			from = managedActor
		} else {
			p.finishProcessing(conversationID)
			return nil
		}
	}

	// Appservice transactions should be acknowledged quickly; dispatch session work async.
	p.dispatchInboundEvent(sessionrt.Event{
		SessionID: sessionID,
		From:      from,
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
	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if !p.isSessionActive(ctx, existing) {
			return "", fmt.Errorf("conversation %q is bound to inactive session %q", conversationID, existing)
		}
		if err := p.ensureSubscription(conversationID, existing); err != nil {
			return "", err
		}
		if _, routeExists := p.currentRoute(conversationID); !routeExists {
			p.setConversationRoute(conversationID, desiredAgentID, recipientID, ConversationModeDM)
		}
		return existing, nil
	}

	p.setupMu.Lock()
	defer p.setupMu.Unlock()

	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if !p.isSessionActive(ctx, existing) {
			return "", fmt.Errorf("conversation %q is bound to inactive session %q", conversationID, existing)
		}
		if err := p.ensureSubscription(conversationID, existing); err != nil {
			return "", err
		}
		if _, routeExists := p.currentRoute(conversationID); !routeExists {
			p.setConversationRoute(conversationID, desiredAgentID, recipientID, ConversationModeDM)
		}
		return existing, nil
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
	if err := p.bindConversation(conversationID, created.ID, desiredAgentID, recipientID, ConversationModeDM); err != nil {
		_ = p.manager.CancelSession(context.Background(), created.ID)
		return "", err
	}
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
			content := strings.TrimSpace(msg.Content)
			if suppress, normalized := p.consumeHeartbeatReply(conversationID, content); suppress {
				continue
			} else if normalized != "" && normalized != content {
				msg.Content = normalized
				content = normalized
			}
			if content == "" {
				continue
			}
			if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
				ConversationID: conversationID,
				SenderID:       p.senderForConversationEvent(conversationID, event.From),
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

func (p *DMPipeline) lookupConversationSession(conversationID string) (sessionrt.SessionID, bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", false
	}
	if existing, ok := p.conversations.Get(conversationID); ok {
		return existing, true
	}
	if p.bindings == nil {
		return "", false
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return "", false
	}
	p.conversations.Set(conversationID, binding.SessionID)
	p.setConversationRoute(conversationID, binding.AgentID, binding.RecipientID, binding.Mode)
	return binding.SessionID, true
}

func (p *DMPipeline) bindConversation(conversationID string, sessionID sessionrt.SessionID, agentID sessionrt.ActorID, recipientID string, mode ConversationMode) error {
	conversationID = strings.TrimSpace(conversationID)
	sessionID = sessionrt.SessionID(strings.TrimSpace(string(sessionID)))
	if conversationID == "" || strings.TrimSpace(string(sessionID)) == "" {
		return fmt.Errorf("conversation id and session id are required")
	}
	if strings.TrimSpace(string(agentID)) == "" {
		agentID = p.agentID
	}
	if strings.TrimSpace(recipientID) == "" {
		recipientID = strings.TrimSpace(p.recipByAgent[agentID])
	}
	mode = normalizeConversationMode(mode)
	if p.bindings != nil {
		if err := p.bindings.Set(ConversationBinding{
			ConversationID: conversationID,
			SessionID:      sessionID,
			AgentID:        agentID,
			RecipientID:    recipientID,
			Mode:           mode,
		}); err != nil {
			return err
		}
	}
	p.conversations.Set(conversationID, sessionID)
	p.setConversationRoute(conversationID, agentID, recipientID, mode)
	return nil
}

func (p *DMPipeline) BindConversation(conversationID string, sessionID sessionrt.SessionID, agentID sessionrt.ActorID, recipientID string, mode ConversationMode) error {
	if err := p.bindConversation(conversationID, sessionID, agentID, recipientID, mode); err != nil {
		return err
	}
	return p.ensureSubscription(conversationID, sessionID)
}

func (p *DMPipeline) ConversationForSession(sessionID sessionrt.SessionID) (string, bool) {
	if p.bindings == nil {
		conversations := p.conversations.Snapshot()
		for conversationID, currentSessionID := range conversations {
			if currentSessionID == sessionID {
				return conversationID, true
			}
		}
		return "", false
	}
	binding, ok := p.bindings.GetBySession(sessionID)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(binding.ConversationID), true
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

func (p *DMPipeline) setConversationRoute(conversationID string, agentID sessionrt.ActorID, recipientID string, mode ConversationMode) {
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
	mode = normalizeConversationMode(mode)
	p.routeMu.Lock()
	defer p.routeMu.Unlock()
	p.routes[conversationID] = conversationRoute{
		AgentID:     agentID,
		RecipientID: recipientID,
		Mode:        mode,
	}
}

func (p *DMPipeline) currentRoute(conversationID string) (conversationRoute, bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return conversationRoute{}, false
	}
	p.routeMu.RLock()
	defer p.routeMu.RUnlock()
	route, ok := p.routes[conversationID]
	return route, ok
}

func (p *DMPipeline) recipientForConversation(conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}
	route, ok := p.currentRoute(conversationID)
	if !ok {
		if p.bindings != nil {
			if binding, exists := p.bindings.GetByConversation(conversationID); exists {
				if strings.TrimSpace(binding.RecipientID) != "" {
					return strings.TrimSpace(binding.RecipientID)
				}
				if strings.TrimSpace(string(binding.AgentID)) != "" {
					return strings.TrimSpace(p.recipByAgent[binding.AgentID])
				}
			}
		}
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

func (p *DMPipeline) senderForConversationEvent(conversationID string, eventFrom sessionrt.ActorID) string {
	from := sessionrt.ActorID(strings.TrimSpace(string(eventFrom)))
	if strings.TrimSpace(string(from)) != "" {
		if sender := strings.TrimSpace(p.recipByAgent[from]); sender != "" {
			return sender
		}
	}
	return p.recipientForConversation(conversationID)
}

func (p *DMPipeline) conversationModeFor(conversationID string) ConversationMode {
	route, ok := p.currentRoute(conversationID)
	if ok {
		return normalizeConversationMode(route.Mode)
	}
	if p.bindings != nil {
		if binding, ok := p.bindings.GetByConversation(conversationID); ok {
			return normalizeConversationMode(binding.Mode)
		}
	}
	return ConversationModeDM
}

func (p *DMPipeline) actorForManagedSender(senderID string) (sessionrt.ActorID, bool) {
	actor, ok := p.agentByRecip[normalizeRecipientID(senderID)]
	if !ok || strings.TrimSpace(string(actor)) == "" {
		return "", false
	}
	return actor, true
}

func (p *DMPipeline) CanDispatchHeartbeat(conversationID string, agentID sessionrt.ActorID) bool {
	conversationID = strings.TrimSpace(conversationID)
	agentID = sessionrt.ActorID(strings.TrimSpace(string(agentID)))
	if conversationID == "" || strings.TrimSpace(string(agentID)) == "" {
		return false
	}
	provider, ok := p.transport.(managedConversationUsersReader)
	if !ok {
		return true
	}
	recipientID := normalizeRecipientID(p.recipByAgent[agentID])
	if recipientID == "" {
		return false
	}
	managedUsers := provider.ManagedUsersForConversation(conversationID)
	for _, managedUserID := range managedUsers {
		if normalizeRecipientID(managedUserID) == recipientID {
			return true
		}
	}
	return false
}

func (p *DMPipeline) HeartbeatTargets() []HeartbeatTarget {
	if p.bindings == nil {
		conversations := p.conversations.Snapshot()
		if len(conversations) == 0 {
			return nil
		}
		out := make([]HeartbeatTarget, 0, len(conversations))
		for conversationID, sessionID := range conversations {
			conversationID = strings.TrimSpace(conversationID)
			if conversationID == "" || strings.TrimSpace(string(sessionID)) == "" {
				continue
			}
			out = append(out, HeartbeatTarget{
				ConversationID: conversationID,
				SessionID:      sessionID,
			})
		}
		return out
	}
	bindings := p.bindings.List()
	if len(bindings) == 0 {
		return nil
	}
	out := make([]HeartbeatTarget, 0, len(bindings))
	for _, binding := range bindings {
		conversationID := strings.TrimSpace(binding.ConversationID)
		sessionID := sessionrt.SessionID(strings.TrimSpace(string(binding.SessionID)))
		if conversationID == "" || strings.TrimSpace(string(sessionID)) == "" {
			continue
		}
		out = append(out, HeartbeatTarget{
			ConversationID: conversationID,
			SessionID:      sessionID,
		})
	}
	return out
}

func (p *DMPipeline) MarkHeartbeatPending(conversationID string, ackMaxChars int) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	if ackMaxChars <= 0 {
		ackMaxChars = heartbeatAckDefaultChars
	}
	p.heartbeatMu.Lock()
	defer p.heartbeatMu.Unlock()
	state := p.heartbeats[conversationID]
	state.pending++
	state.ackMaxChars = ackMaxChars
	p.heartbeats[conversationID] = state
}

func (p *DMPipeline) UnmarkHeartbeatPending(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	p.heartbeatMu.Lock()
	defer p.heartbeatMu.Unlock()
	state, ok := p.heartbeats[conversationID]
	if !ok {
		return
	}
	if state.pending <= 1 {
		delete(p.heartbeats, conversationID)
		return
	}
	state.pending--
	p.heartbeats[conversationID] = state
}

func (p *DMPipeline) IsConversationProcessing(conversationID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false
	}
	p.processingMu.Lock()
	defer p.processingMu.Unlock()
	return p.processing[conversationID] > 0
}

func (p *DMPipeline) consumeHeartbeatReply(conversationID, text string) (bool, string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false, text
	}
	p.heartbeatMu.Lock()
	state, ok := p.heartbeats[conversationID]
	if !ok || state.pending <= 0 {
		p.heartbeatMu.Unlock()
		return false, text
	}
	if state.pending <= 1 {
		delete(p.heartbeats, conversationID)
	} else {
		state.pending--
		p.heartbeats[conversationID] = state
	}
	p.heartbeatMu.Unlock()

	ackMaxChars := state.ackMaxChars
	if ackMaxChars <= 0 {
		ackMaxChars = heartbeatAckDefaultChars
	}
	normalized, hasToken := normalizeHeartbeatReply(text)
	if !hasToken {
		return false, text
	}
	if len([]rune(normalized)) <= ackMaxChars {
		return true, ""
	}
	return false, normalized
}

func normalizeHeartbeatReply(text string) (string, bool) {
	normalized := strings.TrimSpace(text)
	hasToken := false
	for {
		switch {
		case strings.HasPrefix(normalized, heartbeatOKToken):
			normalized = strings.TrimSpace(strings.TrimPrefix(normalized, heartbeatOKToken))
			hasToken = true
		case strings.HasSuffix(normalized, heartbeatOKToken):
			normalized = strings.TrimSpace(strings.TrimSuffix(normalized, heartbeatOKToken))
			hasToken = true
		default:
			return normalized, hasToken
		}
	}
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
