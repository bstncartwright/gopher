package a2a

import (
	"fmt"
	"strings"
)

type AgentCard struct {
	Name               string           `json:"name,omitempty"`
	Description        string           `json:"description,omitempty"`
	URL                string           `json:"url,omitempty"`
	Version            string           `json:"version,omitempty"`
	ProtocolVersions   []string         `json:"protocolVersions,omitempty"`
	PreferredTransport string           `json:"preferredTransport,omitempty"`
	Skills             []AgentSkill     `json:"skills,omitempty"`
	Interfaces         []AgentInterface `json:"interfaces,omitempty"`
}

type AgentSkill struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type AgentInterface struct {
	URL             string `json:"url,omitempty"`
	ProtocolBinding string `json:"protocolBinding,omitempty"`
	Transport       string `json:"transport,omitempty"`
}

func (c AgentCard) ResolveHTTPJSONEndpoint() (string, bool) {
	for _, iface := range c.Interfaces {
		binding := strings.ToUpper(strings.TrimSpace(iface.ProtocolBinding))
		if binding != "" && binding != "HTTP+JSON" {
			continue
		}
		url := strings.TrimSpace(iface.URL)
		if url == "" {
			continue
		}
		return url, true
	}
	if url := strings.TrimSpace(c.URL); url != "" {
		return url, true
	}
	return "", false
}

func (c AgentCard) ValidateHTTPJSON() error {
	if _, ok := c.ResolveHTTPJSONEndpoint(); ok {
		return nil
	}
	return fmt.Errorf("agent card does not expose an HTTP+JSON interface")
}

type Message struct {
	Role      string `json:"role,omitempty"`
	MessageID string `json:"messageId,omitempty"`
	Parts     []Part `json:"parts,omitempty"`
}

type Part struct {
	Kind     string         `json:"kind,omitempty"`
	Type     string         `json:"type,omitempty"`
	Text     string         `json:"text,omitempty"`
	Data     any            `json:"data,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type Artifact struct {
	ArtifactID  string         `json:"artifactId,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type TaskStatus string

const (
	TaskStateSubmitted     TaskStatus = "submitted"
	TaskStateWorking       TaskStatus = "working"
	TaskStateCompleted     TaskStatus = "completed"
	TaskStateFailed        TaskStatus = "failed"
	TaskStateCanceled      TaskStatus = "canceled"
	TaskStateInputRequired TaskStatus = "input_required"
)

type Task struct {
	ID        string         `json:"id,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status,omitempty"`
	State     string         `json:"state,omitempty"`
	Message   *Message       `json:"message,omitempty"`
	Messages  []Message      `json:"messages,omitempty"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func (t Task) NormalizedID() string {
	if id := strings.TrimSpace(t.TaskID); id != "" {
		return id
	}
	return strings.TrimSpace(t.ID)
}

func (t Task) NormalizedStatus() TaskStatus {
	status := strings.TrimSpace(strings.ToLower(string(t.Status)))
	if status == "" {
		status = strings.TrimSpace(strings.ToLower(t.State))
	}
	switch TaskStatus(status) {
	case TaskStateSubmitted, TaskStateWorking, TaskStateCompleted, TaskStateFailed, TaskStateCanceled, TaskStateInputRequired:
		return TaskStatus(status)
	default:
		return TaskStatus(status)
	}
}

func (t Task) Terminal() bool {
	switch t.NormalizedStatus() {
	case TaskStateCompleted, TaskStateFailed, TaskStateCanceled:
		return true
	default:
		return false
	}
}

func (t Task) LatestText() string {
	if t.Message != nil {
		if text := messageText(*t.Message); text != "" {
			return text
		}
	}
	for i := len(t.Messages) - 1; i >= 0; i-- {
		if text := messageText(t.Messages[i]); text != "" {
			return text
		}
	}
	for i := len(t.Artifacts) - 1; i >= 0; i-- {
		if text := partsText(t.Artifacts[i].Parts); text != "" {
			return text
		}
	}
	return ""
}

func messageText(message Message) string {
	return partsText(message.Parts)
}

func partsText(parts []Part) string {
	chunks := make([]string, 0, len(parts))
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		chunks = append(chunks, text)
	}
	return strings.TrimSpace(strings.Join(chunks, "\n"))
}

type Remote struct {
	BaseURL                   string
	CardURL                   string
	Headers                   map[string]string
	AllowInsecureTLS          bool
	CompatLegacyWellKnownPath bool
}

type MessageSendRequest struct {
	Message   Message `json:"message"`
	TaskID    string  `json:"taskId,omitempty"`
	ContextID string  `json:"contextId,omitempty"`
}
