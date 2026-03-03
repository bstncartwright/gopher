package ai

import "testing"

func TestConvertResponsesMessagesDropsOrphanFunctionCallOutput(t *testing.T) {
	model := Model{ID: "gpt-5.3-codex-spark", API: APIOpenAIResponses, Provider: ProviderOpenAI}
	conversation := Context{
		Messages: []Message{
			NewToolResultMessage("call-orphan", "read", []ContentBlock{{Type: ContentTypeText, Text: "orphan"}}, false),
		},
	}

	payload := convertResponsesMessages(model, conversation, openAIResponsesToolCallProviders, true)
	if hasFunctionCallOutput(payload, "call-orphan") {
		t.Fatalf("expected orphan function_call_output to be dropped")
	}
}

func TestConvertResponsesMessagesKeepsMatchedFunctionCallOutputWithResponsesID(t *testing.T) {
	model := Model{ID: "gpt-5.3-codex-spark", API: APIOpenAIResponses, Provider: ProviderOpenAI}
	conversation := Context{
		Messages: []Message{
			{
				Role: RoleAssistant,
				Content: []ContentBlock{
					{Type: ContentTypeToolCall, ID: "call-1|fc_1", Name: "read", Arguments: map[string]any{"path": "a.txt"}},
				},
				API:      APIOpenAIResponses,
				Provider: ProviderOpenAI,
				Model:    "gpt-5.3-codex-spark",
			},
			NewToolResultMessage("call-1", "read", []ContentBlock{{Type: ContentTypeText, Text: "ok"}}, false),
		},
	}

	payload := convertResponsesMessages(model, conversation, openAIResponsesToolCallProviders, true)
	if !hasFunctionCallOutput(payload, "call-1") {
		t.Fatalf("expected function_call_output for matched call_id")
	}
}

func hasFunctionCallOutput(payload []any, callID string) bool {
	for _, item := range payload {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if msg["type"] != "function_call_output" {
			continue
		}
		if id, ok := msg["call_id"].(string); ok && id == callID {
			return true
		}
	}
	return false
}
