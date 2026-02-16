package ai

import "time"

type NormalizeToolCallIDFunc func(id string, model Model, source Message) string

func TransformMessages(messages []Message, model Model, normalizeToolCallID NormalizeToolCallIDFunc) []Message {
	toolCallIDMap := map[string]string{}

	transformed := make([]Message, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case RoleUser:
			transformed = append(transformed, msg.Clone())
		case RoleToolResult:
			cloned := msg.Clone()
			if normalized, ok := toolCallIDMap[msg.ToolCallID]; ok && normalized != "" && normalized != msg.ToolCallID {
				cloned.ToolCallID = normalized
			}
			transformed = append(transformed, cloned)
		case RoleAssistant:
			assistantMsg := msg.Clone()
			isSameModel := assistantMsg.Provider == model.Provider && assistantMsg.API == model.API && assistantMsg.Model == model.ID

			blocks, _ := assistantMsg.ContentBlocks()
			converted := make([]ContentBlock, 0, len(blocks))
			for _, block := range blocks {
				switch block.Type {
				case ContentTypeThinking:
					if isSameModel && block.ThinkingSignature != "" {
						converted = append(converted, block)
						continue
					}
					if block.Thinking == "" {
						continue
					}
					if isSameModel {
						converted = append(converted, block)
					} else {
						converted = append(converted, ContentBlock{Type: ContentTypeText, Text: block.Thinking})
					}
				case ContentTypeText:
					converted = append(converted, ContentBlock{Type: ContentTypeText, Text: block.Text, TextSignature: block.TextSignature})
				case ContentTypeToolCall:
					toolCall := block.Clone()
					if !isSameModel {
						toolCall.ThoughtSignature = ""
					}
					if !isSameModel && normalizeToolCallID != nil {
						normalizedID := normalizeToolCallID(block.ID, model, assistantMsg)
						if normalizedID != "" && normalizedID != block.ID {
							toolCallIDMap[block.ID] = normalizedID
							toolCall.ID = normalizedID
						}
					}
					converted = append(converted, toolCall)
				default:
					converted = append(converted, block)
				}
			}
			assistantMsg.Content = converted
			transformed = append(transformed, assistantMsg)
		default:
			transformed = append(transformed, msg.Clone())
		}
	}

	result := make([]Message, 0, len(transformed)+4)
	pendingToolCalls := make([]ContentBlock, 0)
	existingToolResults := map[string]struct{}{}

	flushPending := func() {
		if len(pendingToolCalls) == 0 {
			return
		}
		for _, tc := range pendingToolCalls {
			if tc.Type != ContentTypeToolCall {
				continue
			}
			if _, ok := existingToolResults[tc.ID]; ok {
				continue
			}
			result = append(result, Message{
				Role:       RoleToolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Content: []ContentBlock{{
					Type: ContentTypeText,
					Text: "No result provided",
				}},
				IsError:   true,
				Timestamp: time.Now().UnixMilli(),
			})
		}
		pendingToolCalls = pendingToolCalls[:0]
		existingToolResults = map[string]struct{}{}
	}

	for _, msg := range transformed {
		switch msg.Role {
		case RoleAssistant:
			flushPending()
			if msg.StopReason == StopReasonError || msg.StopReason == StopReasonAborted {
				continue
			}
			blocks, _ := msg.ContentBlocks()
			pendingToolCalls = pendingToolCalls[:0]
			for _, block := range blocks {
				if block.Type == ContentTypeToolCall {
					pendingToolCalls = append(pendingToolCalls, block)
				}
			}
			existingToolResults = map[string]struct{}{}
			result = append(result, msg)
		case RoleToolResult:
			existingToolResults[msg.ToolCallID] = struct{}{}
			result = append(result, msg)
		case RoleUser:
			flushPending()
			result = append(result, msg)
		default:
			result = append(result, msg)
		}
	}

	return result
}
