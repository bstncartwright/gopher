package context

import (
	"context"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/memory"
)

func TestAssemblerInjectsMemoriesWithinBudget(t *testing.T) {
	assembler := NewAssembler(AssemblerOptions{DefaultMaxTokens: 200, SafetyMargin: 20, MaxMemoryRecords: 3})
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
		MaxTokens:   200,
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
