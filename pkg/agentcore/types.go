package agentcore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	"github.com/bstncartwright/gopher/pkg/memory"
	memfiles "github.com/bstncartwright/gopher/pkg/memory/files"
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
	Memory                  MemoryConfig            `json:"memory,omitempty" toml:"memory,omitempty"`
	MemorySearch            MemorySearchConfig      `json:"memory_search,omitempty" toml:"memory_search,omitempty"`
	ContextManagement       ContextManagementConfig `json:"context_management,omitempty" toml:"context_management,omitempty"`
}

type ContextManagementConfig struct {
	EnablePruning       *bool `json:"enable_pruning,omitempty" toml:"enable_pruning,omitempty"`
	EnableCompaction    *bool `json:"enable_compaction,omitempty" toml:"enable_compaction,omitempty"`
	EnableOverflowRetry *bool `json:"enable_overflow_retry,omitempty" toml:"enable_overflow_retry,omitempty"`

	Mode                       string                      `json:"mode,omitempty" toml:"mode,omitempty"`
	OverflowRetryLimit         int                         `json:"overflow_retry_limit,omitempty" toml:"overflow_retry_limit,omitempty"`
	ReserveMinTokens           int                         `json:"reserve_min_tokens,omitempty" toml:"reserve_min_tokens,omitempty"`
	ModelCompactionSummary     *bool                       `json:"model_compaction_summary,omitempty" toml:"model_compaction_summary,omitempty"`
	CompactionSummaryTimeoutMS int                         `json:"compaction_summary_timeout_ms,omitempty" toml:"compaction_summary_timeout_ms,omitempty"`
	CompactionChunkTokenTarget int                         `json:"compaction_chunk_token_target,omitempty" toml:"compaction_chunk_token_target,omitempty"`
	RetrievedMemoryLanePercent int                         `json:"retrieved_memory_lane_percent,omitempty" toml:"retrieved_memory_lane_percent,omitempty"`
	MemoryFlush                CompactionMemoryFlushConfig `json:"memory_flush,omitempty" toml:"memory_flush,omitempty"`
}

type CompactionMemoryFlushConfig struct {
	Enabled                   *bool  `json:"enabled,omitempty" toml:"enabled,omitempty"`
	SoftThresholdTokens       int    `json:"soft_threshold_tokens,omitempty" toml:"soft_threshold_tokens,omitempty"`
	Prompt                    string `json:"prompt,omitempty" toml:"prompt,omitempty"`
	SystemPrompt              string `json:"system_prompt,omitempty" toml:"system_prompt,omitempty"`
	ForceFlushTranscriptBytes int64  `json:"force_flush_transcript_bytes,omitempty" toml:"force_flush_transcript_bytes,omitempty"`
}

type MemoryConfig struct {
	Enabled   *bool    `json:"enabled,omitempty" toml:"enabled,omitempty"`
	Sources   []string `json:"sources,omitempty" toml:"sources,omitempty"`
	Citations string   `json:"citations,omitempty" toml:"citations,omitempty"`
}

type MemorySearchConfig struct {
	Enabled             *bool                     `json:"enabled,omitempty" toml:"enabled,omitempty"`
	Sources             []string                  `json:"sources,omitempty" toml:"sources,omitempty"`
	MaxResults          int                       `json:"max_results,omitempty" toml:"max_results,omitempty"`
	MinScore            float64                   `json:"min_score,omitempty" toml:"min_score,omitempty"`
	Hybrid              MemorySearchHybridConfig  `json:"hybrid,omitempty" toml:"hybrid,omitempty"`
	MMR                 MemorySearchMMRConfig     `json:"mmr,omitempty" toml:"mmr,omitempty"`
	TemporalDecay       MemoryTemporalDecayConfig `json:"temporal_decay,omitempty" toml:"temporal_decay,omitempty"`
	ChunkTokens         int                       `json:"chunk_tokens,omitempty" toml:"chunk_tokens,omitempty"`
	ChunkOverlap        int                       `json:"chunk_overlap,omitempty" toml:"chunk_overlap,omitempty"`
	EmbeddingProvider   string                    `json:"embedding_provider,omitempty" toml:"embedding_provider,omitempty"`
	EmbeddingModel      string                    `json:"embedding_model,omitempty" toml:"embedding_model,omitempty"`
	EmbeddingBaseURL    string                    `json:"embedding_base_url,omitempty" toml:"embedding_base_url,omitempty"`
	EmbeddingTimeoutMS  int                       `json:"embedding_timeout_ms,omitempty" toml:"embedding_timeout_ms,omitempty"`
	EmbeddingMaxBatch   int                       `json:"embedding_max_batch,omitempty" toml:"embedding_max_batch,omitempty"`
	EmbeddingMaxChars   int                       `json:"embedding_max_chars,omitempty" toml:"embedding_max_chars,omitempty"`
	EmbeddingRetries    int                       `json:"embedding_retries,omitempty" toml:"embedding_retries,omitempty"`
	EmbeddingConcurrent int                       `json:"embedding_concurrency,omitempty" toml:"embedding_concurrency,omitempty"`
}

type MemorySearchHybridConfig struct {
	Enabled             *bool   `json:"enabled,omitempty" toml:"enabled,omitempty"`
	VectorWeight        float64 `json:"vector_weight,omitempty" toml:"vector_weight,omitempty"`
	TextWeight          float64 `json:"text_weight,omitempty" toml:"text_weight,omitempty"`
	CandidateMultiplier int     `json:"candidate_multiplier,omitempty" toml:"candidate_multiplier,omitempty"`
}

type MemorySearchMMRConfig struct {
	Enabled *bool   `json:"enabled,omitempty" toml:"enabled,omitempty"`
	Lambda  float64 `json:"lambda,omitempty" toml:"lambda,omitempty"`
}

type MemoryTemporalDecayConfig struct {
	Enabled      *bool `json:"enabled,omitempty" toml:"enabled,omitempty"`
	HalfLifeDays int   `json:"half_life_days,omitempty" toml:"half_life_days,omitempty"`
}

const (
	defaultContextMode                   = "safeguard"
	defaultOverflowRetryLimit            = 3
	defaultReserveMinTokens              = 20000
	defaultCompactionSummaryTimeoutMS    = 12000
	defaultCompactionChunkTokenTarget    = 1800
	defaultRetrievedMemoryLanePercent    = 20
	defaultMemoryCitationMode            = "auto"
	defaultMemorySearchMaxResults        = 6
	defaultMemorySearchMinScore          = 0.35
	defaultMemorySearchVectorWeight      = 0.7
	defaultMemorySearchTextWeight        = 0.3
	defaultMemorySearchCandidateMultiple = 4
	defaultMemorySearchMMRLambda         = 0.7
	defaultMemorySearchHalfLifeDays      = 30
	defaultMemoryChunkTokens             = 400
	defaultMemoryChunkOverlap            = 80
	defaultMemoryEmbeddingTimeoutMS      = 12000
	defaultMemoryEmbeddingMaxBatch       = 16
	defaultMemoryEmbeddingMaxChars       = 6000
	defaultMemoryEmbeddingRetries        = 2
	defaultMemoryEmbeddingConcurrency    = 4
	defaultFlushSoftThresholdTokens      = 4000
	defaultFlushPrompt                   = "Summarize durable facts from this conversation transcript and write concise memory notes. Reply NO_REPLY."
	defaultFlushSystemPrompt             = "You are running a silent pre-compaction memory flush. Extract durable facts, preferences, decisions, and recent progress. Respond with concise markdown bullet points or NO_REPLY."
	defaultForceFlushTranscriptBytes     = int64(2 << 20)
	maxAllowedOverflowRetryLimit         = 6
	maxAllowedCompactionSummaryTimeoutMS = 120000
	maxAllowedCompactionChunkTokenTarget = 12000
	maxRetrievedMemoryLanePercent        = 50
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

func (c ContextManagementConfig) RetrievedMemoryLanePercentValue() int {
	percent := c.RetrievedMemoryLanePercent
	if percent <= 0 {
		percent = defaultRetrievedMemoryLanePercent
	}
	if percent > maxRetrievedMemoryLanePercent {
		percent = maxRetrievedMemoryLanePercent
	}
	return percent
}

func (c CompactionMemoryFlushConfig) EnabledValue() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c CompactionMemoryFlushConfig) SoftThresholdTokensValue() int {
	if c.SoftThresholdTokens <= 0 {
		return defaultFlushSoftThresholdTokens
	}
	return c.SoftThresholdTokens
}

func (c CompactionMemoryFlushConfig) PromptValue() string {
	prompt := strings.TrimSpace(c.Prompt)
	if prompt == "" {
		return defaultFlushPrompt
	}
	return prompt
}

func (c CompactionMemoryFlushConfig) SystemPromptValue() string {
	prompt := strings.TrimSpace(c.SystemPrompt)
	if prompt == "" {
		return defaultFlushSystemPrompt
	}
	return prompt
}

func (c CompactionMemoryFlushConfig) ForceFlushTranscriptBytesValue() int64 {
	if c.ForceFlushTranscriptBytes <= 0 {
		return defaultForceFlushTranscriptBytes
	}
	return c.ForceFlushTranscriptBytes
}

func (c MemoryConfig) EnabledValue() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c MemoryConfig) SourcesValue() []string {
	if len(c.Sources) == 0 {
		return []string{"memory"}
	}
	out := make([]string, 0, len(c.Sources))
	seen := map[string]struct{}{}
	for _, source := range c.Sources {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "" {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		out = append(out, source)
	}
	if len(out) == 0 {
		return []string{"memory"}
	}
	return out
}

func (c MemoryConfig) CitationsModeValue() string {
	mode := strings.ToLower(strings.TrimSpace(c.Citations))
	switch mode {
	case "on", "off", "auto":
		return mode
	default:
		return defaultMemoryCitationMode
	}
}

func (c MemorySearchConfig) EnabledValue() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c MemorySearchConfig) SourcesValue() []string {
	if len(c.Sources) == 0 {
		return []string{"memory"}
	}
	out := make([]string, 0, len(c.Sources))
	seen := map[string]struct{}{}
	for _, source := range c.Sources {
		source = strings.ToLower(strings.TrimSpace(source))
		if source == "" {
			continue
		}
		if _, ok := seen[source]; ok {
			continue
		}
		seen[source] = struct{}{}
		out = append(out, source)
	}
	if len(out) == 0 {
		return []string{"memory"}
	}
	return out
}

func (c MemorySearchConfig) MaxResultsValue() int {
	if c.MaxResults <= 0 {
		return defaultMemorySearchMaxResults
	}
	if c.MaxResults > 32 {
		return 32
	}
	return c.MaxResults
}

func (c MemorySearchConfig) MinScoreValue() float64 {
	if c.MinScore <= 0 {
		return defaultMemorySearchMinScore
	}
	if c.MinScore > 1 {
		return 1
	}
	return c.MinScore
}

func (c MemorySearchHybridConfig) EnabledValue() bool {
	return c.Enabled == nil || *c.Enabled
}

func (c MemorySearchHybridConfig) VectorWeightValue() float64 {
	weight := c.VectorWeight
	if weight <= 0 {
		weight = defaultMemorySearchVectorWeight
	}
	return weight
}

func (c MemorySearchHybridConfig) TextWeightValue() float64 {
	weight := c.TextWeight
	if weight <= 0 {
		weight = defaultMemorySearchTextWeight
	}
	return weight
}

func (c MemorySearchHybridConfig) CandidateMultiplierValue() int {
	if c.CandidateMultiplier <= 0 {
		return defaultMemorySearchCandidateMultiple
	}
	if c.CandidateMultiplier > 12 {
		return 12
	}
	return c.CandidateMultiplier
}

func (c MemorySearchMMRConfig) EnabledValue() bool {
	return c.Enabled != nil && *c.Enabled
}

func (c MemorySearchMMRConfig) LambdaValue() float64 {
	if c.Lambda <= 0 || c.Lambda > 1 {
		return defaultMemorySearchMMRLambda
	}
	return c.Lambda
}

func (c MemoryTemporalDecayConfig) EnabledValue() bool {
	return c.Enabled != nil && *c.Enabled
}

func (c MemoryTemporalDecayConfig) HalfLifeDaysValue() int {
	if c.HalfLifeDays <= 0 {
		return defaultMemorySearchHalfLifeDays
	}
	return c.HalfLifeDays
}

func (c MemorySearchConfig) ChunkTokensValue() int {
	if c.ChunkTokens <= 0 {
		return defaultMemoryChunkTokens
	}
	return c.ChunkTokens
}

func (c MemorySearchConfig) ChunkOverlapValue() int {
	if c.ChunkOverlap <= 0 {
		return defaultMemoryChunkOverlap
	}
	return c.ChunkOverlap
}

func (c MemorySearchConfig) EmbeddingTimeoutValue() time.Duration {
	timeoutMS := c.EmbeddingTimeoutMS
	if timeoutMS <= 0 {
		timeoutMS = defaultMemoryEmbeddingTimeoutMS
	}
	return time.Duration(timeoutMS) * time.Millisecond
}

func (c MemorySearchConfig) EmbeddingMaxBatchValue() int {
	if c.EmbeddingMaxBatch <= 0 {
		return defaultMemoryEmbeddingMaxBatch
	}
	return c.EmbeddingMaxBatch
}

func (c MemorySearchConfig) EmbeddingMaxCharsValue() int {
	if c.EmbeddingMaxChars <= 0 {
		return defaultMemoryEmbeddingMaxChars
	}
	return c.EmbeddingMaxChars
}

func (c MemorySearchConfig) EmbeddingRetriesValue() int {
	if c.EmbeddingRetries < 0 {
		return 0
	}
	if c.EmbeddingRetries == 0 {
		return defaultMemoryEmbeddingRetries
	}
	return c.EmbeddingRetries
}

func (c MemorySearchConfig) EmbeddingConcurrencyValue() int {
	if c.EmbeddingConcurrent <= 0 {
		return defaultMemoryEmbeddingConcurrency
	}
	return c.EmbeddingConcurrent
}

func (c MemorySearchConfig) Validate() error {
	weights := c.Hybrid.VectorWeightValue() + c.Hybrid.TextWeightValue()
	if weights <= 0 {
		return fmt.Errorf("memory_search hybrid weights must be positive")
	}
	return nil
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
	MemoryFiles           *memfiles.Manager
	MemorySearch          memory.MemorySearchManager
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
