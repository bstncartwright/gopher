package ai

import (
	"encoding/json"
	"fmt"
	"strings"
)

type openAIResponsesStreamOptions struct {
	ServiceTier             string
	ApplyServiceTierPricing func(usage *Usage, serviceTier string)
}

func convertResponsesMessages(model Model, conversation Context, allowedToolCallProviders map[Provider]struct{}, includeSystemPrompt bool) []any {
	messages := make([]any, 0, len(conversation.Messages)+2)
	normalizeToolCallID := func(id string, _ Model, _ Message) string {
		if _, ok := allowedToolCallProviders[model.Provider]; !ok {
			return id
		}
		if !strings.Contains(id, "|") {
			return id
		}
		parts := strings.SplitN(id, "|", 2)
		callID := sanitizeToolID(parts[0], 64)
		itemID := ""
		if len(parts) > 1 {
			itemID = sanitizeToolID(parts[1], 64)
		}
		if !strings.HasPrefix(itemID, "fc") {
			itemID = "fc_" + itemID
		}
		return strings.TrimRight(callID, "_") + "|" + strings.TrimRight(itemID, "_")
	}

	transformed := TransformMessages(conversation.Messages, model, normalizeToolCallID)
	if includeSystemPrompt && conversation.SystemPrompt != "" {
		role := "system"
		if model.Reasoning {
			role = "developer"
		}
		messages = append(messages, map[string]any{
			"role":    role,
			"content": SanitizeSurrogates(conversation.SystemPrompt),
		})
	}

	for _, msg := range transformed {
		switch msg.Role {
		case RoleUser:
			if text, ok := msg.ContentText(); ok {
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": []map[string]any{{"type": "input_text", "text": SanitizeSurrogates(text)}},
				})
				continue
			}
			if blocks, ok := msg.ContentBlocks(); ok {
				parts := make([]map[string]any, 0, len(blocks))
				for _, block := range blocks {
					switch block.Type {
					case ContentTypeText:
						parts = append(parts, map[string]any{"type": "input_text", "text": SanitizeSurrogates(block.Text)})
					case ContentTypeImage:
						parts = append(parts, map[string]any{"type": "input_image", "detail": "auto", "image_url": fmt.Sprintf("data:%s;base64,%s", block.MimeType, block.Data)})
					}
				}
				if !supportsImageInput(model) {
					parts = filterResponsesImageParts(parts)
				}
				if len(parts) > 0 {
					messages = append(messages, map[string]any{"role": "user", "content": parts})
				}
			}
		case RoleAssistant:
			blocks, _ := msg.ContentBlocks()
			for _, block := range blocks {
				switch block.Type {
				case ContentTypeThinking:
					if block.ThinkingSignature != "" {
						var item map[string]any
						if err := json.Unmarshal([]byte(block.ThinkingSignature), &item); err == nil {
							messages = append(messages, item)
						}
					}
				case ContentTypeText:
					msgID := block.TextSignature
					if msgID == "" {
						msgID = "msg"
					}
					if len(msgID) > 64 {
						msgID = "msg_" + shortHash(msgID)
					}
					messages = append(messages, map[string]any{
						"type":    "message",
						"role":    "assistant",
						"content": []map[string]any{{"type": "output_text", "text": SanitizeSurrogates(block.Text), "annotations": []any{}}},
						"status":  "completed",
						"id":      msgID,
					})
				case ContentTypeToolCall:
					parts := strings.SplitN(block.ID, "|", 2)
					callID := parts[0]
					itemID := ""
					if len(parts) > 1 {
						itemID = parts[1]
					}
					messages = append(messages, map[string]any{
						"type":      "function_call",
						"id":        itemID,
						"call_id":   callID,
						"name":      block.Name,
						"arguments": mustJSONString(block.Arguments),
					})
				}
			}
		case RoleToolResult:
			blocks, _ := msg.ContentBlocks()
			textResult := collectTextContent(blocks)
			if textResult == "" {
				textResult = "(see attached image)"
			}
			callID := msg.ToolCallID
			if strings.Contains(callID, "|") {
				callID = strings.SplitN(callID, "|", 2)[0]
			}
			messages = append(messages, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  SanitizeSurrogates(textResult),
			})
			if supportsImageInput(model) {
				hasImage := false
				for _, b := range blocks {
					if b.Type == ContentTypeImage {
						hasImage = true
						break
					}
				}
				if hasImage {
					parts := []map[string]any{{"type": "input_text", "text": "Attached image(s) from tool result:"}}
					for _, b := range blocks {
						if b.Type == ContentTypeImage {
							parts = append(parts, map[string]any{"type": "input_image", "detail": "auto", "image_url": fmt.Sprintf("data:%s;base64,%s", b.MimeType, b.Data)})
						}
					}
					messages = append(messages, map[string]any{"role": "user", "content": parts})
				}
			}
		}
	}

	return messages
}

func convertResponsesTools(tools []Tool, strict *bool) []map[string]any {
	strictValue := false
	if strict != nil {
		strictValue = *strict
	}
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
			"strict":      strictValue,
		})
	}
	return out
}

func processResponsesStreamEvent(event map[string]any, output *AssistantMessage, stream *AssistantMessageEventStream, model Model, state *responsesStreamState, options *openAIResponsesStreamOptions) error {
	eventType := stringFrom(event, "type")
	switch eventType {
	case "response.output_item.added":
		item := mapFrom(event, "item")
		itemType := stringFrom(item, "type")
		switch itemType {
		case "reasoning":
			output.Content = append(output.Content, ContentBlock{Type: ContentTypeThinking})
			state.currentBlock = &output.Content[len(output.Content)-1]
			state.currentItemType = "reasoning"
			stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: len(output.Content) - 1, Partial: output})
		case "message":
			output.Content = append(output.Content, ContentBlock{Type: ContentTypeText})
			state.currentBlock = &output.Content[len(output.Content)-1]
			state.currentItemType = "message"
			stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: len(output.Content) - 1, Partial: output})
		case "function_call":
			callID := stringFrom(item, "call_id")
			itemID := stringFrom(item, "id")
			output.Content = append(output.Content, ContentBlock{Type: ContentTypeToolCall, ID: callID + "|" + itemID, Name: stringFrom(item, "name"), Arguments: map[string]any{}})
			state.currentBlock = &output.Content[len(output.Content)-1]
			state.currentItemType = "function_call"
			state.partialJSON = stringFrom(item, "arguments")
			stream.Push(AssistantMessageEvent{Type: EventToolCallStart, ContentIndex: len(output.Content) - 1, Partial: output})
		}
	case "response.reasoning_summary_text.delta":
		if state.currentItemType == "reasoning" && state.currentBlock != nil && state.currentBlock.Type == ContentTypeThinking {
			delta := stringFrom(event, "delta")
			state.currentBlock.Thinking += delta
			stream.Push(AssistantMessageEvent{Type: EventThinkingDelta, ContentIndex: len(output.Content) - 1, Delta: delta, Partial: output})
		}
	case "response.output_text.delta", "response.refusal.delta":
		if state.currentItemType == "message" && state.currentBlock != nil && state.currentBlock.Type == ContentTypeText {
			delta := stringFrom(event, "delta")
			state.currentBlock.Text += delta
			stream.Push(AssistantMessageEvent{Type: EventTextDelta, ContentIndex: len(output.Content) - 1, Delta: delta, Partial: output})
		}
	case "response.function_call_arguments.delta":
		if state.currentItemType == "function_call" && state.currentBlock != nil && state.currentBlock.Type == ContentTypeToolCall {
			delta := stringFrom(event, "delta")
			state.partialJSON += delta
			state.currentBlock.Arguments = ParseStreamingJSON(state.partialJSON)
			stream.Push(AssistantMessageEvent{Type: EventToolCallDelta, ContentIndex: len(output.Content) - 1, Delta: delta, Partial: output})
		}
	case "response.function_call_arguments.done":
		if state.currentBlock != nil && state.currentBlock.Type == ContentTypeToolCall {
			state.partialJSON = stringFrom(event, "arguments")
			state.currentBlock.Arguments = ParseStreamingJSON(state.partialJSON)
		}
	case "response.output_item.done":
		item := mapFrom(event, "item")
		itemType := stringFrom(item, "type")
		if state.currentBlock == nil {
			break
		}
		idx := len(output.Content) - 1
		switch itemType {
		case "reasoning":
			summary := mapSliceFrom(item, "summary")
			if len(summary) > 0 {
				parts := make([]string, 0, len(summary))
				for _, s := range summary {
					parts = append(parts, stringFrom(s, "text"))
				}
				state.currentBlock.Thinking = strings.Join(parts, "\n\n")
			}
			blob, _ := json.Marshal(item)
			state.currentBlock.ThinkingSignature = string(blob)
			stream.Push(AssistantMessageEvent{Type: EventThinkingEnd, ContentIndex: idx, Content: state.currentBlock.Thinking, Partial: output})
		case "message":
			if content := mapSliceFrom(item, "content"); len(content) > 0 {
				parts := make([]string, 0, len(content))
				for _, part := range content {
					if text := stringFrom(part, "text"); text != "" {
						parts = append(parts, text)
						continue
					}
					if refusal := stringFrom(part, "refusal"); refusal != "" {
						parts = append(parts, refusal)
					}
				}
				state.currentBlock.Text = strings.Join(parts, "")
			}
			state.currentBlock.TextSignature = stringFrom(item, "id")
			stream.Push(AssistantMessageEvent{Type: EventTextEnd, ContentIndex: idx, Content: state.currentBlock.Text, Partial: output})
		case "function_call":
			if state.partialJSON == "" {
				state.partialJSON = stringFrom(item, "arguments")
			}
			state.currentBlock.Arguments = ParseStreamingJSON(state.partialJSON)
			copyBlock := state.currentBlock.Clone()
			stream.Push(AssistantMessageEvent{Type: EventToolCallEnd, ContentIndex: idx, ToolCall: &copyBlock, Partial: output})
		}
		state.currentBlock = nil
		state.currentItemType = ""
		state.partialJSON = ""
	case "response.completed":
		response := mapFrom(event, "response")
		status := stringFrom(response, "status")
		output.StopReason = mapOpenAIResponsesStopReason(status)
		usage := mapFrom(response, "usage")
		if len(usage) > 0 {
			cached := intFrom(mapFrom(usage, "input_tokens_details"), "cached_tokens")
			output.Usage = Usage{
				Input:       intFrom(usage, "input_tokens") - cached,
				Output:      intFrom(usage, "output_tokens"),
				CacheRead:   cached,
				CacheWrite:  0,
				TotalTokens: intFrom(usage, "total_tokens"),
			}
			CalculateCost(model, &output.Usage)
			if options != nil && options.ApplyServiceTierPricing != nil {
				tier := stringFrom(response, "service_tier")
				if tier == "" {
					tier = options.ServiceTier
				}
				options.ApplyServiceTierPricing(&output.Usage, tier)
			}
		}
		if output.StopReason == StopReasonStop {
			for _, block := range output.Content {
				if block.Type == ContentTypeToolCall {
					output.StopReason = StopReasonToolUse
					break
				}
			}
		}
	case "response.failed":
		return fmt.Errorf("response failed")
	case "error":
		code := stringFrom(event, "code")
		msg := stringFrom(event, "message")
		if code != "" {
			return fmt.Errorf("error code %s: %s", code, msg)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

type responsesStreamState struct {
	currentItemType string
	currentBlock    *ContentBlock
	partialJSON     string
}

func mapOpenAIResponsesStopReason(status string) StopReason {
	switch status {
	case "", "completed", "in_progress", "queued":
		return StopReasonStop
	case "incomplete":
		return StopReasonLength
	case "failed", "cancelled":
		return StopReasonError
	default:
		return StopReasonStop
	}
}

func shortHash(str string) string {
	var h1 uint32 = 0xdeadbeef
	var h2 uint32 = 0x41c6ce57
	for i := 0; i < len(str); i++ {
		ch := uint32(str[i])
		h1 = (h1 ^ ch) * 2654435761
		h2 = (h2 ^ ch) * 1597334677
	}
	return fmt.Sprintf("%x%x", h2, h1)
}

func stringFrom(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if ok {
		return s
	}
	return fmt.Sprint(v)
}

func intFrom(m map[string]any, key string) int {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case float32:
		return int(t)
	case int:
		return t
	case int64:
		return int(t)
	default:
		return 0
	}
}

func mapFrom(m map[string]any, key string) map[string]any {
	v, ok := m[key]
	if !ok || v == nil {
		return map[string]any{}
	}
	if out, ok := v.(map[string]any); ok {
		return out
	}
	return map[string]any{}
}

func mapSliceFrom(m map[string]any, key string) []map[string]any {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func filterResponsesImageParts(parts []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, p := range parts {
		if p["type"] == "input_image" {
			continue
		}
		out = append(out, p)
	}
	return out
}
