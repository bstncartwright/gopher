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

type MessageDraftRequest struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text,omitempty"`
	DraftID   int64  `json:"draft_id,omitempty"`
}

type MessageSendResult struct {
	Sent            bool   `json:"sent"`
	ConversationID  string `json:"conversation_id,omitempty"`
	Text            string `json:"text,omitempty"`
	AttachmentCount int    `json:"attachment_count,omitempty"`
}

type MessageDraftResult struct {
	Drafted        bool   `json:"drafted"`
	ConversationID string `json:"conversation_id,omitempty"`
	Text           string `json:"text,omitempty"`
	DraftID        int64  `json:"draft_id,omitempty"`
}

type MessageToolService interface {
	SendMessage(ctx context.Context, req MessageSendRequest) (MessageSendResult, error)
}

type MessageToolStreamingService interface {
	SendMessageDraft(ctx context.Context, req MessageDraftRequest) (MessageDraftResult, error)
}
