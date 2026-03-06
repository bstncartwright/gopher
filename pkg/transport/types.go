package transport

import "context"

type InboundAttachment struct {
	Path     string
	Name     string
	MIMEType string
	Text     string
	Data     []byte
}

type InboundMessage struct {
	ConversationID   string
	ConversationName string
	SenderID         string
	SenderManaged    bool
	RecipientID      string
	EventID          string
	Text             string
	Attachments      []InboundAttachment
}

type OutboundMessage struct {
	ConversationID    string
	SenderID          string
	Text              string
	ThreadRootEventID string
	Attachments       []OutboundAttachment
}

type OutboundSendResult struct {
	EventID string
}

type OutboundAttachment struct {
	Path     string
	Name     string
	MIMEType string
}

type OutboundReaction struct {
	ConversationID string
	SenderID       string
	TargetEventID  string
	Emoji          string
}

type InboundHandler func(ctx context.Context, message InboundMessage) error

type Transport interface {
	Start(ctx context.Context) error
	Stop() error
	SetInboundHandler(handler InboundHandler)
	SendMessage(ctx context.Context, message OutboundMessage) error
	SendTyping(ctx context.Context, conversationID string, typing bool) error
}

type ReactionSender interface {
	SendReaction(ctx context.Context, reaction OutboundReaction) error
}
