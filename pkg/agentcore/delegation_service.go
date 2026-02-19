package agentcore

import "context"

type DelegationCreateRequest struct {
	SourceSessionID string
	SourceAgentID   string
	TargetAgentID   string
	Message         string
	Title           string
}

type DelegationSession struct {
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	SourceAgentID  string `json:"source_agent_id"`
	TargetAgentID  string `json:"target_agent_id"`
	SourceUserID   string `json:"source_user_id"`
	TargetUserID   string `json:"target_user_id"`
	HumanUserID    string `json:"human_user_id"`
	KickoffMessage string `json:"kickoff_message"`
}

type DelegationToolService interface {
	CreateDelegationSession(ctx context.Context, req DelegationCreateRequest) (DelegationSession, error)
}
