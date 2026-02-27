package agentcore

import "context"

type MessageAttachment struct {
	Path     string `json:"path"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

type MessageSendRequest struct {
	SessionID   string              `json:"session_id"`
	Text        string              `json:"text,omitempty"`
	Attachments []MessageAttachment `json:"attachments,omitempty"`
}

type MessageSendResult struct {
	Sent            bool   `json:"sent"`
	ConversationID  string `json:"conversation_id,omitempty"`
	Text            string `json:"text,omitempty"`
	AttachmentCount int    `json:"attachment_count,omitempty"`
}

type MessageToolService interface {
	SendMessage(ctx context.Context, req MessageSendRequest) (MessageSendResult, error)
}
