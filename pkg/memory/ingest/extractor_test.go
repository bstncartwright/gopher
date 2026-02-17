package ingest

import (
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestExtractorBuildsLayeredMemories(t *testing.T) {
	now := time.Now().UTC()
	extractor := NewExtractor(ExtractorOptions{
		Now: func() time.Time { return now },
	})
	events := []sessionrt.Event{
		{
			SessionID: "sess-1",
			From:      "user:me",
			Type:      sessionrt.EventMessage,
			Payload:   sessionrt.Message{Role: sessionrt.RoleUser, Content: "remember: use pnpm in this repo"},
			Seq:       1,
			Timestamp: now,
		},
		{
			SessionID: "sess-1",
			From:      "agent-1",
			Type:      sessionrt.EventToolCall,
			Payload: map[string]any{
				"name": "shell.exec",
				"args": map[string]any{"command": "pnpm test"},
			},
			Seq:       2,
			Timestamp: now,
		},
		{
			SessionID: "sess-1",
			From:      "agent-1",
			Type:      sessionrt.EventToolResult,
			Payload: map[string]any{
				"name":   "shell.exec",
				"status": "ok",
				"result": map[string]any{"stdout": "pass"},
			},
			Seq:       3,
			Timestamp: now,
		},
		{
			SessionID: "sess-1",
			From:      "agent-1",
			Type:      sessionrt.EventMessage,
			Payload:   sessionrt.Message{Role: sessionrt.RoleAgent, Content: "tests passed"},
			Seq:       4,
			Timestamp: now,
		},
	}

	records := extractor.ExtractSession("sess-1", "agent-1", events)
	if len(records) == 0 {
		t.Fatalf("expected extracted records")
	}

	hasType := map[memory.MemoryType]bool{}
	for _, record := range records {
		hasType[record.Type] = true
	}
	if !hasType[memory.MemorySemantic] {
		t.Fatalf("expected semantic memory from explicit remember")
	}
	if !hasType[memory.MemoryTool] {
		t.Fatalf("expected tool memory")
	}
	if !hasType[memory.MemoryEpisodic] {
		t.Fatalf("expected episodic memory")
	}
	if !hasType[memory.MemoryProcedural] {
		t.Fatalf("expected procedural memory")
	}
}
