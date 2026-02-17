package gateway

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type DMPipelineOptions struct {
	Manager     sessionrt.SessionManager
	Transport   transport.Transport
	AgentID     sessionrt.ActorID
	Conversations *ConversationSessionMap
	Logger      *log.Logger
}

type DMPipeline struct {
	manager       sessionrt.SessionManager
	transport     transport.Transport
	agentID       sessionrt.ActorID
	conversations *ConversationSessionMap
	logger        *log.Logger

	setupMu     sync.Mutex
	subscribedMu sync.Mutex
	subscribed map[sessionrt.SessionID]struct{}
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
	pipeline := &DMPipeline{
		manager:       opts.Manager,
		transport:     opts.Transport,
		agentID:       opts.AgentID,
		conversations: conversations,
		logger:        opts.Logger,
		subscribed:    map[sessionrt.SessionID]struct{}{},
	}
	pipeline.transport.SetInboundHandler(pipeline.HandleInbound)
	return pipeline, nil
}

func (p *DMPipeline) HandleInbound(ctx context.Context, inbound transport.InboundMessage) error {
	conversationID := strings.TrimSpace(inbound.ConversationID)
	if conversationID == "" {
		return nil
	}

	sessionID, err := p.resolveConversationSession(ctx, conversationID, inbound.SenderID)
	if err != nil {
		return err
	}

	if err := p.manager.SendEvent(ctx, sessionrt.Event{
		SessionID: sessionID,
		From:      matrixActorID(inbound.SenderID),
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: inbound.Text,
		},
	}); err != nil {
		return fmt.Errorf("send dm session event: %w", err)
	}
	return nil
}

func (p *DMPipeline) resolveConversationSession(ctx context.Context, conversationID, senderID string) (sessionrt.SessionID, error) {
	if existing, ok := p.conversations.Get(conversationID); ok {
		if err := p.ensureSubscription(conversationID, existing); err != nil {
			return "", err
		}
		return existing, nil
	}

	p.setupMu.Lock()
	defer p.setupMu.Unlock()

	if existing, ok := p.conversations.Get(conversationID); ok {
		if err := p.ensureSubscription(conversationID, existing); err != nil {
			return "", err
		}
		return existing, nil
	}

	created, err := p.manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: p.agentID, Type: sessionrt.ActorAgent},
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
	return created.ID, nil
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
			if msg.Role != sessionrt.RoleAgent || strings.TrimSpace(msg.Content) == "" {
				continue
			}
			if err := p.transport.SendMessage(context.Background(), transport.OutboundMessage{
				ConversationID: conversationID,
				Text:           msg.Content,
			}); err != nil && p.logger != nil {
				p.logger.Printf("send dm response failed: %v", err)
			}
		}
	}()
	return nil
}

func matrixActorID(sender string) sessionrt.ActorID {
	sender = strings.TrimSpace(sender)
	if sender == "" {
		return "matrix:unknown"
	}
	return sessionrt.ActorID("matrix:" + sender)
}
