package session

import "time"

type SessionID string

type EventID string

type ActorID string

const SystemActorID ActorID = "system"

type Session struct {
	ID           SessionID               `json:"id"`
	DisplayName  string                  `json:"display_name,omitempty"`
	Participants map[ActorID]Participant `json:"participants"`
	CreatedAt    time.Time               `json:"created_at"`
	Status       SessionStatus           `json:"status"`
}

type SessionStatus int

const (
	SessionActive SessionStatus = iota
	SessionPaused
	SessionCompleted
	SessionFailed
)

func (s SessionStatus) IsTerminal() bool {
	return s == SessionPaused || s == SessionCompleted || s == SessionFailed
}

type Participant struct {
	ID       ActorID           `json:"id"`
	Type     ActorType         `json:"type"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type ActorType int

const (
	ActorAgent ActorType = iota
	ActorHuman
	ActorSystem
)

func validActorType(t ActorType) bool {
	return t == ActorAgent || t == ActorHuman || t == ActorSystem
}

type Event struct {
	ID        EventID   `json:"id"`
	SessionID SessionID `json:"session_id"`
	From      ActorID   `json:"from"`
	Type      EventType `json:"type"`
	Payload   any       `json:"payload"`
	Timestamp time.Time `json:"timestamp"`
	Seq       uint64    `json:"seq"`
}

type Attachment struct {
	Path     string `json:"path,omitempty"`
	Name     string `json:"name,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Text     string `json:"text,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

type EventType string

const (
	EventMessage            EventType = "message"
	EventAgentStart         EventType = "agent_start"
	EventAgentStop          EventType = "agent_stop"
	EventAgentDelta         EventType = "agent_delta"
	EventAgentThinkingDelta EventType = "agent_thinking_delta"
	EventToolCall           EventType = "tool_call"
	EventToolResult         EventType = "tool_result"
	EventStatePatch         EventType = "state_patch"
	EventControl            EventType = "control"
	EventError              EventType = "error"
)

type Message struct {
	Role          Role         `json:"role"`
	Content       string       `json:"content"`
	TargetActorID ActorID      `json:"target_actor_id,omitempty"`
	Attachments   []Attachment `json:"attachments,omitempty"`
}

type Role string

const (
	RoleAgent  Role = "agent"
	RoleUser   Role = "user"
	RoleSystem Role = "system"
)

type CreateSessionOptions struct {
	Participants []Participant
	DisplayName  string
}
