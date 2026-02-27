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

func TestPruneMessagesDetailedAppliesHistoricalAndRecentCaps(t *testing.T) {
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

	result := PruneMessagesDetailed(messages, PruneOptions{
		MaxHistoricalToolResultChars: 240,
		MaxRecentToolResultChars:     2400,
		RecentWindowMessages:         4,
	})
	if result.ToolResultTruncations != 2 {
		t.Fatalf("ToolResultTruncations = %d, want 2", result.ToolResultTruncations)
	}
	if len(result.Actions) < 2 {
		t.Fatalf("expected at least 2 prune actions, got %#v", result.Actions)
	}

	oldBlocks, _ := result.Messages[1].ContentBlocks()
	if len(oldBlocks) == 0 || len(oldBlocks[0].Text) > 260 {
		t.Fatalf("historical tool result not truncated as expected")
	}
	recentBlocks, _ := result.Messages[7].ContentBlocks()
	if len(recentBlocks) == 0 || len(recentBlocks[0].Text) > 2420 {
		t.Fatalf("recent tool result not truncated as expected")
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
