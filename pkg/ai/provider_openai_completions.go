package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type OpenAICompletionsOptions struct {
	StreamOptions
	ToolChoice      any           `json:"toolChoice,omitempty"`
	ReasoningEffort ThinkingLevel `json:"reasoningEffort,omitempty"`
}

type resolvedOpenAICompletionsCompat struct {
	SupportsStore                    bool
	SupportsDeveloperRole            bool
	SupportsReasoningEffort          bool
	SupportsUsageInStreaming         bool
	MaxTokensField                   string
	RequiresToolResultName           bool
	RequiresAssistantAfterToolResult bool
	RequiresThinkingAsText           bool
	RequiresMistralToolIDs           bool
	ThinkingFormat                   string
	OpenRouterRouting                OpenRouterRouting
	VercelGatewayRouting             VercelGatewayRouting
	SupportsStrictMode               bool
}

type openAIChatCompletionsChunk struct {
	Choices []struct {
		FinishReason *string `json:"finish_reason"`
		Delta        struct {
			Content          *string `json:"content"`
			Reasoning        *string `json:"reasoning"`
			ReasoningText    *string `json:"reasoning_text"`
			ReasoningContent *string `json:"reasoning_content"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func StreamOpenAICompletions(model Model, conversation Context, options *StreamOptions) *AssistantMessageEventStream {
	opts := &OpenAICompletionsOptions{}
	if options != nil {
		opts.StreamOptions = *options
	}
	return streamOpenAICompletions(model, conversation, opts)
}

func StreamSimpleOpenAICompletions(model Model, conversation Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == "" {
		apiKey = GetEnvAPIKey(string(model.Provider))
	}
	base := BuildBaseOptions(model, options, apiKey)
	reasoningEffort := ThinkingLevel("")
	if options != nil {
		reasoningEffort = options.Reasoning
	}
	if !SupportsXHigh(model) {
		reasoningEffort = ClampReasoning(reasoningEffort)
	}

	toolChoice := any(nil)
	if options != nil {
		toolChoice = options.ToolChoice
	}

	openAIOpts := &OpenAICompletionsOptions{
		StreamOptions:   *base,
		ReasoningEffort: reasoningEffort,
		ToolChoice:      toolChoice,
	}
	return streamOpenAICompletions(model, conversation, openAIOpts)
}

func streamOpenAICompletions(model Model, conversation Context, options *OpenAICompletionsOptions) *AssistantMessageEventStream {
	stream := CreateAssistantMessageEventStream()
	if options == nil {
		options = &OpenAICompletionsOptions{}
	}
	ctx := resolveRequestContext(&options.StreamOptions)

	slog.Debug("openai: starting stream",
		"model_id", model.ID,
		"provider", model.Provider,
		"session_id", options.SessionID,
		"messages_count", len(conversation.Messages),
		"tools_count", len(conversation.Tools),
	)

	go func() {
		output := NewAssistantMessage(model)
		defer stream.End(&output)

		apiKey := options.APIKey
		if apiKey == "" {
			apiKey = GetEnvAPIKey(string(model.Provider))
		}
		if apiKey == "" && model.Provider != ProviderOllama {
			slog.Error("openai: no API key", "provider", model.Provider)
			output.StopReason = StopReasonError
			output.ErrorMessage = fmt.Sprintf("no API key for provider %s", model.Provider)
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}
		if apiKey == "" && model.Provider == ProviderOllama {
			apiKey = "ollama"
		}

		compat := getOpenAICompletionsCompat(model)
		payload := buildOpenAICompletionsParams(model, conversation, options, compat)
		if options.OnPayload != nil {
			options.OnPayload(payload)
		}

		body, err := json.Marshal(payload)
		if err != nil {
			slog.Error("openai: failed to marshal payload", "error", err)
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		baseURL := resolveOpenAICompletionsBaseURL(model, apiKey)
		endpoint := strings.TrimRight(baseURL, "/") + "/chat/completions"
		slog.Debug("openai: sending request", "endpoint", endpoint, "model_id", model.ID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			slog.Error("openai: failed to create request", "error", err)
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		headers := withJSONContentType(mergeHeaders(model.Headers, options.Headers))
		if model.Provider == ProviderGitHubCopilot {
			for key, value := range buildGitHubCopilotDynamicHeaders(conversation.Messages) {
				headers[key] = value
			}
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := defaultHTTPClient.Do(req)
		if err != nil {
			slog.Error("openai: request failed", "error", err, "model_id", model.ID)
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			slog.Error("openai: received error status",
				"status_code", resp.StatusCode,
				"response", strings.TrimSpace(string(raw)),
				"model_id", model.ID,
			)
			output.StopReason = StopReasonError
			output.ErrorMessage = fmt.Sprintf("%d status code (%s)", resp.StatusCode, strings.TrimSpace(string(raw)))
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		slog.Debug("openai: stream started", "model_id", model.ID, "session_id", options.SessionID)
		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: &output})
		events := make(chan sseEvent, 32)
		readErr := make(chan error, 1)
		go func() {
			readErr <- readSSE(ctx, resp.Body, events)
		}()

		var currentBlock *ContentBlock
		var partialArgs string
		finishCurrent := func() {
			if currentBlock == nil {
				return
			}
			idx := len(output.Content) - 1
			switch currentBlock.Type {
			case ContentTypeText:
				stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: idx, Content: currentBlock.Text, Partial: &output})
			case ContentTypeThinking:
				stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: idx, Content: currentBlock.Thinking, Partial: &output})
			case ContentTypeToolCall:
				currentBlock.Arguments = ParseStreamingJSON(partialArgs)
				copyBlock := currentBlock.Clone()
				stream.Push(AssistantMessageEvent{Type: EventToolCallEnd, ContentIndex: idx, ToolCall: &copyBlock, Partial: &output})
			}
			currentBlock = nil
			partialArgs = ""
		}

		for event := range events {
			if event.Data == "[DONE]" {
				continue
			}
			var chunk openAIChatCompletionsChunk
			if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
				continue
			}

			if chunk.Usage != nil {
				cached := chunk.Usage.PromptTokensDetails.CachedTokens
				reasoning := chunk.Usage.CompletionTokensDetails.ReasoningTokens
				input := chunk.Usage.PromptTokens - cached
				outputTokens := chunk.Usage.CompletionTokens + reasoning
				output.Usage = Usage{
					Input:       input,
					Output:      outputTokens,
					CacheRead:   cached,
					CacheWrite:  0,
					TotalTokens: input + outputTokens + cached,
				}
				CalculateCost(model, &output.Usage)
			}

			if len(chunk.Choices) == 0 {
				continue
			}
			choice := chunk.Choices[0]
			if choice.FinishReason != nil {
				output.StopReason = mapOpenAICompletionsStopReason(*choice.FinishReason)
			}

			delta := choice.Delta
			if delta.Content != nil && *delta.Content != "" {
				if currentBlock == nil || currentBlock.Type != ContentTypeText {
					finishCurrent()
					output.Content = append(output.Content, ContentBlock{Type: ContentTypeText})
					currentBlock = &output.Content[len(output.Content)-1]
					stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: len(output.Content) - 1, Partial: &output})
				}
				currentBlock.Text += *delta.Content
				stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: len(output.Content) - 1, Delta: *delta.Content, Partial: &output})
			}

			reasoningDelta := firstNonEmptyStringPtr(delta.ReasoningContent, delta.Reasoning, delta.ReasoningText)
			if reasoningDelta != "" {
				if currentBlock == nil || currentBlock.Type != ContentTypeThinking {
					finishCurrent()
					output.Content = append(output.Content, ContentBlock{Type: ContentTypeThinking})
					currentBlock = &output.Content[len(output.Content)-1]
					stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: len(output.Content) - 1, Partial: &output})
				}
				currentBlock.Thinking += reasoningDelta
				stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: len(output.Content) - 1, Delta: reasoningDelta, Partial: &output})
			}

			if len(delta.ToolCalls) > 0 {
				for _, tc := range delta.ToolCalls {
					if currentBlock == nil || currentBlock.Type != ContentTypeToolCall || (tc.ID != "" && currentBlock.ID != tc.ID) {
						finishCurrent()
						output.Content = append(output.Content, ContentBlock{Type: ContentTypeToolCall, ID: tc.ID, Name: tc.Function.Name, Arguments: map[string]any{}})
						currentBlock = &output.Content[len(output.Content)-1]
						partialArgs = ""
						stream.Push(AssistantMessageEvent{Type: EventToolCallStart, ContentIndex: len(output.Content) - 1, Partial: &output})
					}
					if tc.ID != "" {
						currentBlock.ID = tc.ID
					}
					if tc.Function.Name != "" {
						currentBlock.Name = tc.Function.Name
					}
					deltaArgs := tc.Function.Arguments
					if deltaArgs != "" {
						partialArgs += deltaArgs
						currentBlock.Arguments = ParseStreamingJSON(partialArgs)
					}
					stream.Push(AssistantMessageEvent{Type: EventToolCallDelta, ContentIndex: len(output.Content) - 1, Delta: deltaArgs, Partial: &output})
				}
			}
		}

		finishCurrent()
		if err := <-readErr; err != nil && err != context.Canceled {
			slog.Error("openai: SSE read error", "error", err, "model_id", model.ID)
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}

		if ctx.Err() != nil {
			slog.Debug("openai: context cancelled", "model_id", model.ID)
			output.StopReason = StopReasonAborted
			output.ErrorMessage = "request was aborted"
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonAborted, Error: &output})
			return
		}
		if output.StopReason == StopReasonError || output.StopReason == StopReasonAborted {
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		slog.Info("openai: stream complete",
			"model_id", model.ID,
			"provider", model.Provider,
			"session_id", options.SessionID,
			"stop_reason", output.StopReason,
			"usage_input", output.Usage.Input,
			"usage_output", output.Usage.Output,
			"usage_total", output.Usage.TotalTokens,
			"usage_cost", output.Usage.Cost,
		)
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: &output})
	}()

	return stream
}

func buildOpenAICompletionsParams(model Model, conversation Context, options *OpenAICompletionsOptions, compat resolvedOpenAICompletionsCompat) map[string]any {
	messages := convertOpenAICompletionsMessages(model, conversation, compat)
	params := map[string]any{
		"model":    model.ID,
		"messages": messages,
		"stream":   true,
	}
	if compat.SupportsUsageInStreaming {
		params["stream_options"] = map[string]any{"include_usage": true}
	}
	if compat.SupportsStore {
		params["store"] = false
	}
	if options.MaxTokens != nil {
		if compat.MaxTokensField == "max_tokens" {
			params["max_tokens"] = *options.MaxTokens
		} else {
			params["max_completion_tokens"] = *options.MaxTokens
		}
	}
	if options.Temperature != nil {
		params["temperature"] = *options.Temperature
	}
	if len(conversation.Tools) > 0 {
		params["tools"] = convertOpenAICompletionsTools(conversation.Tools, compat)
	} else if hasToolHistory(conversation.Messages) {
		params["tools"] = []any{}
	}
	if options.ToolChoice != nil {
		params["tool_choice"] = options.ToolChoice
	}
	if model.Reasoning && options.ReasoningEffort != "" {
		switch compat.ThinkingFormat {
		case "zai":
			params["thinking"] = map[string]any{"type": "enabled"}
		case "qwen":
			params["enable_thinking"] = true
		default:
			if compat.SupportsReasoningEffort {
				params["reasoning_effort"] = string(options.ReasoningEffort)
			}
		}
	} else if model.Reasoning && compat.ThinkingFormat == "zai" {
		params["thinking"] = map[string]any{"type": "disabled"}
	}
	if strings.Contains(model.BaseURL, "openrouter.ai") && (compat.OpenRouterRouting.Only != nil || compat.OpenRouterRouting.Order != nil) {
		params["provider"] = compat.OpenRouterRouting
	}
	if strings.Contains(model.BaseURL, "ai-gateway.vercel.sh") && (compat.VercelGatewayRouting.Only != nil || compat.VercelGatewayRouting.Order != nil) {
		gateway := map[string]any{}
		if compat.VercelGatewayRouting.Only != nil {
			gateway["only"] = compat.VercelGatewayRouting.Only
		}
		if compat.VercelGatewayRouting.Order != nil {
			gateway["order"] = compat.VercelGatewayRouting.Order
		}
		params["providerOptions"] = map[string]any{"gateway": gateway}
	}
	return params
}

func convertOpenAICompletionsMessages(model Model, conversation Context, compat resolvedOpenAICompletionsCompat) []map[string]any {
	messages := make([]map[string]any, 0, len(conversation.Messages)+2)
	normalizeToolCallID := func(id string, _ Model, _ Message) string {
		if compat.RequiresMistralToolIDs {
			return normalizeMistralToolID(id)
		}
		if strings.Contains(id, "|") {
			parts := strings.SplitN(id, "|", 2)
			return sanitizeToolID(parts[0], 40)
		}
		if model.Provider == ProviderOpenAI && len(id) > 40 {
			return id[:40]
		}
		return id
	}

	transformedMessages := TransformMessages(conversation.Messages, model, normalizeToolCallID)
	if conversation.SystemPrompt != "" {
		role := "system"
		if model.Reasoning && compat.SupportsDeveloperRole {
			role = "developer"
		}
		messages = append(messages, map[string]any{"role": role, "content": SanitizeSurrogates(conversation.SystemPrompt)})
	}

	lastRole := ""
	for i := 0; i < len(transformedMessages); i++ {
		msg := transformedMessages[i]
		if compat.RequiresAssistantAfterToolResult && lastRole == string(RoleToolResult) && msg.Role == RoleUser {
			messages = append(messages, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
		}

		switch msg.Role {
		case RoleUser:
			if text, ok := msg.ContentText(); ok {
				messages = append(messages, map[string]any{"role": "user", "content": SanitizeSurrogates(text)})
			} else if blocks, ok := msg.ContentBlocks(); ok {
				parts := make([]map[string]any, 0, len(blocks))
				for _, b := range blocks {
					switch b.Type {
					case ContentTypeText:
						parts = append(parts, map[string]any{"type": "text", "text": SanitizeSurrogates(b.Text)})
					case ContentTypeImage:
						parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": fmt.Sprintf("data:%s;base64,%s", b.MimeType, b.Data)}})
					}
				}
				if !supportsImageInput(model) {
					parts = filterOutImageParts(parts)
				}
				if len(parts) > 0 {
					messages = append(messages, map[string]any{"role": "user", "content": parts})
				}
			}
			lastRole = string(RoleUser)
		case RoleAssistant:
			blocks, _ := msg.ContentBlocks()
			assistant := map[string]any{"role": "assistant", "content": any(nil)}
			textParts := make([]map[string]any, 0)
			for _, b := range blocks {
				if b.Type == ContentTypeText && strings.TrimSpace(b.Text) != "" {
					textParts = append(textParts, map[string]any{"type": "text", "text": SanitizeSurrogates(b.Text)})
				}
			}
			if len(textParts) > 0 {
				if model.Provider == ProviderGitHubCopilot {
					joined := ""
					for _, p := range textParts {
						joined += p["text"].(string)
					}
					assistant["content"] = joined
				} else {
					assistant["content"] = textParts
				}
			}
			thinkingText := ""
			toolCalls := make([]map[string]any, 0)
			for _, b := range blocks {
				switch b.Type {
				case ContentTypeThinking:
					if strings.TrimSpace(b.Thinking) == "" {
						continue
					}
					if compat.RequiresThinkingAsText {
						if thinkingText != "" {
							thinkingText += "\n\n"
						}
						thinkingText += b.Thinking
					}
				case ContentTypeToolCall:
					toolCalls = append(toolCalls, map[string]any{
						"id":   b.ID,
						"type": "function",
						"function": map[string]any{
							"name":      b.Name,
							"arguments": mustJSONString(b.Arguments),
						},
					})
				}
			}
			if thinkingText != "" {
				if existing, ok := assistant["content"].([]map[string]any); ok {
					assistant["content"] = append([]map[string]any{{"type": "text", "text": thinkingText}}, existing...)
				} else {
					assistant["content"] = []map[string]any{{"type": "text", "text": thinkingText}}
				}
			}
			if len(toolCalls) > 0 {
				assistant["tool_calls"] = toolCalls
			}
			hasContent := false
			if content, ok := assistant["content"]; ok && content != nil {
				switch t := content.(type) {
				case string:
					hasContent = t != ""
				case []map[string]any:
					hasContent = len(t) > 0
				}
			}
			if !hasContent && len(toolCalls) == 0 {
				continue
			}
			messages = append(messages, assistant)
			lastRole = string(RoleAssistant)
		case RoleToolResult:
			imageParts := make([]map[string]any, 0)
			j := i
			for ; j < len(transformedMessages) && transformedMessages[j].Role == RoleToolResult; j++ {
				toolMsg := transformedMessages[j]
				blocks, _ := toolMsg.ContentBlocks()
				textResult := collectTextContent(blocks)
				if textResult == "" {
					textResult = "(see attached image)"
				}
				toolResult := map[string]any{
					"role":         "tool",
					"content":      SanitizeSurrogates(textResult),
					"tool_call_id": toolMsg.ToolCallID,
				}
				if compat.RequiresToolResultName && toolMsg.ToolName != "" {
					toolResult["name"] = toolMsg.ToolName
				}
				messages = append(messages, toolResult)

				if supportsImageInput(model) {
					for _, b := range blocks {
						if b.Type == ContentTypeImage {
							imageParts = append(imageParts, map[string]any{
								"type": "image_url",
								"image_url": map[string]any{
									"url": fmt.Sprintf("data:%s;base64,%s", b.MimeType, b.Data),
								},
							})
						}
					}
				}
			}
			i = j - 1
			if len(imageParts) > 0 {
				if compat.RequiresAssistantAfterToolResult {
					messages = append(messages, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
				}
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": append([]map[string]any{{"type": "text", "text": "Attached image(s) from tool result:"}}, imageParts...),
				})
				lastRole = string(RoleUser)
			} else {
				lastRole = string(RoleToolResult)
			}
		}
	}

	if model.Provider == "openrouter" && strings.HasPrefix(model.ID, "anthropic/") {
		maybeAddOpenRouterAnthropicCacheControl(messages)
	}

	return messages
}

func maybeAddOpenRouterAnthropicCacheControl(messages []map[string]any) {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		role := fmt.Sprint(msg["role"])
		if role != "user" && role != "assistant" {
			continue
		}
		content := msg["content"]
		switch t := content.(type) {
		case string:
			msg["content"] = []map[string]any{{"type": "text", "text": t, "cache_control": map[string]any{"type": "ephemeral"}}}
			return
		case []map[string]any:
			for j := len(t) - 1; j >= 0; j-- {
				if t[j]["type"] == "text" {
					t[j]["cache_control"] = map[string]any{"type": "ephemeral"}
					msg["content"] = t
					return
				}
			}
		}
	}
}

func convertOpenAICompletionsTools(tools []Tool, compat resolvedOpenAICompletionsCompat) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		fn := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		}
		if compat.SupportsStrictMode {
			fn["strict"] = false
		}
		out = append(out, map[string]any{"type": "function", "function": fn})
	}
	return out
}

func mapOpenAICompletionsStopReason(reason string) StopReason {
	switch reason {
	case "", "stop":
		return StopReasonStop
	case "length":
		return StopReasonLength
	case "function_call", "tool_calls":
		return StopReasonToolUse
	case "content_filter":
		return StopReasonError
	default:
		return StopReasonStop
	}
}

func detectOpenAICompletionsCompat(model Model) resolvedOpenAICompletionsCompat {
	provider := model.Provider
	baseURL := model.BaseURL
	isGitHubCopilot := provider == ProviderGitHubCopilot || strings.Contains(baseURL, "githubcopilot.com")
	isZAI := provider == ProviderZAI || strings.Contains(baseURL, "api.z.ai")
	isNonStandard :=
		provider == "cerebras" || strings.Contains(baseURL, "cerebras.ai") ||
			provider == "xai" || strings.Contains(baseURL, "api.x.ai") ||
			provider == "mistral" || strings.Contains(baseURL, "mistral.ai") ||
			strings.Contains(baseURL, "chutes.ai") || strings.Contains(baseURL, "deepseek.com") ||
			isZAI || isGitHubCopilot || provider == "opencode" || strings.Contains(baseURL, "opencode.ai") ||
			provider == ProviderOllama || strings.Contains(baseURL, "localhost:11434") ||
			provider == ProviderMinimax || strings.Contains(baseURL, "api.minimax.io")
	useMaxTokens := provider == "mistral" || strings.Contains(baseURL, "mistral.ai") || strings.Contains(baseURL, "chutes.ai") || provider == ProviderOllama
	isGrok := provider == "xai" || strings.Contains(baseURL, "api.x.ai")
	isMistral := provider == "mistral" || strings.Contains(baseURL, "mistral.ai")

	thinkingFormat := "openai"
	if isZAI {
		thinkingFormat = "zai"
	}

	return resolvedOpenAICompletionsCompat{
		SupportsStore:                    !isNonStandard,
		SupportsDeveloperRole:            !isNonStandard,
		SupportsReasoningEffort:          !isGrok && !isZAI && provider != ProviderOllama,
		SupportsUsageInStreaming:         provider != ProviderOllama,
		MaxTokensField:                   ternary(useMaxTokens, "max_tokens", "max_completion_tokens"),
		RequiresToolResultName:           isMistral,
		RequiresAssistantAfterToolResult: false,
		RequiresThinkingAsText:           isMistral,
		RequiresMistralToolIDs:           isMistral,
		ThinkingFormat:                   thinkingFormat,
		OpenRouterRouting:                OpenRouterRouting{},
		VercelGatewayRouting:             VercelGatewayRouting{},
		SupportsStrictMode:               true,
	}
}

func resolveOpenAICompletionsBaseURL(model Model, apiKey string) string {
	if model.Provider == ProviderGitHubCopilot {
		return resolveGitHubCopilotBaseURL(apiKey)
	}
	return model.BaseURL
}

func getOpenAICompletionsCompat(model Model) resolvedOpenAICompletionsCompat {
	detected := detectOpenAICompletionsCompat(model)
	if model.Compat == nil {
		return detected
	}
	compat := *model.Compat
	return resolvedOpenAICompletionsCompat{
		SupportsStore:                    pickBool(compat.SupportsStore, detected.SupportsStore),
		SupportsDeveloperRole:            pickBool(compat.SupportsDeveloperRole, detected.SupportsDeveloperRole),
		SupportsReasoningEffort:          pickBool(compat.SupportsReasoningEffort, detected.SupportsReasoningEffort),
		SupportsUsageInStreaming:         pickBool(compat.SupportsUsageInStreaming, detected.SupportsUsageInStreaming),
		MaxTokensField:                   firstNonEmpty(compat.MaxTokensField, detected.MaxTokensField),
		RequiresToolResultName:           pickBool(compat.RequiresToolResultName, detected.RequiresToolResultName),
		RequiresAssistantAfterToolResult: pickBool(compat.RequiresAssistantAfterToolResult, detected.RequiresAssistantAfterToolResult),
		RequiresThinkingAsText:           pickBool(compat.RequiresThinkingAsText, detected.RequiresThinkingAsText),
		RequiresMistralToolIDs:           pickBool(compat.RequiresMistralToolIDs, detected.RequiresMistralToolIDs),
		ThinkingFormat:                   firstNonEmpty(compat.ThinkingFormat, detected.ThinkingFormat),
		OpenRouterRouting:                pickOpenRouterRouting(compat.OpenRouterRouting, detected.OpenRouterRouting),
		VercelGatewayRouting:             pickVercelGatewayRouting(compat.VercelGatewayRouting, detected.VercelGatewayRouting),
		SupportsStrictMode:               pickBool(compat.SupportsStrictMode, detected.SupportsStrictMode),
	}
}

func normalizeMistralToolID(id string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9]`)
	normalized := re.ReplaceAllString(id, "")
	if len(normalized) < 9 {
		padding := "ABCDEFGHI"
		normalized += padding[:9-len(normalized)]
	} else if len(normalized) > 9 {
		normalized = normalized[:9]
	}
	return normalized
}

func sanitizeToolID(id string, maxLen int) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	normalized := re.ReplaceAllString(id, "_")
	normalized = strings.TrimRight(normalized, "_")
	if maxLen > 0 && len(normalized) > maxLen {
		normalized = strings.TrimRight(normalized[:maxLen], "_")
	}
	if normalized == "" {
		return "tool_call"
	}
	return normalized
}

func hasToolHistory(messages []Message) bool {
	for _, msg := range messages {
		if msg.Role == RoleToolResult {
			return true
		}
		if msg.Role == RoleAssistant {
			blocks, _ := msg.ContentBlocks()
			for _, block := range blocks {
				if block.Type == ContentTypeToolCall {
					return true
				}
			}
		}
	}
	return false
}

func mergeHeaders(base map[string]string, overrides map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func firstNonEmptyStringPtr(values ...*string) string {
	for _, v := range values {
		if v != nil && *v != "" {
			return *v
		}
	}
	return ""
}

func mustJSONString(v any) string {
	if v == nil {
		return "{}"
	}
	blob, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(blob)
}

func stopReasonForError(ctx context.Context, err error) StopReason {
	if err == nil {
		return StopReasonError
	}
	if ctx.Err() != nil {
		return StopReasonAborted
	}
	if strings.Contains(strings.ToLower(err.Error()), "aborted") || strings.Contains(strings.ToLower(err.Error()), "canceled") {
		return StopReasonAborted
	}
	return StopReasonError
}

func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

func pickBool(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func pickOpenRouterRouting(v *OpenRouterRouting, fallback OpenRouterRouting) OpenRouterRouting {
	if v == nil {
		return fallback
	}
	return *v
}

func pickVercelGatewayRouting(v *VercelGatewayRouting, fallback VercelGatewayRouting) VercelGatewayRouting {
	if v == nil {
		return fallback
	}
	return *v
}

func supportsImageInput(model Model) bool {
	for _, m := range model.Input {
		if m == "image" {
			return true
		}
	}
	return false
}

func filterOutImageParts(parts []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		if p["type"] == "image_url" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func collectTextContent(blocks []ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == ContentTypeText && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Backoff helper shared by providers.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
