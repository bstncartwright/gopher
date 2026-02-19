package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

type AnthropicEffort string

const (
	AnthropicEffortLow    AnthropicEffort = "low"
	AnthropicEffortMedium AnthropicEffort = "medium"
	AnthropicEffortHigh   AnthropicEffort = "high"
	AnthropicEffortMax    AnthropicEffort = "max"
)

type AnthropicMessagesOptions struct {
	StreamOptions
	ThinkingEnabled      bool            `json:"thinkingEnabled,omitempty"`
	ThinkingBudgetTokens int             `json:"thinkingBudgetTokens,omitempty"`
	Effort               AnthropicEffort `json:"effort,omitempty"`
	InterleavedThinking  bool            `json:"interleavedThinking,omitempty"`
	ToolChoice           any             `json:"toolChoice,omitempty"`
}

type anthropicCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func StreamAnthropicMessages(model Model, conversation Context, options *StreamOptions) *AssistantMessageEventStream {
	opts := &AnthropicMessagesOptions{}
	if options != nil {
		opts.StreamOptions = *options
	}
	return streamAnthropicMessages(model, conversation, opts)
}

func StreamSimpleAnthropicMessages(model Model, conversation Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == "" {
		apiKey = GetEnvAPIKey(string(model.Provider))
	}
	base := BuildBaseOptions(model, options, apiKey)
	if options == nil || options.Reasoning == "" {
		return streamAnthropicMessages(model, conversation, &AnthropicMessagesOptions{StreamOptions: *base, ThinkingEnabled: false})
	}
	if supportsAdaptiveThinkingModel(model.ID) {
		effort := mapThinkingLevelToAnthropicEffort(options.Reasoning)
		return streamAnthropicMessages(model, conversation, &AnthropicMessagesOptions{StreamOptions: *base, ThinkingEnabled: true, Effort: effort})
	}
	maxTokens := 0
	if base.MaxTokens != nil {
		maxTokens = *base.MaxTokens
	}
	adjustedMax, thinkingBudget := AdjustMaxTokensForThinking(maxTokens, model.MaxTokens, options.Reasoning, options.ThinkingBudgets)
	base.MaxTokens = &adjustedMax
	return streamAnthropicMessages(model, conversation, &AnthropicMessagesOptions{StreamOptions: *base, ThinkingEnabled: true, ThinkingBudgetTokens: thinkingBudget})
}

func streamAnthropicMessages(model Model, conversation Context, options *AnthropicMessagesOptions) *AssistantMessageEventStream {
	stream := CreateAssistantMessageEventStream()
	if options == nil {
		options = &AnthropicMessagesOptions{}
	}
	ctx := resolveRequestContext(&options.StreamOptions)

	slog.Debug("anthropic: starting stream",
		"model_id", model.ID,
		"session_id", options.SessionID,
		"messages_count", len(conversation.Messages),
		"tools_count", len(conversation.Tools),
		"thinking_enabled", options.ThinkingEnabled,
	)

	go func() {
		output := NewAssistantMessage(model)
		defer stream.End(&output)

		apiKey := options.APIKey
		if apiKey == "" {
			apiKey = GetEnvAPIKey(string(model.Provider))
		}
		if apiKey == "" {
			slog.Error("anthropic: no API key", "provider", model.Provider)
			output.StopReason = StopReasonError
			output.ErrorMessage = fmt.Sprintf("no API key for provider %s", model.Provider)
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		payload := buildAnthropicParams(model, conversation, options)
		if options.OnPayload != nil {
			options.OnPayload(payload)
		}
		body, err := json.Marshal(payload)
		if err != nil {
			slog.Error("anthropic: failed to marshal payload", "error", err)
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		endpoint := resolveAnthropicMessagesURL(model.BaseURL)
		slog.Debug("anthropic: sending request", "endpoint", endpoint, "model_id", model.ID)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			slog.Error("anthropic: failed to create request", "error", err)
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}
		headers := withJSONContentType(mergeHeaders(model.Headers, options.Headers))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("accept", "text/event-stream")
		if strings.HasPrefix(apiKey, "sk-ant-oat") {
			req.Header.Set("Authorization", "Bearer "+apiKey)
			req.Header.Set("anthropic-beta", "fine-grained-tool-streaming-2025-05-14")
		} else {
			req.Header.Set("x-api-key", apiKey)
			beta := []string{"fine-grained-tool-streaming-2025-05-14"}
			if options.InterleavedThinking {
				beta = append(beta, "interleaved-thinking-2025-05-14")
			}
			req.Header.Set("anthropic-beta", strings.Join(beta, ","))
		}

		resp, err := defaultHTTPClient.Do(req)
		if err != nil {
			slog.Error("anthropic: request failed", "error", err, "model_id", model.ID)
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			slog.Error("anthropic: received error status",
				"status_code", resp.StatusCode,
				"response", strings.TrimSpace(string(raw)),
				"model_id", model.ID,
			)
			output.StopReason = StopReasonError
			output.ErrorMessage = fmt.Sprintf("%d status code (%s)", resp.StatusCode, strings.TrimSpace(string(raw)))
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		slog.Debug("anthropic: stream started", "model_id", model.ID, "session_id", options.SessionID)
		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: &output})
		events := make(chan sseEvent, 32)
		errCh := make(chan error, 1)
		go func() {
			errCh <- readSSE(ctx, resp.Body, events)
		}()

		indexToPos := map[int]int{}
		partialJSON := map[int]string{}

		for ev := range events {
			if ev.Data == "[DONE]" || ev.Data == "" {
				continue
			}
			payload := decodeJSON(ev.Data)
			eventType := ev.Event
			if eventType == "" {
				eventType = stringFrom(payload, "type")
			}
			switch eventType {
			case "message_start":
				message := mapFrom(payload, "message")
				usage := mapFrom(message, "usage")
				output.Usage.Input = intFrom(usage, "input_tokens")
				output.Usage.Output = intFrom(usage, "output_tokens")
				output.Usage.CacheRead = intFrom(usage, "cache_read_input_tokens")
				output.Usage.CacheWrite = intFrom(usage, "cache_creation_input_tokens")
				output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
				CalculateCost(model, &output.Usage)
			case "content_block_start":
				index := intFrom(payload, "index")
				block := mapFrom(payload, "content_block")
				blockType := stringFrom(block, "type")
				switch blockType {
				case "text":
					output.Content = append(output.Content, ContentBlock{Type: ContentTypeText})
					pos := len(output.Content) - 1
					indexToPos[index] = pos
					stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: pos, Partial: &output})
				case "thinking":
					output.Content = append(output.Content, ContentBlock{Type: ContentTypeThinking})
					pos := len(output.Content) - 1
					indexToPos[index] = pos
					stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: pos, Partial: &output})
				case "tool_use":
					output.Content = append(output.Content, ContentBlock{
						Type:      ContentTypeToolCall,
						ID:        stringFrom(block, "id"),
						Name:      stringFrom(block, "name"),
						Arguments: mapFrom(block, "input"),
					})
					pos := len(output.Content) - 1
					indexToPos[index] = pos
					stream.Push(AssistantMessageEvent{Type: EventToolCallStart, ContentIndex: pos, Partial: &output})
				}
			case "content_block_delta":
				index := intFrom(payload, "index")
				pos, ok := indexToPos[index]
				if !ok || pos < 0 || pos >= len(output.Content) {
					continue
				}
				delta := mapFrom(payload, "delta")
				deltaType := stringFrom(delta, "type")
				switch deltaType {
				case "text_delta":
					text := stringFrom(delta, "text")
					output.Content[pos].Text += text
					stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: pos, Delta: text, Partial: &output})
				case "thinking_delta":
					thinking := stringFrom(delta, "thinking")
					output.Content[pos].Thinking += thinking
					stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: pos, Delta: thinking, Partial: &output})
				case "input_json_delta":
					piece := stringFrom(delta, "partial_json")
					partialJSON[index] += piece
					output.Content[pos].Arguments = ParseStreamingJSON(partialJSON[index])
					stream.Push(AssistantMessageEvent{Type: EventToolCallDelta, ContentIndex: pos, Delta: piece, Partial: &output})
				case "signature_delta":
					sig := stringFrom(delta, "signature")
					output.Content[pos].ThinkingSignature += sig
				}
			case "content_block_stop":
				index := intFrom(payload, "index")
				pos, ok := indexToPos[index]
				if !ok || pos < 0 || pos >= len(output.Content) {
					continue
				}
				block := output.Content[pos]
				switch block.Type {
				case ContentTypeText:
					stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: pos, Content: block.Text, Partial: &output})
				case ContentTypeThinking:
					stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: pos, Content: block.Thinking, Partial: &output})
				case ContentTypeToolCall:
					if partialJSON[index] != "" {
						output.Content[pos].Arguments = ParseStreamingJSON(partialJSON[index])
					}
					copyBlock := output.Content[pos].Clone()
					stream.Push(AssistantMessageEvent{Type: EventToolCallEnd, ContentIndex: pos, ToolCall: &copyBlock, Partial: &output})
				}
			case "message_delta":
				delta := mapFrom(payload, "delta")
				if stop := stringFrom(delta, "stop_reason"); stop != "" {
					output.StopReason = mapAnthropicStopReason(stop)
				}
				usage := mapFrom(payload, "usage")
				if val, ok := usage["input_tokens"]; ok && val != nil {
					output.Usage.Input = intFrom(usage, "input_tokens")
				}
				if val, ok := usage["output_tokens"]; ok && val != nil {
					output.Usage.Output = intFrom(usage, "output_tokens")
				}
				if val, ok := usage["cache_read_input_tokens"]; ok && val != nil {
					output.Usage.CacheRead = intFrom(usage, "cache_read_input_tokens")
				}
				if val, ok := usage["cache_creation_input_tokens"]; ok && val != nil {
					output.Usage.CacheWrite = intFrom(usage, "cache_creation_input_tokens")
				}
				output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
				CalculateCost(model, &output.Usage)
			}
		}

		if err := <-errCh; err != nil && ctx.Err() == nil {
			slog.Error("anthropic: SSE read error", "error", err, "model_id", model.ID)
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		if ctx.Err() != nil {
			slog.Debug("anthropic: context cancelled", "model_id", model.ID)
			output.StopReason = StopReasonAborted
			output.ErrorMessage = "request was aborted"
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonAborted, Error: &output})
			return
		}
		if output.StopReason == StopReasonError || output.StopReason == StopReasonAborted {
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		slog.Info("anthropic: stream complete",
			"model_id", model.ID,
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

func buildAnthropicParams(model Model, conversation Context, options *AnthropicMessagesOptions) map[string]any {
	cacheControl := getAnthropicCacheControl(model.BaseURL, options.CacheRetention)
	messages := convertAnthropicMessages(conversation.Messages, model, cacheControl)
	maxTokens := model.MaxTokens / 3
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	if options.MaxTokens != nil {
		maxTokens = *options.MaxTokens
	}
	params := map[string]any{
		"model":      model.ID,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if conversation.SystemPrompt != "" {
		entry := map[string]any{"type": "text", "text": SanitizeSurrogates(conversation.SystemPrompt)}
		if cacheControl != nil {
			entry["cache_control"] = cacheControl
		}
		params["system"] = []map[string]any{entry}
	}
	if options.Temperature != nil {
		params["temperature"] = *options.Temperature
	}
	if len(conversation.Tools) > 0 {
		params["tools"] = convertAnthropicTools(conversation.Tools)
	}
	if options.ThinkingEnabled && model.Reasoning {
		if supportsAdaptiveThinkingModel(model.ID) {
			params["thinking"] = map[string]any{"type": "adaptive"}
			if options.Effort != "" {
				params["output_config"] = map[string]any{"effort": string(options.Effort)}
			}
		} else {
			budget := options.ThinkingBudgetTokens
			if budget <= 0 {
				budget = 1024
			}
			params["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
	}
	if options.Metadata != nil {
		if userID, ok := options.Metadata["user_id"].(string); ok && userID != "" {
			params["metadata"] = map[string]any{"user_id": userID}
		}
	}
	if options.ToolChoice != nil {
		switch t := options.ToolChoice.(type) {
		case string:
			params["tool_choice"] = map[string]any{"type": t}
		default:
			params["tool_choice"] = t
		}
	}
	return params
}

func resolveAnthropicMessagesURL(baseURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if normalized == "" {
		normalized = "https://api.anthropic.com"
	}
	if strings.HasSuffix(normalized, "/v1/messages") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/v1") {
		return normalized + "/messages"
	}
	return normalized + "/v1/messages"
}

func convertAnthropicMessages(messages []Message, model Model, cacheControl *anthropicCacheControl) []map[string]any {
	transformed := TransformMessages(messages, model, func(id string, _ Model, _ Message) string {
		return normalizeAnthropicToolCallID(id)
	})

	out := make([]map[string]any, 0, len(transformed))
	for i := 0; i < len(transformed); i++ {
		msg := transformed[i]
		switch msg.Role {
		case RoleUser:
			if text, ok := msg.ContentText(); ok {
				if strings.TrimSpace(text) == "" {
					continue
				}
				out = append(out, map[string]any{"role": "user", "content": SanitizeSurrogates(text)})
				continue
			}
			if blocks, ok := msg.ContentBlocks(); ok {
				parts := make([]map[string]any, 0, len(blocks))
				for _, b := range blocks {
					switch b.Type {
					case ContentTypeText:
						if strings.TrimSpace(b.Text) == "" {
							continue
						}
						parts = append(parts, map[string]any{"type": "text", "text": SanitizeSurrogates(b.Text)})
					case ContentTypeImage:
						if supportsImageInput(model) {
							parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": b.MimeType, "data": b.Data}})
						}
					}
				}
				if len(parts) > 0 {
					out = append(out, map[string]any{"role": "user", "content": parts})
				}
			}
		case RoleAssistant:
			blocks, _ := msg.ContentBlocks()
			parts := make([]map[string]any, 0, len(blocks))
			for _, b := range blocks {
				switch b.Type {
				case ContentTypeText:
					if strings.TrimSpace(b.Text) == "" {
						continue
					}
					parts = append(parts, map[string]any{"type": "text", "text": SanitizeSurrogates(b.Text)})
				case ContentTypeThinking:
					if strings.TrimSpace(b.Thinking) == "" {
						continue
					}
					if strings.TrimSpace(b.ThinkingSignature) == "" {
						parts = append(parts, map[string]any{"type": "text", "text": SanitizeSurrogates(b.Thinking)})
					} else {
						parts = append(parts, map[string]any{"type": "thinking", "thinking": SanitizeSurrogates(b.Thinking), "signature": b.ThinkingSignature})
					}
				case ContentTypeToolCall:
					parts = append(parts, map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": b.Arguments})
				}
			}
			if len(parts) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": parts})
			}
		case RoleToolResult:
			toolResults := make([]map[string]any, 0)
			j := i
			for ; j < len(transformed) && transformed[j].Role == RoleToolResult; j++ {
				item := transformed[j]
				blocks, _ := item.ContentBlocks()
				content := convertAnthropicToolResultContent(blocks)
				toolResults = append(toolResults, map[string]any{
					"type":        "tool_result",
					"tool_use_id": item.ToolCallID,
					"content":     content,
					"is_error":    item.IsError,
				})
			}
			i = j - 1
			if len(toolResults) > 0 {
				out = append(out, map[string]any{"role": "user", "content": toolResults})
			}
		}
	}

	if cacheControl != nil && len(out) > 0 {
		last := out[len(out)-1]
		if stringFrom(last, "role") == "user" {
			if content, ok := last["content"].([]map[string]any); ok && len(content) > 0 {
				lastBlock := content[len(content)-1]
				lastBlock["cache_control"] = cacheControl
			}
		}
	}

	return out
}

func convertAnthropicToolResultContent(blocks []ContentBlock) any {
	hasImage := false
	for _, b := range blocks {
		if b.Type == ContentTypeImage {
			hasImage = true
			break
		}
	}
	if !hasImage {
		text := collectTextContent(blocks)
		if text == "" {
			text = "(see attached image)"
		}
		return SanitizeSurrogates(text)
	}
	parts := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case ContentTypeText:
			parts = append(parts, map[string]any{"type": "text", "text": SanitizeSurrogates(b.Text)})
		case ContentTypeImage:
			parts = append(parts, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": b.MimeType, "data": b.Data}})
		}
	}
	if len(parts) == 0 {
		return "(see attached image)"
	}
	return parts
}

func convertAnthropicTools(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		schema := tool.Parameters
		properties := map[string]any{}
		if p, ok := schema["properties"].(map[string]any); ok {
			properties = p
		}
		required := []any{}
		if r, ok := schema["required"].([]any); ok {
			required = r
		}
		out = append(out, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"input_schema": map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
		})
	}
	return out
}

func getAnthropicCacheControl(baseURL string, requested CacheRetention) *anthropicCacheControl {
	retention := requested
	if retention == "" {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("PI_CACHE_RETENTION")), "long") {
			retention = CacheRetentionLong
		} else {
			retention = CacheRetentionShort
		}
	}
	if retention == CacheRetentionNone {
		return nil
	}
	out := &anthropicCacheControl{Type: "ephemeral"}
	if retention == CacheRetentionLong && strings.Contains(baseURL, "api.anthropic.com") {
		out.TTL = "1h"
	}
	return out
}

func normalizeAnthropicToolCallID(id string) string {
	clean := make([]rune, 0, len(id))
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			clean = append(clean, r)
		} else {
			clean = append(clean, '_')
		}
	}
	out := string(clean)
	if len(out) > 64 {
		out = out[:64]
	}
	if out == "" {
		out = "tool_call"
	}
	return out
}

func supportsAdaptiveThinkingModel(modelID string) bool {
	return strings.Contains(modelID, "opus-4-6") || strings.Contains(modelID, "opus-4.6")
}

func mapThinkingLevelToAnthropicEffort(level ThinkingLevel) AnthropicEffort {
	switch level {
	case ThinkingMinimal, ThinkingLow:
		return AnthropicEffortLow
	case ThinkingMedium:
		return AnthropicEffortMedium
	case ThinkingHigh:
		return AnthropicEffortHigh
	case ThinkingXHigh:
		return AnthropicEffortMax
	default:
		return AnthropicEffortHigh
	}
}

func mapAnthropicStopReason(reason string) StopReason {
	switch reason {
	case "end_turn", "stop_sequence", "pause_turn":
		return StopReasonStop
	case "max_tokens":
		return StopReasonLength
	case "tool_use":
		return StopReasonToolUse
	case "refusal", "sensitive":
		return StopReasonError
	default:
		return StopReasonStop
	}
}
