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

func TestConvertResponsesToolsEncodesHostedWebSearch(t *testing.T) {
	tools := convertResponsesTools([]Tool{{
		Kind:              ToolKindHostedWebSearch,
		Name:              "web_search",
		Description:       "native search",
		ExternalWebAccess: boolPtr(true),
	}}, nil)

	if len(tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(tools))
	}
	if got := tools[0]["type"]; got != "web_search" {
		t.Fatalf("type = %#v, want web_search", got)
	}
	if got, ok := tools[0]["external_web_access"].(bool); !ok || !got {
		t.Fatalf("expected external_web_access=true, got %#v", tools[0]["external_web_access"])
	}
}

func TestProcessResponsesStreamEventEmitsHostedWebSearchEvents(t *testing.T) {
	output := NewAssistantMessage(Model{ID: "gpt-5", API: APIOpenAIResponses, Provider: ProviderOpenAI})
	stream := CreateAssistantMessageEventStream()
	state := &responsesStreamState{}

	added := map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{
			"type":   "web_search_call",
			"id":     "ws_1",
			"query":  "weather seattle",
			"status": "in_progress",
		},
	}
	done := map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{
			"type":   "web_search_call",
			"id":     "ws_1",
			"query":  "weather seattle",
			"status": "completed",
			"action": map[string]any{"type": "search"},
		},
	}

	if err := processResponsesStreamEvent(added, &output, stream, outputToModel(output), state, nil); err != nil {
		t.Fatalf("added event error: %v", err)
	}
	if err := processResponsesStreamEvent(done, &output, stream, outputToModel(output), state, nil); err != nil {
		t.Fatalf("done event error: %v", err)
	}

	var sawStart bool
	var sawEnd bool
	for i := 0; i < 2; i++ {
		ev := <-stream.Events()
		switch ev.Type {
		case EventWebSearchStart:
			sawStart = ev.WebSearch != nil && ev.WebSearch.Query == "weather seattle"
		case EventWebSearchEnd:
			sawEnd = ev.WebSearch != nil && ev.WebSearch.Status == "completed"
		}
	}
	if !sawStart || !sawEnd {
		t.Fatalf("expected hosted web search start/end events, got start=%v end=%v", sawStart, sawEnd)
	}
	if len(output.Content) != 0 {
		t.Fatalf("expected hosted web_search to avoid assistant tool-call content blocks, got %#v", output.Content)
	}
}

func TestBuildOpenAIResponsesParamsIncludesHostedWebSearch(t *testing.T) {
	params := buildOpenAIResponsesParams(Model{
		ID:       "gpt-5",
		API:      APIOpenAIResponses,
		Provider: ProviderOpenAI,
	}, Context{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Tools: []Tool{{
			Kind:              ToolKindHostedWebSearch,
			Name:              "web_search",
			ExternalWebAccess: boolPtr(false),
		}},
	}, &OpenAIResponsesOptions{})

	tools, ok := params["tools"].([]map[string]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected one hosted search tool, got %#v", params["tools"])
	}
	if got := tools[0]["type"]; got != "web_search" {
		t.Fatalf("type = %#v, want web_search", got)
	}
	if got, ok := tools[0]["external_web_access"].(bool); !ok || got {
		t.Fatalf("expected external_web_access=false, got %#v", tools[0]["external_web_access"])
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

func outputToModel(output AssistantMessage) Model {
	return Model{ID: output.Model, API: output.API, Provider: output.Provider}
}
