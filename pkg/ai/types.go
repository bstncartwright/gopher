package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type API string

const (
	APIOpenAICompletions   API = "openai-completions"
	APIOpenAIResponses     API = "openai-responses"
	APIOpenAICodexResponse API = "openai-codex-responses"
	APIAnthropicMessages   API = "anthropic-messages"
)

type Provider string

const (
	ProviderOpenAI      Provider = "openai"
	ProviderOpenAICodex Provider = "openai-codex"
	ProviderKimiCoding  Provider = "kimi-coding"
	ProviderZAI         Provider = "zai"
	ProviderOllama      Provider = "ollama"
	ProviderAnthropic   Provider = "anthropic"
)

type ThinkingLevel string

const (
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
)

type CacheRetention string

const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

type Transport string

const (
	TransportSSE       Transport = "sse"
	TransportWebSocket Transport = "websocket"
	TransportAuto      Transport = "auto"
)

type MessageRole string

const (
	RoleUser       MessageRole = "user"
	RoleAssistant  MessageRole = "assistant"
	RoleToolResult MessageRole = "toolResult"
)

type AssistantPhase string

const (
	AssistantPhaseCommentary  AssistantPhase = "commentary"
	AssistantPhaseFinalAnswer AssistantPhase = "final_answer"
)

type ContentType string

const (
	ContentTypeText     ContentType = "text"
	ContentTypeThinking ContentType = "thinking"
	ContentTypeImage    ContentType = "image"
	ContentTypeToolCall ContentType = "toolCall"
)

type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonLength  StopReason = "length"
	StopReasonToolUse StopReason = "toolUse"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

type ThinkingBudgets struct {
	Minimal int `json:"minimal,omitempty"`
	Low     int `json:"low,omitempty"`
	Medium  int `json:"medium,omitempty"`
	High    int `json:"high,omitempty"`
}

type StreamOptions struct {
	Temperature     *float64          `json:"temperature,omitempty"`
	MaxTokens       *int              `json:"maxTokens,omitempty"`
	RequestContext  context.Context   `json:"-"`
	APIKey          string            `json:"apiKey,omitempty"`
	Transport       Transport         `json:"transport,omitempty"`
	CacheRetention  CacheRetention    `json:"cacheRetention,omitempty"`
	SessionID       string            `json:"sessionId,omitempty"`
	OnPayload       func(any)         `json:"-"`
	Headers         map[string]string `json:"headers,omitempty"`
	MaxRetryDelayMS int               `json:"maxRetryDelayMs,omitempty"`
	Metadata        map[string]any    `json:"metadata,omitempty"`
	ProviderOptions map[string]any    `json:"providerOptions,omitempty"`
}

type SimpleStreamOptions struct {
	StreamOptions
	Reasoning       ThinkingLevel    `json:"reasoning,omitempty"`
	ThinkingBudgets *ThinkingBudgets `json:"thinkingBudgets,omitempty"`
	ToolChoice      any              `json:"toolChoice,omitempty"`
}

type OpenRouterRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

type VercelGatewayRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

type OpenAICompletionsCompat struct {
	SupportsStore                    *bool                 `json:"supportsStore,omitempty"`
	SupportsDeveloperRole            *bool                 `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort          *bool                 `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming         *bool                 `json:"supportsUsageInStreaming,omitempty"`
	MaxTokensField                   string                `json:"maxTokensField,omitempty"`
	RequiresToolResultName           *bool                 `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult *bool                 `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText           *bool                 `json:"requiresThinkingAsText,omitempty"`
	RequiresMistralToolIDs           *bool                 `json:"requiresMistralToolIds,omitempty"`
	ThinkingFormat                   string                `json:"thinkingFormat,omitempty"`
	OpenRouterRouting                *OpenRouterRouting    `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting             *VercelGatewayRouting `json:"vercelGatewayRouting,omitempty"`
	SupportsStrictMode               *bool                 `json:"supportsStrictMode,omitempty"`
}

type OpenAIResponsesCompat struct {
	SupportsHostedWebSearch *bool `json:"supportsHostedWebSearch,omitempty"`
}

type ToolKind string

const (
	ToolKindFunction        ToolKind = "function"
	ToolKindHostedWebSearch ToolKind = "hosted_web_search"
)

type HostedWebSearchAction map[string]any

type HostedWebSearchCall struct {
	ID     string                `json:"id,omitempty"`
	Query  string                `json:"query,omitempty"`
	Action HostedWebSearchAction `json:"action,omitempty"`
	Status string                `json:"status,omitempty"`
}

type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

type Model struct {
	ID              string                   `json:"id"`
	Name            string                   `json:"name"`
	API             API                      `json:"api"`
	Provider        Provider                 `json:"provider"`
	BaseURL         string                   `json:"baseUrl"`
	Reasoning       bool                     `json:"reasoning"`
	Input           []string                 `json:"input"`
	Cost            ModelCost                `json:"cost"`
	ContextWindow   int                      `json:"contextWindow"`
	MaxTokens       int                      `json:"maxTokens"`
	Headers         map[string]string        `json:"headers,omitempty"`
	Compat          *OpenAICompletionsCompat `json:"compat,omitempty"`
	ResponsesCompat *OpenAIResponsesCompat   `json:"responsesCompat,omitempty"`
}

type Tool struct {
	Kind              ToolKind       `json:"kind,omitempty"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Parameters        map[string]any `json:"parameters,omitempty"`
	ExternalWebAccess *bool          `json:"externalWebAccess,omitempty"`
}

func (t Tool) KindValue() ToolKind {
	switch t.Kind {
	case ToolKindHostedWebSearch:
		return ToolKindHostedWebSearch
	default:
		return ToolKindFunction
	}
}

func (t Tool) IsHostedWebSearch() bool {
	return t.KindValue() == ToolKindHostedWebSearch
}

func SupportsHostedWebSearch(model Model) bool {
	if model.API != APIOpenAIResponses && model.API != APIOpenAICodexResponse {
		return false
	}
	if model.ResponsesCompat != nil && model.ResponsesCompat.SupportsHostedWebSearch != nil {
		return *model.ResponsesCompat.SupportsHostedWebSearch
	}
	return model.Provider == ProviderOpenAI || model.Provider == ProviderOpenAICodex
}

type ContentBlock struct {
	Type              ContentType    `json:"type"`
	Text              string         `json:"text,omitempty"`
	TextSignature     string         `json:"textSignature,omitempty"`
	Thinking          string         `json:"thinking,omitempty"`
	ThinkingSignature string         `json:"thinkingSignature,omitempty"`
	Data              string         `json:"data,omitempty"`
	MimeType          string         `json:"mimeType,omitempty"`
	ID                string         `json:"id,omitempty"`
	Name              string         `json:"name,omitempty"`
	Arguments         map[string]any `json:"arguments,omitempty"`
	ThoughtSignature  string         `json:"thoughtSignature,omitempty"`
}

func (b ContentBlock) Clone() ContentBlock {
	out := b
	if b.Arguments != nil {
		out.Arguments = CloneMap(b.Arguments)
	}
	return out
}

type Message struct {
	Role         MessageRole    `json:"role"`
	Phase        AssistantPhase `json:"phase,omitempty"`
	Content      any            `json:"content,omitempty"`
	ToolCallID   string         `json:"toolCallId,omitempty"`
	ToolName     string         `json:"toolName,omitempty"`
	Details      any            `json:"details,omitempty"`
	IsError      bool           `json:"isError,omitempty"`
	API          API            `json:"api,omitempty"`
	Provider     Provider       `json:"provider,omitempty"`
	Model        string         `json:"model,omitempty"`
	Usage        Usage          `json:"usage,omitempty"`
	StopReason   StopReason     `json:"stopReason,omitempty"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	Timestamp    int64          `json:"timestamp"`
}

func NewUserTextMessage(text string) Message {
	return Message{Role: RoleUser, Content: text, Timestamp: time.Now().UnixMilli()}
}

func NewUserBlocksMessage(blocks []ContentBlock) Message {
	return Message{Role: RoleUser, Content: blocks, Timestamp: time.Now().UnixMilli()}
}

func NewToolResultMessage(toolCallID, toolName string, blocks []ContentBlock, isError bool) Message {
	return Message{
		Role:       RoleToolResult,
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Content:    blocks,
		IsError:    isError,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func (m Message) Clone() Message {
	out := m
	if blocks, ok := m.ContentBlocks(); ok {
		cloned := make([]ContentBlock, 0, len(blocks))
		for _, b := range blocks {
			cloned = append(cloned, b.Clone())
		}
		out.Content = cloned
	} else if v, ok := m.Content.(map[string]any); ok {
		out.Content = CloneMap(v)
	}
	return out
}

func (m Message) ContentText() (string, bool) {
	s, ok := m.Content.(string)
	return s, ok
}

func (m Message) ContentBlocks() ([]ContentBlock, bool) {
	if m.Content == nil {
		return nil, false
	}
	switch c := m.Content.(type) {
	case []ContentBlock:
		out := make([]ContentBlock, len(c))
		copy(out, c)
		return out, true
	case []any:
		out := make([]ContentBlock, 0, len(c))
		for _, item := range c {
			blob, err := json.Marshal(item)
			if err != nil {
				continue
			}
			var block ContentBlock
			if err := json.Unmarshal(blob, &block); err != nil {
				continue
			}
			if block.Type == "" {
				continue
			}
			out = append(out, block)
		}
		return out, true
	default:
		blob, err := json.Marshal(c)
		if err != nil {
			return nil, false
		}
		var out []ContentBlock
		if err := json.Unmarshal(blob, &out); err != nil {
			return nil, false
		}
		return out, true
	}
}

func (m Message) Validate() error {
	switch m.Role {
	case RoleUser:
		if _, ok := m.ContentText(); ok {
			return nil
		}
		if blocks, ok := m.ContentBlocks(); ok && len(blocks) > 0 {
			for _, b := range blocks {
				if b.Type != ContentTypeText && b.Type != ContentTypeImage {
					return fmt.Errorf("user message contains invalid block type %q", b.Type)
				}
			}
			return nil
		}
		return fmt.Errorf("user message must contain string or text/image blocks")
	case RoleAssistant:
		blocks, ok := m.ContentBlocks()
		if !ok {
			return fmt.Errorf("assistant message content must be blocks")
		}
		for _, b := range blocks {
			if b.Type != ContentTypeText && b.Type != ContentTypeThinking && b.Type != ContentTypeToolCall {
				return fmt.Errorf("assistant message contains invalid block type %q", b.Type)
			}
		}
		return nil
	case RoleToolResult:
		if m.ToolCallID == "" || m.ToolName == "" {
			return fmt.Errorf("toolResult message requires toolCallId and toolName")
		}
		blocks, ok := m.ContentBlocks()
		if !ok || len(blocks) == 0 {
			return fmt.Errorf("toolResult message content must be text/image blocks")
		}
		for _, b := range blocks {
			if b.Type != ContentTypeText && b.Type != ContentTypeImage {
				return fmt.Errorf("toolResult message contains invalid block type %q", b.Type)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown role %q", m.Role)
	}
}

type Context struct {
	SystemPrompt string    `json:"systemPrompt,omitempty"`
	Messages     []Message `json:"messages"`
	Tools        []Tool    `json:"tools,omitempty"`
}

type CostBreakdown struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

type Usage struct {
	Input       int           `json:"input"`
	Output      int           `json:"output"`
	CacheRead   int           `json:"cacheRead"`
	CacheWrite  int           `json:"cacheWrite"`
	TotalTokens int           `json:"totalTokens"`
	Cost        CostBreakdown `json:"cost"`
}

type AssistantMessage struct {
	Role         MessageRole    `json:"role"`
	Phase        AssistantPhase `json:"phase,omitempty"`
	Content      []ContentBlock `json:"content"`
	API          API            `json:"api"`
	Provider     Provider       `json:"provider"`
	Model        string         `json:"model"`
	Usage        Usage          `json:"usage"`
	StopReason   StopReason     `json:"stopReason"`
	ErrorMessage string         `json:"errorMessage,omitempty"`
	Timestamp    int64          `json:"timestamp"`
}

func NewAssistantMessage(model Model) AssistantMessage {
	return AssistantMessage{
		Role:       RoleAssistant,
		Content:    make([]ContentBlock, 0, 4),
		API:        model.API,
		Provider:   model.Provider,
		Model:      model.ID,
		StopReason: StopReasonStop,
		Timestamp:  time.Now().UnixMilli(),
	}
}

func (m AssistantMessage) ToMessage() Message {
	return Message{
		Role:         RoleAssistant,
		Phase:        m.Phase,
		Content:      m.Content,
		API:          m.API,
		Provider:     m.Provider,
		Model:        m.Model,
		Usage:        m.Usage,
		StopReason:   m.StopReason,
		ErrorMessage: m.ErrorMessage,
		Timestamp:    m.Timestamp,
	}
}

type AssistantMessageEventType string

const (
	EventStart          AssistantMessageEventType = "start"
	EventTextStart      AssistantMessageEventType = "text_start"
	EventTextDelta      AssistantMessageEventType = "text_delta"
	EventTextEnd        AssistantMessageEventType = "text_end"
	EventThinkingStart  AssistantMessageEventType = "thinking_start"
	EventThinkingDelta  AssistantMessageEventType = "thinking_delta"
	EventThinkingEnd    AssistantMessageEventType = "thinking_end"
	EventToolCallStart  AssistantMessageEventType = "toolcall_start"
	EventToolCallDelta  AssistantMessageEventType = "toolcall_delta"
	EventToolCallEnd    AssistantMessageEventType = "toolcall_end"
	EventWebSearchStart AssistantMessageEventType = "web_search_start"
	EventWebSearchEnd   AssistantMessageEventType = "web_search_end"
	EventDone           AssistantMessageEventType = "done"
	EventError          AssistantMessageEventType = "error"
)

type AssistantMessageEvent struct {
	Type         AssistantMessageEventType `json:"type"`
	ContentIndex int                       `json:"contentIndex,omitempty"`
	Delta        string                    `json:"delta,omitempty"`
	Content      string                    `json:"content,omitempty"`
	ToolCall     *ContentBlock             `json:"toolCall,omitempty"`
	WebSearch    *HostedWebSearchCall      `json:"webSearch,omitempty"`
	Partial      *AssistantMessage         `json:"partial,omitempty"`
	Message      *AssistantMessage         `json:"message,omitempty"`
	Error        *AssistantMessage         `json:"error,omitempty"`
	Reason       StopReason                `json:"reason,omitempty"`
}

func CloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch t := v.(type) {
		case map[string]any:
			out[k] = CloneMap(t)
		case []any:
			cp := make([]any, len(t))
			for i, item := range t {
				if m, ok := item.(map[string]any); ok {
					cp[i] = CloneMap(m)
				} else {
					cp[i] = item
				}
			}
			out[k] = cp
		default:
			out[k] = v
		}
	}
	return out
}
