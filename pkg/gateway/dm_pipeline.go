package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
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
	TracePublisher   TracePublisher
	TraceProvisioner TraceConversationProvisioner
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

type TraceConversationRequest struct {
	ConversationID   string
	ConversationName string
	SessionID        sessionrt.SessionID
	AgentID          sessionrt.ActorID
	SenderID         string
	RecipientID      string
}

type TraceConversationBinding struct {
	ConversationID   string
	ConversationName string
	Mode             string
	Render           string
}

type TraceConversationProvisioner interface {
	CreateTraceConversation(ctx context.Context, req TraceConversationRequest) (TraceConversationBinding, error)
}

type DMPipeline struct {
	manager       sessionrt.SessionManager
	transport     transport.Transport
	agentID       sessionrt.ActorID
	agentByRecip  map[string]sessionrt.ActorID
	recipByAgent  map[sessionrt.ActorID]string
	conversations *ConversationSessionMap
	bindings      ConversationBindingStore

	setupMu          sync.Mutex
	subscribedMu     sync.Mutex
	subscribed       map[sessionrt.SessionID]struct{}
	fallbackMu       sync.Mutex
	lastFallback     map[string]time.Time
	processingMu     sync.Mutex
	processing       map[string]int
	typingMu         sync.Mutex
	typingCancel     map[string]context.CancelFunc
	routeMu          sync.RWMutex
	routes           map[string]conversationRoute
	traceRouteMu     sync.RWMutex
	traceByDM        map[string]string
	dmByTrace        map[string]string
	traceSetupMu     sync.Mutex
	traceSetup       map[sessionrt.SessionID]struct{}
	heartbeatMu      sync.Mutex
	heartbeats       map[string]heartbeatState
	tracePublisher   TracePublisher
	traceProvisioner TraceConversationProvisioner
}

type heartbeatState struct {
	pending     int
	ackMaxChars int
}

const (
	dmRateLimitFallbackReply = "I hit a temporary rate limit while processing that. Please try again in a moment."
	dmErrorFallbackReply     = "I ran into an upstream error while processing that message. Please try again."
	dmContextClearedReply    = "Context cleared. Started a fresh session for this chat."
	dmFallbackMinInterval    = 5 * time.Second
	dmFallbackDetailMaxChars = 120
	dmTypingTimeout          = 3 * time.Second
	dmTypingKeepaliveDefault = 5 * time.Second
	heartbeatOKToken         = "HEARTBEAT_OK"
	heartbeatAckDefaultChars = 300
)

var dmTypingKeepaliveInterval = dmTypingKeepaliveDefault

const dmSummarizeCommandPrompt = "Summarize this conversation so far in 8 bullets max. Include: objective, key decisions, important constraints, open questions, and next steps."

var (
	fallbackAuthBearerPattern     = regexp.MustCompile(`(?i)\bauthorization\b\s*[:=]\s*bearer\s+[^\s,;]+`)
	fallbackBearerPattern         = regexp.MustCompile(`(?i)\bbearer\s+[^\s,;]+`)
	fallbackSensitiveValuePattern = regexp.MustCompile(`(?i)\b(authorization|bearer|api[-_ ]?key|token|secret|password)\b\s*[:=]?\s*([^\s,;]+)`)
	fallbackLongTokenPattern      = regexp.MustCompile(`\b[A-Za-z0-9_-]{24,}\b`)
)

type typingTransport interface {
	SendTyping(ctx context.Context, conversationID string, typing bool) error
}

type typingSenderTransport interface {
	SendTypingAs(ctx context.Context, conversationID, senderID string, typing bool) error
}

type managedConversationUsersReader interface {
	ManagedUsersForConversation(conversationID string) []string
}

func NewDMPipeline(opts DMPipelineOptions) (*DMPipeline, error) {
	if opts.Manager == nil {
		slog.Error("dm_pipeline: session manager is required")
		return nil, fmt.Errorf("session manager is required")
	}
	if opts.Transport == nil {
		slog.Error("dm_pipeline: transport is required")
		return nil, fmt.Errorf("transport is required")
	}
	if strings.TrimSpace(string(opts.AgentID)) == "" {
		slog.Error("dm_pipeline: agent id is required")
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
	slog.Info("dm_pipeline: creating pipeline",
		"agent_id", opts.AgentID,
		"agents_by_recipient_count", len(agentByRecip),
	)
	pipeline := &DMPipeline{
		manager:          opts.Manager,
		transport:        opts.Transport,
		agentID:          opts.AgentID,
		agentByRecip:     agentByRecip,
		recipByAgent:     recipByAgent,
		conversations:    conversations,
		bindings:         bindings,
		subscribed:       map[sessionrt.SessionID]struct{}{},
		lastFallback:     map[string]time.Time{},
		processing:       map[string]int{},
		typingCancel:     map[string]context.CancelFunc{},
		routes:           map[string]conversationRoute{},
		traceByDM:        map[string]string{},
		dmByTrace:        map[string]string{},
		traceSetup:       map[sessionrt.SessionID]struct{}{},
		heartbeats:       map[string]heartbeatState{},
		tracePublisher:   opts.TracePublisher,
		traceProvisioner: opts.TraceProvisioner,
	}
	for _, binding := range bindings.List() {
		pipeline.conversations.Set(binding.ConversationID, binding.SessionID)
		pipeline.setConversationRoute(binding.ConversationID, binding.AgentID, binding.RecipientID, binding.Mode)
		pipeline.setTraceConversationRoute(binding.ConversationID, binding.TraceConversationID)
	}
	pipeline.transport.SetInboundHandler(pipeline.HandleInbound)
	slog.Info("dm_pipeline: pipeline created", "agent_id", opts.AgentID)
	return pipeline, nil
}

func (p *DMPipeline) HandleInbound(ctx context.Context, inbound transport.InboundMessage) error {
	conversationID := strings.TrimSpace(inbound.ConversationID)
	if conversationID == "" {
		return nil
	}
	if p.isTraceConversation(conversationID) {
		p.recordTraceInboundIgnored()
		return nil
	}
	slog.Debug("dm_pipeline: handling inbound",
		"conversation_id", conversationID,
		"sender_id", inbound.SenderID,
		"recipient_id", inbound.RecipientID,
		"sender_managed", inbound.SenderManaged,
	)
	if inbound.SenderManaged {
		mode := p.conversationModeFor(conversationID)
		if mode != ConversationModeDelegation {
			slog.Debug("dm_pipeline: skipping managed sender - not delegation mode",
				"conversation_id", conversationID,
				"mode", mode,
			)
			return nil
		}
		if existingSessionID, ok := p.conversations.Get(conversationID); !ok || !p.isSessionActive(ctx, existingSessionID) {
			slog.Debug("dm_pipeline: skipping managed sender - no active session",
				"conversation_id", conversationID,
			)
			return nil
		}
	}

	agentID, recipientID := p.routeForInbound(inbound.RecipientID)
	if handled, err := p.handleInboundCommand(ctx, inbound, agentID, recipientID); handled || err != nil {
		return err
	}

	sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID)
	if err != nil {
		slog.Error("dm_pipeline: failed to resolve session",
			"conversation_id", conversationID,
			"sender_id", inbound.SenderID,
			"error", err,
		)
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
	}, conversationID, inbound.SenderID, inbound.EventID)
	return nil
}

func (p *DMPipeline) handleInboundCommand(ctx context.Context, inbound transport.InboundMessage, agentID sessionrt.ActorID, recipientID string) (bool, error) {
	action, ok := parseDMContextCommand(inbound.Text)
	if !ok {
		return false, nil
	}

	conversationID := strings.TrimSpace(inbound.ConversationID)
	switch action {
	case "clear":
		if err := p.resetConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID); err != nil {
			return true, err
		}
		if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
			ConversationID: conversationID,
			SenderID:       recipientID,
			Text:           dmContextClearedReply,
		}); err != nil {
			slog.Error("dm_pipeline: failed to send context cleared acknowledgement",
				"conversation_id", conversationID,
				"error", err,
			)
		}
		return true, nil
	case "summarize":
		sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.ConversationName, inbound.SenderID, agentID, recipientID)
		if err != nil {
			return true, err
		}
		p.startProcessing(conversationID)
		p.dispatchInboundEvent(sessionrt.Event{
			SessionID: sessionID,
			From:      matrixActorID(inbound.SenderID),
			Type:      sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleUser,
				Content: dmSummarizeCommandPrompt,
			},
		}, conversationID, inbound.SenderID, "")
		return true, nil
	default:
		return false, nil
	}
}

func parseDMContextCommand(text string) (string, bool) {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(text)))
	if len(fields) == 0 {
		return "", false
	}
	switch fields[0] {
	case "/clear", "/reset":
		return "clear", true
	case "/summarize", "/summary":
		return "summarize", true
	case "/context":
		if len(fields) < 2 {
			return "", false
		}
		switch fields[1] {
		case "clear", "reset":
			return "clear", true
		case "summarize", "summary":
			return "summarize", true
		}
	}
	return "", false
}

func (p *DMPipeline) resetConversationSession(ctx context.Context, conversationID, conversationName, senderID string, agentID sessionrt.ActorID, recipientID string) error {
	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if err := p.manager.CancelSession(ctx, existing); err != nil && err != sessionrt.ErrSessionNotFound {
			return fmt.Errorf("cancel previous session: %w", err)
		}
	}
	_, err := p.resolveConversationSession(ctx, conversationID, conversationName, senderID, agentID, recipientID)
	if err != nil {
		return fmt.Errorf("create replacement session: %w", err)
	}
	return nil
}

func (p *DMPipeline) dispatchInboundEvent(event sessionrt.Event, conversationID, senderID, inboundEventID string) {
	go func() {
		if err := p.manager.SendEvent(context.Background(), event); err != nil {
			p.finishProcessing(conversationID)
			slog.Error("dm_pipeline: send dm session event failed",
				"conversation_id", conversationID,
				"sender_id", senderID,
				"error", err,
			)
			p.sendErrorFallback(conversationID, p.recipientForConversation(conversationID), err.Error())
			return
		}
		p.markInboundEventProcessed(conversationID, event.SessionID, inboundEventID)
	}()
}

func (p *DMPipeline) resolveConversationSession(ctx context.Context, conversationID, conversationName, senderID string, desiredAgentID sessionrt.ActorID, recipientID string) (sessionrt.SessionID, error) {
	conversationName = strings.TrimSpace(conversationName)
	if strings.TrimSpace(string(desiredAgentID)) == "" {
		desiredAgentID = p.agentID
	}
	if route, ok := p.currentRoute(conversationID); ok {
		if strings.TrimSpace(string(route.AgentID)) != "" {
			desiredAgentID = route.AgentID
		}
		if strings.TrimSpace(route.RecipientID) != "" {
			recipientID = route.RecipientID
		}
	}
	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if !p.isSessionActive(ctx, existing) {
			slog.Debug("dm_pipeline: conversation has inactive session; creating replacement",
				"conversation_id", conversationID,
				"session_id", existing,
			)
		} else {
			if err := p.ensureSubscription(conversationID, existing); err != nil {
				return "", err
			}
			route, routeExists := p.currentRoute(conversationID)
			if !routeExists {
				p.setConversationRoute(conversationID, desiredAgentID, recipientID, ConversationModeDM)
				route = conversationRoute{
					AgentID:     desiredAgentID,
					RecipientID: recipientID,
					Mode:        ConversationModeDM,
				}
			}
			if err := p.maybeUpdateConversationName(conversationID, existing, route, conversationName); err != nil {
				return "", err
			}
			return existing, nil
		}
	}

	p.setupMu.Lock()
	defer p.setupMu.Unlock()

	if existing, ok := p.lookupConversationSession(conversationID); ok {
		if !p.isSessionActive(ctx, existing) {
			slog.Debug("dm_pipeline: conversation has inactive session after lock; creating replacement",
				"conversation_id", conversationID,
				"session_id", existing,
			)
		} else {
			if err := p.ensureSubscription(conversationID, existing); err != nil {
				return "", err
			}
			route, routeExists := p.currentRoute(conversationID)
			if !routeExists {
				p.setConversationRoute(conversationID, desiredAgentID, recipientID, ConversationModeDM)
				route = conversationRoute{
					AgentID:     desiredAgentID,
					RecipientID: recipientID,
					Mode:        ConversationModeDM,
				}
			}
			if err := p.maybeUpdateConversationName(conversationID, existing, route, conversationName); err != nil {
				return "", err
			}
			return existing, nil
		}
	}
	if route, ok := p.currentRoute(conversationID); ok {
		if strings.TrimSpace(string(route.AgentID)) != "" {
			desiredAgentID = route.AgentID
		}
		if strings.TrimSpace(route.RecipientID) != "" {
			recipientID = route.RecipientID
		}
	}

	slog.Info("dm_pipeline: creating new session",
		"conversation_id", conversationID,
		"agent_id", desiredAgentID,
		"sender_id", senderID,
	)
	created, err := p.manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: desiredAgentID, Type: sessionrt.ActorAgent},
			{ID: matrixActorID(senderID), Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		slog.Error("dm_pipeline: failed to create session",
			"conversation_id", conversationID,
			"error", err,
		)
		return "", fmt.Errorf("create dm session: %w", err)
	}
	if err := p.ensureSubscription(conversationID, created.ID); err != nil {
		_ = p.manager.CancelSession(context.Background(), created.ID)
		return "", err
	}
	if err := p.bindConversation(conversationID, created.ID, desiredAgentID, recipientID, ConversationModeDM, conversationName); err != nil {
		_ = p.manager.CancelSession(context.Background(), created.ID)
		return "", err
	}
	p.ensureTraceConversation(ctx, TraceConversationRequest{
		ConversationID:   conversationID,
		ConversationName: conversationName,
		SessionID:        created.ID,
		AgentID:          desiredAgentID,
		SenderID:         senderID,
		RecipientID:      recipientID,
	})
	slog.Info("dm_pipeline: session created",
		"conversation_id", conversationID,
		"session_id", created.ID,
	)
	return created.ID, nil
}

func (p *DMPipeline) maybeUpdateConversationName(conversationID string, sessionID sessionrt.SessionID, route conversationRoute, conversationName string) error {
	conversationName = strings.TrimSpace(conversationName)
	if conversationName == "" || p.bindings == nil {
		return nil
	}
	existing, ok := p.bindings.GetByConversation(conversationID)
	if ok && strings.TrimSpace(existing.ConversationName) == conversationName {
		return nil
	}
	return p.bindConversation(conversationID, sessionID, route.AgentID, route.RecipientID, route.Mode, conversationName)
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
			if err := p.publishTraceEvent(conversationID, event); err != nil {
				slog.Warn("dm_pipeline: publish trace event failed",
					"conversation_id", conversationID,
					"session_id", sessionID,
					"event_type", event.Type,
					"error", err,
				)
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
			slog.Debug("dm_pipeline: sending message",
				"conversation_id", conversationID,
				"content_length", len(content),
			)
			if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
				ConversationID: conversationID,
				SenderID:       p.senderForConversationEvent(conversationID, event.From),
				Text:           msg.Content,
			}); err != nil {
				slog.Error("dm_pipeline: send dm response failed",
					"conversation_id", conversationID,
					"error", err,
				)
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
		p.startTypingKeepalive(conversationID)
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
		p.stopTypingKeepalive(conversationID)
		p.sendTyping(conversationID, false)
	}
}

func (p *DMPipeline) startTypingKeepalive(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	p.typingMu.Lock()
	if _, exists := p.typingCancel[conversationID]; exists {
		p.typingMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.typingCancel[conversationID] = cancel
	p.typingMu.Unlock()

	interval := dmTypingKeepaliveInterval
	if interval <= 0 {
		interval = dmTypingKeepaliveDefault
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !p.IsConversationProcessing(conversationID) {
					return
				}
				p.sendTyping(conversationID, true)
			}
		}
	}()
}

func (p *DMPipeline) stopTypingKeepalive(conversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}

	p.typingMu.Lock()
	cancel, exists := p.typingCancel[conversationID]
	if exists {
		delete(p.typingCancel, conversationID)
	}
	p.typingMu.Unlock()

	if exists {
		cancel()
	}
}

func (p *DMPipeline) sendTyping(conversationID string, typing bool) {
	typingAPI, ok := p.transport.(typingTransport)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dmTypingTimeout)
	defer cancel()
	if senderTypingAPI, ok := p.transport.(typingSenderTransport); ok {
		senderID := p.recipientForConversation(conversationID)
		if err := senderTypingAPI.SendTypingAs(ctx, conversationID, senderID, typing); err != nil {
			slog.Warn("dm_pipeline: send dm typing failed",
				"conversation_id", conversationID,
				"sender_id", senderID,
				"typing", typing,
				"error", err,
			)
		}
		return
	}
	if err := typingAPI.SendTyping(ctx, conversationID, typing); err != nil {
		slog.Warn("dm_pipeline: send dm typing failed",
			"conversation_id", conversationID,
			"typing", typing,
			"error", err,
		)
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
	p.setTraceConversationRoute(conversationID, binding.TraceConversationID)
	return binding.SessionID, true
}

func (p *DMPipeline) bindConversation(conversationID string, sessionID sessionrt.SessionID, agentID sessionrt.ActorID, recipientID string, mode ConversationMode, conversationName string) error {
	conversationID = strings.TrimSpace(conversationID)
	conversationName = strings.TrimSpace(conversationName)
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
			ConversationID:   conversationID,
			ConversationName: conversationName,
			SessionID:        sessionID,
			AgentID:          agentID,
			RecipientID:      recipientID,
			Mode:             mode,
		}); err != nil {
			return err
		}
		if stored, ok := p.bindings.GetByConversation(conversationID); ok {
			p.setTraceConversationRoute(conversationID, stored.TraceConversationID)
		}
	} else {
		p.setTraceConversationRoute(conversationID, "")
	}
	p.conversations.Set(conversationID, sessionID)
	p.setConversationRoute(conversationID, agentID, recipientID, mode)
	return nil
}

func (p *DMPipeline) markInboundEventProcessed(conversationID string, sessionID sessionrt.SessionID, eventID string) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" || p.bindings == nil {
		return
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return
	}
	if binding.LastInboundEvent == eventID {
		return
	}
	binding.LastInboundEvent = eventID
	if strings.TrimSpace(string(sessionID)) != "" {
		binding.SessionID = sessionID
	}
	if err := p.bindings.Set(binding); err != nil {
		slog.Warn("dm_pipeline: failed to persist inbound event checkpoint",
			"conversation_id", conversationID,
			"event_id", eventID,
			"error", err,
		)
	}
}

func (p *DMPipeline) BindConversation(conversationID string, sessionID sessionrt.SessionID, agentID sessionrt.ActorID, recipientID string, mode ConversationMode) error {
	if err := p.bindConversation(conversationID, sessionID, agentID, recipientID, mode, ""); err != nil {
		return err
	}
	return p.ensureSubscription(conversationID, sessionID)
}

func (p *DMPipeline) EnsureTraceConversation(ctx context.Context, req TraceConversationRequest) {
	p.ensureTraceConversation(ctx, req)
}

func (p *DMPipeline) TraceConversationFor(conversationID string) (string, bool) {
	return p.traceConversationFor(conversationID)
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
	detail := fallbackErrorDetail(message)
	text := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(text, "rate limit") || strings.Contains(text, "429") {
		if detail == "" {
			detail = "rate limit (429)"
		}
		return appendFallbackDetail(dmRateLimitFallbackReply, detail)
	}
	return appendFallbackDetail(dmErrorFallbackReply, detail)
}

func fallbackErrorDetail(message string) string {
	sanitized := sanitizeFallbackErrorDetail(message)
	if sanitized == "" {
		return ""
	}
	text := strings.ToLower(sanitized)
	switch {
	case strings.Contains(text, "rate limit"), strings.Contains(text, "429"), strings.Contains(text, "too many requests"):
		return "rate limit (429)"
	case strings.Contains(text, "deadline exceeded"), strings.Contains(text, "timed out"), strings.Contains(text, "timeout"):
		return "request timed out"
	case strings.Contains(text, "connection refused"), strings.Contains(text, "upstream connect"), strings.Contains(text, "connection reset"), strings.Contains(text, "dial tcp"), strings.Contains(text, "no such host"):
		return "connection to provider failed"
	case strings.Contains(text, "unauthorized"), strings.Contains(text, "invalid api key"), strings.Contains(text, "401"):
		return "provider authentication failed (401)"
	case strings.Contains(text, "forbidden"), strings.Contains(text, "403"):
		return "provider rejected the request (403)"
	case strings.Contains(text, "model not found"), strings.Contains(text, "404"):
		return "model not found (404)"
	case strings.Contains(text, "service unavailable"), strings.Contains(text, "overloaded"), strings.Contains(text, "502"), strings.Contains(text, "503"), strings.Contains(text, "500"), strings.Contains(text, "bad gateway"):
		return "provider service unavailable"
	}
	return sanitized
}

func sanitizeFallbackErrorDetail(message string) string {
	detail := strings.TrimSpace(message)
	if detail == "" {
		return ""
	}
	detail = strings.Join(strings.Fields(detail), " ")
	detail = fallbackAuthBearerPattern.ReplaceAllString(detail, "authorization=[redacted]")
	detail = fallbackBearerPattern.ReplaceAllString(detail, "bearer [redacted]")
	detail = fallbackSensitiveValuePattern.ReplaceAllString(detail, "$1=[redacted]")
	detail = fallbackLongTokenPattern.ReplaceAllString(detail, "[redacted]")
	if len([]rune(detail)) > dmFallbackDetailMaxChars {
		runes := []rune(detail)
		detail = strings.TrimSpace(string(runes[:dmFallbackDetailMaxChars])) + "..."
	}
	return strings.TrimSpace(detail)
}

func appendFallbackDetail(base, detail string) string {
	base = strings.TrimSpace(base)
	detail = strings.TrimSpace(strings.TrimRight(detail, "."))
	if base == "" || detail == "" {
		return base
	}
	base = strings.TrimSuffix(base, ".")
	return base + " Details: " + detail + "."
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
	slog.Warn("dm_pipeline: sending error fallback",
		"conversation_id", conversationID,
		"error_message", rawErr,
	)
	if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: conversationID,
		SenderID:       strings.TrimSpace(senderID),
		Text:           reply,
	}); err != nil {
		slog.Error("dm_pipeline: send dm error fallback failed",
			"conversation_id", conversationID,
			"error", err,
		)
	}
}

func (p *DMPipeline) ensureTraceConversation(ctx context.Context, req TraceConversationRequest) {
	if p == nil || p.traceProvisioner == nil {
		return
	}
	req.ConversationID = strings.TrimSpace(req.ConversationID)
	req.ConversationName = strings.TrimSpace(req.ConversationName)
	req.SessionID = sessionrt.SessionID(strings.TrimSpace(string(req.SessionID)))
	req.AgentID = sessionrt.ActorID(strings.TrimSpace(string(req.AgentID)))
	req.SenderID = strings.TrimSpace(req.SenderID)
	req.RecipientID = strings.TrimSpace(req.RecipientID)
	if req.ConversationID == "" || strings.TrimSpace(string(req.SessionID)) == "" {
		return
	}

	p.traceSetupMu.Lock()
	if _, exists := p.traceSetup[req.SessionID]; exists {
		p.traceSetupMu.Unlock()
		return
	}
	p.traceSetup[req.SessionID] = struct{}{}
	p.traceSetupMu.Unlock()

	created, err := p.traceProvisioner.CreateTraceConversation(ctx, req)
	if err != nil {
		slog.Warn("dm_pipeline: failed to create trace conversation",
			"conversation_id", req.ConversationID,
			"session_id", req.SessionID,
			"error", err,
		)
		return
	}
	if strings.TrimSpace(created.ConversationID) == "" {
		return
	}
	if err := p.setConversationTraceBinding(req.ConversationID, created); err != nil {
		slog.Warn("dm_pipeline: failed to persist trace conversation",
			"conversation_id", req.ConversationID,
			"session_id", req.SessionID,
			"trace_conversation_id", created.ConversationID,
			"error", err,
		)
		return
	}
	p.sendTraceConversationReadyNotice(req.ConversationID, created.ConversationID)
}

func (p *DMPipeline) setConversationTraceBinding(conversationID string, trace TraceConversationBinding) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("conversation id is required")
	}
	traceID := strings.TrimSpace(trace.ConversationID)
	traceName := strings.TrimSpace(trace.ConversationName)
	traceMode := normalizeTraceMode(trace.Mode)
	traceRender := normalizeTraceRender(trace.Render)
	if traceID != "" {
		if traceMode == "" {
			traceMode = TraceModeReadOnly
		}
		if traceRender == "" {
			traceRender = TraceRenderCards
		}
	}

	if p.bindings == nil {
		p.setTraceConversationRoute(conversationID, traceID)
		return nil
	}
	binding, ok := p.bindings.GetByConversation(conversationID)
	if !ok {
		return fmt.Errorf("conversation binding not found")
	}
	binding.TraceConversationID = traceID
	binding.TraceConversationName = traceName
	binding.TraceMode = traceMode
	binding.TraceRender = traceRender
	if err := p.bindings.Set(binding); err != nil {
		return err
	}
	p.setTraceConversationRoute(conversationID, traceID)
	return nil
}

func (p *DMPipeline) publishTraceEvent(conversationID string, event sessionrt.Event) error {
	if p == nil || p.tracePublisher == nil {
		return nil
	}
	traceConversationID, ok := p.traceConversationFor(conversationID)
	if !ok {
		return nil
	}
	return p.tracePublisher.PublishEvent(context.Background(), traceConversationID, event)
}

func (p *DMPipeline) setTraceConversationRoute(conversationID, traceConversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return
	}
	traceConversationID = strings.TrimSpace(traceConversationID)
	p.traceRouteMu.Lock()
	defer p.traceRouteMu.Unlock()
	if existingTrace, ok := p.traceByDM[conversationID]; ok && existingTrace != "" {
		delete(p.dmByTrace, existingTrace)
	}
	if traceConversationID == "" {
		delete(p.traceByDM, conversationID)
		return
	}
	p.traceByDM[conversationID] = traceConversationID
	p.dmByTrace[traceConversationID] = conversationID
}

func (p *DMPipeline) traceConversationFor(conversationID string) (string, bool) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return "", false
	}
	p.traceRouteMu.RLock()
	defer p.traceRouteMu.RUnlock()
	traceConversationID, ok := p.traceByDM[conversationID]
	if !ok || strings.TrimSpace(traceConversationID) == "" {
		return "", false
	}
	return traceConversationID, true
}

func (p *DMPipeline) isTraceConversation(conversationID string) bool {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return false
	}
	p.traceRouteMu.RLock()
	defer p.traceRouteMu.RUnlock()
	_, ok := p.dmByTrace[conversationID]
	return ok
}

type traceInboundIgnoredRecorder interface {
	RecordTraceInboundIgnored()
}

func (p *DMPipeline) recordTraceInboundIgnored() {
	if p == nil || p.transport == nil {
		return
	}
	recorder, ok := p.transport.(traceInboundIgnoredRecorder)
	if !ok {
		return
	}
	recorder.RecordTraceInboundIgnored()
}

func (p *DMPipeline) sendTraceConversationReadyNotice(conversationID, traceConversationID string) {
	conversationID = strings.TrimSpace(conversationID)
	traceConversationID = strings.TrimSpace(traceConversationID)
	if conversationID == "" || traceConversationID == "" {
		return
	}
	if p == nil || p.transport == nil {
		return
	}
	senderID := p.recipientForConversation(conversationID)
	message := traceConversationReadyMessage(traceConversationID)
	if strings.TrimSpace(message) == "" {
		return
	}
	if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
		ConversationID: conversationID,
		SenderID:       senderID,
		Text:           message,
	}); err != nil {
		slog.Warn("dm_pipeline: failed to send trace conversation notice",
			"conversation_id", conversationID,
			"trace_conversation_id", traceConversationID,
			"error", err,
		)
	}
}

func traceConversationReadyMessage(traceConversationID string) string {
	traceConversationID = strings.TrimSpace(traceConversationID)
	if traceConversationID == "" {
		return ""
	}
	link := "https://matrix.to/#/" + url.PathEscape(traceConversationID)
	return "Trace channel (read-only): " + link
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
