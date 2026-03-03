package context

import (
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
)

func TestComputeReserveTokensUsesFloorAndHalfWindowCap(t *testing.T) {
	tests := []struct {
		name     string
		window   int
		floor    int
		expected int
	}{
		{name: "half window cap", window: 12000, floor: 20000, expected: 6000},
		{name: "fifteen percent wins", window: 200000, floor: 20000, expected: 30000},
		{name: "floor wins", window: 40000, floor: 20000, expected: 20000},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeReserveTokens(tc.window, tc.floor)
			if got != tc.expected {
				t.Fatalf("ComputeReserveTokens(%d, %d) = %d, want %d", tc.window, tc.floor, got, tc.expected)
			}
		})
	}
}

func TestPruneMessagesDetailedRemovesThinkingButPreservesToolPayloads(t *testing.T) {
	oldPayload := strings.Repeat("x", 600)
	recentPayload := strings.Repeat("y", 3200)
	messages := []ai.Message{
		{Role: ai.RoleUser, Content: "u0", Timestamp: 1},
		ai.NewToolResultMessage("old", "read", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: oldPayload}}, false),
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "a2"}}, Timestamp: 3},
		{Role: ai.RoleUser, Content: "u3", Timestamp: 4},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "a4"}}, Timestamp: 5},
		{Role: ai.RoleUser, Content: "u5", Timestamp: 6},
		{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "a6"}}, Timestamp: 7},
		ai.NewToolResultMessage("recent", "exec", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: recentPayload}}, false),
	}

	result := PruneMessagesDetailed(messages, PruneOptions{})
	if result.ToolResultTruncations != 0 {
		t.Fatalf("ToolResultTruncations = %d, want 0", result.ToolResultTruncations)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("expected no prune actions for thinking-free messages, got %#v", result.Actions)
	}

	oldBlocks, _ := result.Messages[1].ContentBlocks()
	if len(oldBlocks) == 0 || oldBlocks[0].Text != oldPayload {
		t.Fatalf("historical tool result payload was unexpectedly changed")
	}
	recentBlocks, _ := result.Messages[7].ContentBlocks()
	if len(recentBlocks) == 0 || recentBlocks[0].Text != recentPayload {
		t.Fatalf("recent tool result payload was unexpectedly changed")
	}
}

func TestRepairMessagesDropsOrphanToolResultsAndStaleToolCalls(t *testing.T) {
	messages := []ai.Message{
		{
			Role: ai.RoleAssistant,
			Content: []ai.ContentBlock{
				{Type: ai.ContentTypeToolCall, ID: "stale", Name: "exec", Arguments: map[string]any{"cmd": "echo stale"}},
			},
			Timestamp: 1,
		},
		{Role: ai.RoleUser, Content: "u1", Timestamp: 2},
		{
			Role: ai.RoleAssistant,
			Content: []ai.ContentBlock{
				{Type: ai.ContentTypeToolCall, ID: "ok-call", Name: "read", Arguments: map[string]any{"path": "a.txt"}},
			},
			Timestamp: 3,
		},
		ai.NewToolResultMessage("ok-call", "read", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "ok"}}, false),
		ai.NewToolResultMessage("orphan", "write", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "no matching call"}}, false),
		{Role: ai.RoleUser, Content: "u5", Timestamp: 6},
	}

	repaired, actions := RepairMessages(messages, RepairOptions{HistoricalWindowMessages: 4})
	if len(actions) < 2 {
		t.Fatalf("expected stale tool-call and orphan-removal actions, got %#v", actions)
	}

	firstBlocks, _ := repaired[0].ContentBlocks()
	for _, block := range firstBlocks {
		if block.Type == ai.ContentTypeToolCall {
			t.Fatalf("expected stale historical tool call to be removed")
		}
	}
	for _, msg := range repaired {
		if msg.Role == ai.RoleToolResult && strings.TrimSpace(msg.ToolCallID) == "orphan" {
			t.Fatalf("expected orphan tool result to be removed")
		}
	}
}

func TestRepairMessagesMatchesResponsesStyleToolCallIDs(t *testing.T) {
	messages := []ai.Message{
		{
			Role: ai.RoleAssistant,
			Content: []ai.ContentBlock{
				{Type: ai.ContentTypeToolCall, ID: "call-1|fc_1", Name: "read", Arguments: map[string]any{"path": "a.txt"}},
			},
			Timestamp: 1,
		},
		ai.NewToolResultMessage("call-1", "different-name", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "ok"}}, false),
	}

	repaired, actions := RepairMessages(messages, RepairOptions{})
	if len(actions) != 0 {
		t.Fatalf("expected no repair actions, got %#v", actions)
	}
	if len(repaired) != 2 {
		t.Fatalf("expected tool result to be retained, got len=%d", len(repaired))
	}
	if repaired[1].Role != ai.RoleToolResult {
		t.Fatalf("expected second message to remain a tool result, got %s", repaired[1].Role)
	}
}

func TestRepairMessagesDropsOrphanToolResultEvenWhenToolNameMatches(t *testing.T) {
	messages := []ai.Message{
		{
			Role: ai.RoleAssistant,
			Content: []ai.ContentBlock{
				{Type: ai.ContentTypeToolCall, ID: "call-ok", Name: "read", Arguments: map[string]any{"path": "ok.txt"}},
			},
			Timestamp: 1,
		},
		ai.NewToolResultMessage("call-orphan", "read", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "orphan"}}, false),
	}

	repaired, _ := RepairMessages(messages, RepairOptions{})
	if len(repaired) != 1 {
		t.Fatalf("expected orphan tool result to be dropped, got len=%d", len(repaired))
	}
	if repaired[0].Role != ai.RoleAssistant {
		t.Fatalf("expected assistant message to remain, got %s", repaired[0].Role)
	}
}

func TestSelectMessagesBudgetWithRepairLeavesNoOrphanToolResults(t *testing.T) {
	messages := []ai.Message{
		{Role: ai.RoleUser, Content: "u1", Timestamp: 1},
		ai.NewToolResultMessage("orphan", "read", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "no call"}}, false),
	}
	selected, _, _ := SelectMessagesForBudget(messages, 10)
	repaired, _ := RepairMessages(selected, RepairOptions{})
	for _, msg := range repaired {
		if msg.Role == ai.RoleToolResult {
			t.Fatalf("expected no toolResult messages after repair in orphan-only selection")
		}
	}
}

func TestSelectMessagesForBudgetDropsOversizedTrailingChunkAndKeepsLastUser(t *testing.T) {
	messages := []ai.Message{
		{Role: ai.RoleUser, Content: "keep this user message", Timestamp: 1},
		{
			Role: ai.RoleAssistant,
			Content: []ai.ContentBlock{
				{Type: ai.ContentTypeToolCall, ID: "call-1", Name: "read", Arguments: map[string]any{"path": "huge.txt"}},
			},
			Timestamp: 2,
		},
		ai.NewToolResultMessage("call-1", "read", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: strings.Repeat("z", 5000)}}, false),
	}

	selected, dropped, _ := SelectMessagesForBudget(messages, 12)
	if len(selected) != 1 {
		t.Fatalf("selected length = %d, want 1", len(selected))
	}
	if selected[0].Role != ai.RoleUser {
		t.Fatalf("selected role = %s, want user", selected[0].Role)
	}
	if len(dropped) != 2 {
		t.Fatalf("dropped length = %d, want 2", len(dropped))
	}
	if dropped[0].Role != ai.RoleAssistant || dropped[1].Role != ai.RoleToolResult {
		t.Fatalf("expected oversized assistant/tool_result chunk to be dropped")
	}
}
