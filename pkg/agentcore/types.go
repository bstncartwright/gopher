package agentcore

import (
	"context"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	"github.com/bstncartwright/gopher/pkg/memory"
)

type Message = ai.Message

type AgentConfig struct {
	AgentID                 string          `json:"agent_id"`
	Name                    string          `json:"name"`
	Role                    string          `json:"role"`
	ModelPolicy             string          `json:"model_policy"`
	Execution               ExecutionConfig `json:"execution"`
	EnabledTools            []string        `json:"enabled_tools"`
	DisableDefaultSearchMCP bool            `json:"disable_default_search_mcp"`
	SkillsPaths             []string        `json:"skills_paths"`
	MaxContextMessages      int             `json:"max_context_messages"`
	BootstrapMaxChars       int             `json:"bootstrap_max_chars"`
	BootstrapTotalMaxChars  int             `json:"bootstrap_total_max_chars"`
	UserTimezone            string          `json:"user_timezone"`
	TimeFormat              string          `json:"time_format"`
	Heartbeat               HeartbeatConfig `json:"heartbeat"`
}

type ExecutionConfig struct {
	RequiredCapabilities []string `json:"required_capabilities"`
}

type HeartbeatConfig struct {
	Every       string `json:"every"`
	Prompt      string `json:"prompt"`
	AckMaxChars int    `json:"ack_max_chars"`
}

type AgentHeartbeat struct {
	Enabled     bool
	Every       time.Duration
	Prompt      string
	AckMaxChars int
}

type NetworkPolicy struct {
	Enabled      bool     `json:"enabled"`
	AllowDomains []string `json:"allow_domains"`
}

type BudgetPolicy struct {
	MaxTokensPerSession int `json:"max_tokens_per_session"`
}

type AgentPolicies struct {
	FSRoots           []string            `json:"fs_roots"`
	AllowCrossAgentFS bool                `json:"allow_cross_agent_fs"`
	CanShell          bool                `json:"can_shell"`
	ShellAllowlist    []string            `json:"shell_allowlist"`
	Network           NetworkPolicy       `json:"network"`
	Budget            BudgetPolicy        `json:"budget"`
	ApplyPatchEnabled bool                `json:"apply_patch_enabled"`
	LoopDetection     LoopDetectionConfig `json:"loop_detection"`
}

type Agent struct {
	ID        string
	Name      string
	Role      string
	Workspace string
	Config    AgentConfig
	Policies  AgentPolicies

	Tools          ToolRegistry
	Memory         MemoryStore
	LongTermMemory memory.MemoryManager
	Assembler      ctxbundle.Assembler
	Logger         EventLogger
	Provider       AIProvider
	Processes      *ProcessManager
	Cron           CronToolService
	Delegation     DelegationToolService
	Heartbeat      AgentHeartbeat
	KnownAgents    []string

	skills         []Skill
	model          ai.Model
	allowedFSRoots []string
}

type Skill struct {
	Name                   string
	Description            string
	Location               string
	BaseDir                string
	Instruction            string
	DisableModelInvocation bool
}

type Session struct {
	ID           string
	Messages     []Message
	WorkingState map[string]any
}

type Attachment struct {
	Name     string
	MIMEType string
	Text     string
	Data     []byte
}

type TurnInput struct {
	UserMessage string
	Attachments []Attachment
	PromptMode  PromptMode
}

type TurnResult struct {
	FinalText string
	Events    []Event
}

type EventType string

const (
	EventTypeAgentDelta EventType = "agent.delta"
	EventTypeAgentMsg   EventType = "agent.message"
	EventTypeToolCall   EventType = "tool.call"
	EventTypeToolResult EventType = "tool.result"
	EventTypeError      EventType = "error"
)

const (
	DefaultContextWindow = 40
	MaxToolRounds        = 8
)

type PromptMode string

const (
	PromptModeFull    PromptMode = "full"
	PromptModeMinimal PromptMode = "minimal"
	PromptModeNone    PromptMode = "none"
)

type Event struct {
	TS        string    `json:"ts"`
	SessionID string    `json:"session_id"`
	AgentID   string    `json:"agent_id"`
	Type      EventType `json:"type"`
	Payload   any       `json:"payload"`
}

type MemoryStore interface {
	LoadWorking() (map[string]any, error)
	SaveWorking(map[string]any) error
}

type EventLogger interface {
	Append(Event) error
}

type AIProvider interface {
	Stream(model ai.Model, conversation ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream
}

type Tool interface {
	Name() string
	Schema() ToolSchema
	Run(ctx context.Context, input ToolInput) (ToolOutput, error)
}

type ToolSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolInput struct {
	Agent   *Agent
	Session *Session
	Args    map[string]any
}

type ToolOutput struct {
	Status ToolStatus `json:"status"`
	Result any        `json:"result,omitempty"`
}

type ToolRegistry interface {
	Schemas() []ToolSchema
	Get(name string) (Tool, bool)
}
