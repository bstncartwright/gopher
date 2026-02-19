package transport

import "context"

type InboundMessage struct {
	ConversationID string
	SenderID       string
	SenderManaged  bool
	RecipientID    string
	EventID        string
	Text           string
}

type OutboundMessage struct {
	ConversationID string
	SenderID       string
	Text           string
}

type InboundHandler func(ctx context.Context, message InboundMessage) error

type Transport interface {
	Start(ctx context.Context) error
	Stop() error
	SetInboundHandler(handler InboundHandler)
	SendMessage(ctx context.Context, message OutboundMessage) error
	SendTyping(ctx context.Context, conversationID string, typing bool) error
}
