package ai

import "testing"

func TestTransformMessagesNormalizesToolCallIDAndToolResult(t *testing.T) {
	model := Model{ID: "target", API: APIOpenAICompletions, Provider: ProviderOpenAI}
	messages := []Message{
		{
			Role: RoleAssistant,
			Content: []ContentBlock{{
				Type:      ContentTypeToolCall,
				ID:        "call|bad/id",
				Name:      "echo",
				Arguments: map[string]any{"x": 1},
			}},
			API:        APIOpenAIResponses,
			Provider:   ProviderOpenAI,
			Model:      "other",
			StopReason: StopReasonToolUse,
		},
		{
			Role:       RoleToolResult,
			ToolCallID: "call|bad/id",
			ToolName:   "echo",
			Content: []ContentBlock{{
				Type: ContentTypeText,
				Text: "ok",
			}},
		},
	}

	out := TransformMessages(messages, model, func(id string, _ Model, _ Message) string {
		return "normalized"
	})
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
	assistantBlocks, _ := out[0].ContentBlocks()
	if assistantBlocks[0].ID != "normalized" {
		t.Fatalf("expected normalized tool call id, got %q", assistantBlocks[0].ID)
	}
	if out[1].ToolCallID != "normalized" {
		t.Fatalf("expected normalized tool result id, got %q", out[1].ToolCallID)
	}
}

func TestTransformMessagesInsertsSyntheticToolResult(t *testing.T) {
	model := Model{ID: "target", API: APIOpenAICompletions, Provider: ProviderOpenAI}
	messages := []Message{
		{
			Role: RoleAssistant,
			Content: []ContentBlock{{
				Type:      ContentTypeToolCall,
				ID:        "call_1",
				Name:      "echo",
				Arguments: map[string]any{"x": 1},
			}},
			API:        APIOpenAICompletions,
			Provider:   ProviderOpenAI,
			Model:      "target",
			StopReason: StopReasonToolUse,
		},
		{Role: RoleUser, Content: "continue"},
	}

	out := TransformMessages(messages, model, nil)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages (assistant + synthetic toolResult + user), got %d", len(out))
	}
	if out[1].Role != RoleToolResult {
		t.Fatalf("expected synthetic toolResult at index 1, got %q", out[1].Role)
	}
	if out[1].ToolCallID != "call_1" {
		t.Fatalf("expected toolCallId call_1, got %q", out[1].ToolCallID)
	}
}
