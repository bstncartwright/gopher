package agentcore

import "context"

type ReactionSendRequest struct {
	SessionID string `json:"session_id"`
	Emoji     string `json:"emoji"`
}

type ReactionSendResult struct {
	Sent           bool   `json:"sent"`
	ConversationID string `json:"conversation_id,omitempty"`
	TargetEventID  string `json:"target_event_id,omitempty"`
	Emoji          string `json:"emoji,omitempty"`
}

type ReactionToolService interface {
	SendReaction(ctx context.Context, req ReactionSendRequest) (ReactionSendResult, error)
}
