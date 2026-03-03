package agentcore

import (
	"context"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	"github.com/bstncartwright/gopher/pkg/memory"
)

type Message = ai.Message

type AgentConfig struct {
	AgentID                 string                  `json:"agent_id" toml:"agent_id"`
	Name                    string                  `json:"name" toml:"name"`
	Role                    string                  `json:"role" toml:"role"`
	ModelPolicy             string                  `json:"model_policy" toml:"model_policy"`
	ReasoningLevel          string                  `json:"reasoning_level,omitempty" toml:"reasoning_level,omitempty"`
	Execution               ExecutionConfig         `json:"execution" toml:"execution"`
	Policies                *AgentPolicies          `json:"policies,omitempty" toml:"policies,omitempty"`
	EnabledTools            []string                `json:"enabled_tools" toml:"enabled_tools"`
	DisableDefaultSearchMCP bool                    `json:"disable_default_search_mcp" toml:"disable_default_search_mcp"`
	SkillsPaths             []string                `json:"skills_paths" toml:"skills_paths"`
	MaxContextMessages      int                     `json:"max_context_messages" toml:"max_context_messages"`
	BootstrapMaxChars       int                     `json:"bootstrap_max_chars" toml:"bootstrap_max_chars"`
	BootstrapTotalMaxChars  int                     `json:"bootstrap_total_max_chars" toml:"bootstrap_total_max_chars"`
	UserTimezone            string                  `json:"user_timezone" toml:"user_timezone"`
	TimeFormat              string                  `json:"time_format" toml:"time_format"`
	Heartbeat               HeartbeatConfig         `json:"heartbeat" toml:"heartbeat"`
	ContextManagement       ContextManagementConfig `json:"context_management,omitempty" toml:"context_management,omitempty"`
}

type ContextManagementConfig struct {
	EnablePruning       *bool `json:"enable_pruning,omitempty" toml:"enable_pruning,omitempty"`
	EnableCompaction    *bool `json:"enable_compaction,omitempty" toml:"enable_compaction,omitempty"`
	EnableOverflowRetry *bool `json:"enable_overflow_retry,omitempty" toml:"enable_overflow_retry,omitempty"`

	Mode                       string `json:"mode,omitempty" toml:"mode,omitempty"`
	OverflowRetryLimit         int    `json:"overflow_retry_limit,omitempty" toml:"overflow_retry_limit,omitempty"`
	ReserveMinTokens           int    `json:"reserve_min_tokens,omitempty" toml:"reserve_min_tokens,omitempty"`
	ModelCompactionSummary     *bool  `json:"model_compaction_summary,omitempty" toml:"model_compaction_summary,omitempty"`
	CompactionSummaryTimeoutMS int    `json:"compaction_summary_timeout_ms,omitempty" toml:"compaction_summary_timeout_ms,omitempty"`
	CompactionChunkTokenTarget int    `json:"compaction_chunk_token_target,omitempty" toml:"compaction_chunk_token_target,omitempty"`
}

const (
	defaultContextMode                   = "safeguard"
	defaultOverflowRetryLimit            = 3
	defaultReserveMinTokens              = 20000
	defaultCompactionSummaryTimeoutMS    = 12000
	defaultCompactionChunkTokenTarget    = 1800
	maxAllowedOverflowRetryLimit         = 6
	maxAllowedCompactionSummaryTimeoutMS = 120000
	maxAllowedCompactionChunkTokenTarget = 12000
)

func (c ContextManagementConfig) PruningEnabled() bool {
	return c.EnablePruning == nil || *c.EnablePruning
}

func (c ContextManagementConfig) CompactionEnabled() bool {
	return c.EnableCompaction == nil || *c.EnableCompaction
}

func (c ContextManagementConfig) OverflowRetryEnabled() bool {
	return c.EnableOverflowRetry == nil || *c.EnableOverflowRetry
}

func (c ContextManagementConfig) ModeValue() string {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	if mode == "" {
		return defaultContextMode
	}
	switch mode {
	case "safeguard":
		return mode
	default:
		return defaultContextMode
	}
}

func (c ContextManagementConfig) OverflowRetryLimitValue() int {
	limit := c.OverflowRetryLimit
	if limit <= 0 {
		limit = defaultOverflowRetryLimit
	}
	if limit > maxAllowedOverflowRetryLimit {
		limit = maxAllowedOverflowRetryLimit
	}
	return limit
}

func (c ContextManagementConfig) ReserveMinTokensValue() int {
	if c.ReserveMinTokens <= 0 {
		return defaultReserveMinTokens
	}
	return c.ReserveMinTokens
}

func (c ContextManagementConfig) ModelCompactionSummaryEnabled() bool {
	return c.ModelCompactionSummary == nil || *c.ModelCompactionSummary
}

func (c ContextManagementConfig) CompactionSummaryTimeoutMSValue() int {
	timeout := c.CompactionSummaryTimeoutMS
	if timeout <= 0 {
		timeout = defaultCompactionSummaryTimeoutMS
	}
	if timeout > maxAllowedCompactionSummaryTimeoutMS {
		timeout = maxAllowedCompactionSummaryTimeoutMS
	}
	return timeout
}

func (c ContextManagementConfig) CompactionChunkTokenTargetValue() int {
	chunkTokens := c.CompactionChunkTokenTarget
	if chunkTokens <= 0 {
		chunkTokens = defaultCompactionChunkTokenTarget
	}
	if chunkTokens > maxAllowedCompactionChunkTokenTarget {
		chunkTokens = maxAllowedCompactionChunkTokenTarget
	}
	return chunkTokens
}

func (c AgentConfig) ReasoningLevelValue() ai.ThinkingLevel {
	return normalizeReasoningLevel(c.ReasoningLevel)
}

func normalizeReasoningLevel(raw string) ai.ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return ""
	case string(ai.ThinkingMinimal), "min":
		return ai.ThinkingMinimal
	case string(ai.ThinkingLow):
		return ai.ThinkingLow
	case string(ai.ThinkingMedium), "med":
		return ai.ThinkingMedium
	case string(ai.ThinkingHigh):
		return ai.ThinkingHigh
	case string(ai.ThinkingXHigh), "x-high", "x_high", "x high":
		return ai.ThinkingXHigh
	case "off", "none", "disabled", "false", "0":
		return ""
	default:
		return ""
	}
}

type ExecutionConfig struct {
	RequiredCapabilities []string `json:"required_capabilities" toml:"required_capabilities"`
}

type HeartbeatConfig struct {
	Every       string                      `json:"every" toml:"every"`
	Prompt      string                      `json:"prompt" toml:"prompt"`
	AckMaxChars int                         `json:"ack_max_chars" toml:"ack_max_chars"`
	Session     string                      `json:"session,omitempty" toml:"session,omitempty"`
	ActiveHours *HeartbeatActiveHoursConfig `json:"active_hours,omitempty" toml:"active_hours,omitempty"`
}

type HeartbeatActiveHoursConfig struct {
	Start    string `json:"start" toml:"start"`
	End      string `json:"end" toml:"end"`
	Timezone string `json:"timezone,omitempty" toml:"timezone,omitempty"`
}

type AgentHeartbeat struct {
	Enabled     bool
	Every       time.Duration
	Prompt      string
	AckMaxChars int
	SessionID   string
	ActiveHours AgentHeartbeatActiveHours
}

type AgentHeartbeatActiveHours struct {
	Enabled     bool
	Start       string
	End         string
	StartMinute int
	EndMinute   int
	Timezone    string
	Location    *time.Location
}

type NetworkPolicy struct {
	Enabled      bool     `json:"enabled" toml:"enabled"`
	AllowDomains []string `json:"allow_domains" toml:"allow_domains"`
	BlockDomains []string `json:"block_domains" toml:"block_domains"`
}

type BudgetPolicy struct {
	MaxTokensPerSession int `json:"max_tokens_per_session" toml:"max_tokens_per_session"`
}

type AgentPolicies struct {
	FSRoots           []string            `json:"fs_roots" toml:"fs_roots"`
	AllowCrossAgentFS bool                `json:"allow_cross_agent_fs" toml:"allow_cross_agent_fs"`
	CanShell          bool                `json:"can_shell" toml:"can_shell"`
	ShellAllowlist    []string            `json:"shell_allowlist" toml:"shell_allowlist"`
	Network           NetworkPolicy       `json:"network" toml:"network"`
	Budget            BudgetPolicy        `json:"budget" toml:"budget"`
	ApplyPatchEnabled bool                `json:"apply_patch_enabled" toml:"apply_patch_enabled"`
	LoopDetection     LoopDetectionConfig `json:"loop_detection" toml:"loop_detection"`
}

type Agent struct {
	ID        string
	Name      string
	Role      string
	Workspace string
	Config    AgentConfig
	Policies  AgentPolicies

	Tools                 ToolRegistry
	Memory                MemoryStore
	LongTermMemory        memory.MemoryManager
	Assembler             ctxbundle.Assembler
	Logger                EventLogger
	Provider              AIProvider
	Processes             *ProcessManager
	Cron                  CronToolService
	Delegation            DelegationToolService
	HeartbeatService      HeartbeatToolService
	MessageService        MessageToolService
	ReactionService       ReactionToolService
	Heartbeat             AgentHeartbeat
	KnownAgents           []string
	CaptureThinkingDeltas bool
	SessionMemoryFlusher  SessionMemoryFlusher

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
	ID                     string
	Messages               []Message
	WorkingState           map[string]any
	CompactionSummaries    []string
	LastContextDiagnostics ctxbundle.ContextDiagnostics
}

type SessionMemoryFlusher interface {
	FlushSession(ctx context.Context, sessionID string) error
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
	EventTypeAgentDelta         EventType = "agent.delta"
	EventTypeAgentThinkingDelta EventType = "agent.thinking_delta"
	EventTypeAgentMsg           EventType = "agent.message"
	EventTypeToolCall           EventType = "tool.call"
	EventTypeToolResult         EventType = "tool.result"
	EventTypeError              EventType = "error"
)

const (
	DefaultContextWindow = 40
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
