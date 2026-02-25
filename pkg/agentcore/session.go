package agentcore

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

var sessionCounter uint64

func (a *Agent) NewSession() *Session {
	id := fmt.Sprintf("s-%d-%d", time.Now().UTC().UnixNano(), atomic.AddUint64(&sessionCounter, 1))
	return &Session{
		ID:                  id,
		Messages:            make([]Message, 0, a.Config.MaxContextMessages),
		WorkingState:        map[string]any{},
		CompactionSummaries: make([]string, 0, 3),
	}
}

func boundMessages(messages []Message, max int) []Message {
	if max <= 0 || len(messages) <= max {
		out := make([]Message, len(messages))
		copy(out, messages)
		return out
	}
	start := len(messages) - max
	// Never start on a tool result message - scan forward to find a safe boundary.
	for start < len(messages) && messages[start].Role == ai.RoleToolResult {
		start++
	}
	// If we landed on an assistant message that contains tool calls, skip past the
	// assistant + subsequent tool results to avoid orphaning tool call/result pairs.
	// Plain text assistant messages are safe truncation points.
	if start < len(messages) && messages[start].Role == ai.RoleAssistant && assistantHasToolCalls(messages[start]) {
		for start < len(messages) && messages[start].Role != ai.RoleUser {
			start++
		}
	}
	if start >= len(messages) {
		return nil
	}
	out := make([]Message, len(messages[start:]))
	copy(out, messages[start:])
	return out
}

func assistantHasToolCalls(msg Message) bool {
	blocks, ok := msg.ContentBlocks()
	if !ok {
		return false
	}
	for _, b := range blocks {
		if b.Type == ai.ContentTypeToolCall {
			return true
		}
	}
	return false
}
