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
	AgentID                 string                  `json:"agent_id"`
	Name                    string                  `json:"name"`
	Role                    string                  `json:"role"`
	ModelPolicy             string                  `json:"model_policy"`
	Execution               ExecutionConfig         `json:"execution"`
	EnabledTools            []string                `json:"enabled_tools"`
	DisableDefaultSearchMCP bool                    `json:"disable_default_search_mcp"`
	SkillsPaths             []string                `json:"skills_paths"`
	MaxContextMessages      int                     `json:"max_context_messages"`
	BootstrapMaxChars       int                     `json:"bootstrap_max_chars"`
	BootstrapTotalMaxChars  int                     `json:"bootstrap_total_max_chars"`
	UserTimezone            string                  `json:"user_timezone"`
	TimeFormat              string                  `json:"time_format"`
	Heartbeat               HeartbeatConfig         `json:"heartbeat"`
	ContextManagement       ContextManagementConfig `json:"context_management,omitempty"`
}

type ContextManagementConfig struct {
	EnablePruning       *bool `json:"enable_pruning,omitempty"`
	EnableCompaction    *bool `json:"enable_compaction,omitempty"`
	EnableOverflowRetry *bool `json:"enable_overflow_retry,omitempty"`

	Mode                       string `json:"mode,omitempty"`
	OverflowRetryLimit         int    `json:"overflow_retry_limit,omitempty"`
	ReserveMinTokens           int    `json:"reserve_min_tokens,omitempty"`
	ModelCompactionSummary     *bool  `json:"model_compaction_summary,omitempty"`
	CompactionSummaryTimeoutMS int    `json:"compaction_summary_timeout_ms,omitempty"`
	CompactionChunkTokenTarget int    `json:"compaction_chunk_token_target,omitempty"`
	ToolResultContextMaxChars  int    `json:"tool_result_context_max_chars,omitempty"`
	ToolResultContextHeadChars int    `json:"tool_result_context_head_chars,omitempty"`
	ToolResultContextTailChars int    `json:"tool_result_context_tail_chars,omitempty"`
	RecentToolResultChars      int    `json:"recent_tool_result_chars,omitempty"`
	HistoricalToolResultChars  int    `json:"historical_tool_result_chars,omitempty"`
}

const (
	defaultContextMode                    = "safeguard"
	defaultOverflowRetryLimit             = 3
	defaultReserveMinTokens               = 20000
	defaultCompactionSummaryTimeoutMS     = 12000
	defaultCompactionChunkTokenTarget     = 1800
	defaultToolResultContextMaxChars      = 12000
	defaultToolResultContextHeadChars     = 8000
	defaultToolResultContextTailChars     = 3000
	defaultRecentToolResultChars          = 2400
	defaultHistoricalToolResultChars      = 240
	maxAllowedOverflowRetryLimit          = 6
	maxAllowedCompactionSummaryTimeoutMS  = 120000
	maxAllowedCompactionChunkTokenTarget  = 12000
	maxAllowedToolResultContextMaxChars   = 200000
	maxAllowedToolResultContextSliceChars = 120000
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

func (c ContextManagementConfig) ToolResultContextMaxCharsValue() int {
	maxChars := c.ToolResultContextMaxChars
	if maxChars <= 0 {
		maxChars = defaultToolResultContextMaxChars
	}
	if maxChars > maxAllowedToolResultContextMaxChars {
		maxChars = maxAllowedToolResultContextMaxChars
	}
	return maxChars
}

func (c ContextManagementConfig) ToolResultContextHeadCharsValue() int {
	headChars := c.ToolResultContextHeadChars
	if headChars <= 0 {
		headChars = defaultToolResultContextHeadChars
	}
	if headChars > maxAllowedToolResultContextSliceChars {
		headChars = maxAllowedToolResultContextSliceChars
	}
	return headChars
}

func (c ContextManagementConfig) ToolResultContextTailCharsValue() int {
	tailChars := c.ToolResultContextTailChars
	if tailChars <= 0 {
		tailChars = defaultToolResultContextTailChars
	}
	if tailChars > maxAllowedToolResultContextSliceChars {
		tailChars = maxAllowedToolResultContextSliceChars
	}
	return tailChars
}

func (c ContextManagementConfig) RecentToolResultCharsValue() int {
	recentChars := c.RecentToolResultChars
	if recentChars <= 0 {
		recentChars = defaultRecentToolResultChars
	}
	return recentChars
}

func (c ContextManagementConfig) HistoricalToolResultCharsValue() int {
	historicalChars := c.HistoricalToolResultChars
	if historicalChars <= 0 {
		historicalChars = defaultHistoricalToolResultChars
	}
	return historicalChars
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
	BlockDomains []string `json:"block_domains"`
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
