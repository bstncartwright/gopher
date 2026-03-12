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

func TestConvertResponsesMessagesPreservesAssistantPhaseOnReplay(t *testing.T) {
	model := Model{ID: "gpt-5.4", API: APIOpenAIResponses, Provider: ProviderOpenAI}
	conversation := Context{
		Messages: []Message{{
			Role:     RoleAssistant,
			Phase:    AssistantPhaseCommentary,
			Content:  []ContentBlock{{Type: ContentTypeText, Text: "Working on it.", TextSignature: "msg_1"}},
			API:      APIOpenAIResponses,
			Provider: ProviderOpenAI,
			Model:    "gpt-5.4",
		}},
	}

	payload := convertResponsesMessages(model, conversation, openAIResponsesToolCallProviders, false)
	if len(payload) != 1 {
		t.Fatalf("payload len = %d, want 1", len(payload))
	}
	item, ok := payload[0].(map[string]any)
	if !ok {
		t.Fatalf("payload item type = %T, want map[string]any", payload[0])
	}
	if got := item["phase"]; got != string(AssistantPhaseCommentary) {
		t.Fatalf("phase = %#v, want %q", got, AssistantPhaseCommentary)
	}
}

func TestProcessResponsesStreamEventPreservesAssistantPhaseFromMessageItem(t *testing.T) {
	output := NewAssistantMessage(Model{ID: "gpt-5.4", API: APIOpenAIResponses, Provider: ProviderOpenAI})
	stream := CreateAssistantMessageEventStream()
	state := &responsesStreamState{}

	added := map[string]any{
		"type": "response.output_item.added",
		"item": map[string]any{"type": "message", "id": "msg_1", "phase": "commentary"},
	}
	done := map[string]any{
		"type": "response.output_item.done",
		"item": map[string]any{
			"type":    "message",
			"id":      "msg_1",
			"phase":   "commentary",
			"content": []any{map[string]any{"type": "output_text", "text": "Working on it."}},
		},
	}

	if err := processResponsesStreamEvent(added, &output, stream, outputToModel(output), state, nil); err != nil {
		t.Fatalf("added event error: %v", err)
	}
	if err := processResponsesStreamEvent(done, &output, stream, outputToModel(output), state, nil); err != nil {
		t.Fatalf("done event error: %v", err)
	}

	if output.Phase != AssistantPhaseCommentary {
		t.Fatalf("phase = %q, want %q", output.Phase, AssistantPhaseCommentary)
	}
}

func TestProcessResponsesStreamEventRecoversPhaseAndToolUseFromResponseCompleted(t *testing.T) {
	output := NewAssistantMessage(Model{ID: "gpt-5.4", API: APIOpenAIResponses, Provider: ProviderOpenAI})
	stream := CreateAssistantMessageEventStream()
	state := &responsesStreamState{}

	completed := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"status": "completed",
			"output": []any{
				map[string]any{
					"type":    "message",
					"id":      "msg_phase",
					"phase":   "commentary",
					"content": []any{map[string]any{"type": "output_text", "text": "Working..."}},
				},
				map[string]any{
					"type":      "function_call",
					"id":        "fc_1",
					"call_id":   "call_1",
					"name":      "exec",
					"arguments": `{"cmd":"ls"}`,
				},
			},
		},
	}

	if err := processResponsesStreamEvent(completed, &output, stream, outputToModel(output), state, nil); err != nil {
		t.Fatalf("completed event error: %v", err)
	}

	if output.Phase != AssistantPhaseCommentary {
		t.Fatalf("phase = %q, want %q", output.Phase, AssistantPhaseCommentary)
	}
	if output.StopReason != StopReasonToolUse {
		t.Fatalf("stop reason = %q, want %q", output.StopReason, StopReasonToolUse)
	}
	if len(output.Content) != 2 {
		t.Fatalf("content len = %d, want 2", len(output.Content))
	}
	if output.Content[0].Text != "Working..." {
		t.Fatalf("text = %q, want Working...", output.Content[0].Text)
	}
	if output.Content[1].ID != "call_1|fc_1" {
		t.Fatalf("tool call id = %q, want call_1|fc_1", output.Content[1].ID)
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
