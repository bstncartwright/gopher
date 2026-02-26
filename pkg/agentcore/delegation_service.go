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
	SessionID       string `json:"session_id"`
	ConversationID  string `json:"conversation_id"`
	SourceSessionID string `json:"source_session_id,omitempty"`
	SourceAgentID   string `json:"source_agent_id"`
	TargetAgentID   string `json:"target_agent_id"`
	SourceUserID    string `json:"source_user_id"`
	TargetUserID    string `json:"target_user_id"`
	HumanUserID     string `json:"human_user_id"`
	KickoffMessage  string `json:"kickoff_message"`
	Status          string `json:"status,omitempty"`
	Announcement    string `json:"announcement,omitempty"`
}

type DelegationListRequest struct {
	SourceSessionID string
	IncludeInactive bool
}

type DelegationListItem struct {
	SessionID       string `json:"session_id"`
	ConversationID  string `json:"conversation_id"`
	SourceSessionID string `json:"source_session_id"`
	SourceAgentID   string `json:"source_agent_id"`
	TargetAgentID   string `json:"target_agent_id"`
	Title           string `json:"title,omitempty"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	LastSeq         uint64 `json:"last_seq,omitempty"`
}

type DelegationKillRequest struct {
	SourceSessionID string
	DelegationID    string
}

type DelegationKillResult struct {
	SessionID       string `json:"session_id"`
	SourceSessionID string `json:"source_session_id,omitempty"`
	Status          string `json:"status"`
	Killed          bool   `json:"killed"`
}

type DelegationLogRequest struct {
	SourceSessionID string
	DelegationID    string
	Offset          int
	Limit           int
}

type DelegationLogEntry struct {
	Seq           uint64 `json:"seq"`
	Timestamp     string `json:"timestamp,omitempty"`
	Type          string `json:"type"`
	From          string `json:"from,omitempty"`
	Role          string `json:"role,omitempty"`
	Content       string `json:"content,omitempty"`
	TargetActorID string `json:"target_actor_id,omitempty"`
}

type DelegationLogResult struct {
	SessionID string               `json:"session_id"`
	Total     int                  `json:"total"`
	Offset    int                  `json:"offset"`
	Count     int                  `json:"count"`
	Entries   []DelegationLogEntry `json:"entries"`
}

type DelegationToolService interface {
	CreateDelegationSession(ctx context.Context, req DelegationCreateRequest) (DelegationSession, error)
	ListDelegationSessions(ctx context.Context, req DelegationListRequest) ([]DelegationListItem, error)
	KillDelegationSession(ctx context.Context, req DelegationKillRequest) (DelegationKillResult, error)
	GetDelegationLog(ctx context.Context, req DelegationLogRequest) (DelegationLogResult, error)
}
