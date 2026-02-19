package agentcore

import "context"

type HeartbeatState struct {
	Enabled      bool   `json:"enabled"`
	Every        string `json:"every,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	AckMaxChars  int    `json:"ack_max_chars,omitempty"`
	UserTimezone string `json:"user_timezone,omitempty"`
}

type HeartbeatSetRequest struct {
	AgentID      string
	Every        string
	Prompt       *string
	AckMaxChars  *int
	UserTimezone *string
}

type HeartbeatToolService interface {
	GetHeartbeat(ctx context.Context, agentID string) (HeartbeatState, error)
	SetHeartbeat(ctx context.Context, req HeartbeatSetRequest) (HeartbeatState, error)
	DisableHeartbeat(ctx context.Context, agentID string) (HeartbeatState, error)
}
