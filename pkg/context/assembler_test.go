package context

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/memory"
)

func TestAssemblerInjectsMemoriesWithinBudget(t *testing.T) {
	assembler := NewAssembler(AssemblerOptions{DefaultMaxTokens: 400, SafetyMargin: 20, MaxMemoryRecords: 3})
	bundle, err := assembler.Build(context.Background(), ContextRequest{
		BaseSystemPrompt: "base",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "hello", Timestamp: 1},
		},
		Retrieved: []memory.MemoryRecord{
			{ID: "1", Type: memory.MemorySemantic, Scope: memory.ScopeGlobal, Content: strings.Repeat("fact ", 20)},
			{ID: "2", Type: memory.MemoryProcedural, Scope: memory.ScopeGlobal, Content: "run tests then deploy"},
			{ID: "3", Type: memory.MemoryTool, Scope: memory.ScopeGlobal, Content: "shell.exec worked"},
		},
		CurrentTask: "deploy",
		MaxTokens:   400,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(bundle.Messages) != 1 {
		t.Fatalf("expected one message, got %d", len(bundle.Messages))
	}
	if len(bundle.Sources) == 0 {
		t.Fatalf("expected selected memory sources")
	}
	if !strings.Contains(bundle.SystemPrompt, "### retrieved memory") {
		t.Fatalf("expected retrieved memory section in system prompt")
	}
}

func TestAssemblerDiagnosticsDeterministic(t *testing.T) {
	assembler := NewAssembler(AssemblerOptions{DefaultMaxTokens: 512, SafetyMargin: 16, MaxMemoryRecords: 4})
	input := ContextRequest{
		BaseSystemPrompt: "system prompt",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "first", Timestamp: 1},
			{Role: ai.RoleAssistant, Content: []ai.ContentBlock{{Type: ai.ContentTypeText, Text: "ack"}}, Timestamp: 2},
		},
		Retrieved: []memory.MemoryRecord{
			{ID: "m1", Type: memory.MemorySemantic, Scope: memory.ScopeGlobal, Content: "semantic memory"},
			{ID: "m2", Type: memory.MemoryProcedural, Scope: memory.ScopeGlobal, Content: "procedural memory"},
		},
		CompactionSummaries: []string{"Compacted 2 older messages.\nKey user context: deploy carefully"},
		CurrentTask:         "run deploy",
		MaxTokens:           512,
		EnablePruning:       true,
		EnableCompaction:    true,
		BootstrapTokens:     20,
		WorkingTokens:       10,
		OverflowRetries:     1,
	}

	a, err := assembler.Build(context.Background(), input)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	b, err := assembler.Build(context.Background(), input)
	if err != nil {
		t.Fatalf("Build() second error: %v", err)
	}

	if !reflect.DeepEqual(a.Diagnostics, b.Diagnostics) {
		t.Fatalf("expected deterministic diagnostics output")
	}
	if a.Diagnostics.ModelContextWindow != 512 {
		t.Fatalf("model context window = %d, want 512", a.Diagnostics.ModelContextWindow)
	}
	if a.Diagnostics.RecentMessagesLane.UsedTokens <= 0 {
		t.Fatalf("expected recent messages lane usage to be populated")
	}
}

func TestAssemblerCompactsDroppedOversizedHistoryFromBudgetFallback(t *testing.T) {
	assembler := NewAssembler(AssemblerOptions{DefaultMaxTokens: 160, SafetyMargin: 16, MaxMemoryRecords: 2})
	bundle, err := assembler.Build(context.Background(), ContextRequest{
		BaseSystemPrompt: "system",
		Messages: []ai.Message{
			{Role: ai.RoleUser, Content: "keep latest user context", Timestamp: 1},
			{
				Role: ai.RoleAssistant,
				Content: []ai.ContentBlock{
					{Type: ai.ContentTypeToolCall, ID: "call-1", Name: "read", Arguments: map[string]any{"path": "huge.txt"}},
				},
				Timestamp: 2,
			},
			ai.NewToolResultMessage("call-1", "read", []ai.ContentBlock{{Type: ai.ContentTypeText, Text: strings.Repeat("z", 5000)}}, false),
		},
		MaxTokens:        160,
		ReserveMinTokens: 0,
		EnableCompaction: true,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(bundle.Messages) != 1 || bundle.Messages[0].Role != ai.RoleUser {
		t.Fatalf("expected fallback to keep only the latest user message")
	}
	if strings.TrimSpace(bundle.NewCompactionSummary) == "" {
		t.Fatalf("expected compaction summary for dropped oversized history")
	}
	if !strings.Contains(bundle.NewCompactionSummary, "Compacted") {
		t.Fatalf("unexpected compaction summary: %q", bundle.NewCompactionSummary)
	}
	if len(bundle.Diagnostics.CompactionActions) == 0 {
		t.Fatalf("expected compaction action diagnostics for dropped history")
	}
}
