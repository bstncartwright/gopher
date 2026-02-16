package agentcore

import (
	"fmt"
	"sync/atomic"
	"time"
)

var sessionCounter uint64

func (a *Agent) NewSession() *Session {
	id := fmt.Sprintf("s-%d-%d", time.Now().UTC().UnixNano(), atomic.AddUint64(&sessionCounter, 1))
	return &Session{
		ID:           id,
		Messages:     make([]Message, 0, a.Config.MaxContextMessages),
		WorkingState: map[string]any{},
	}
}

func boundMessages(messages []Message, max int) []Message {
	if max <= 0 || len(messages) <= max {
		out := make([]Message, len(messages))
		copy(out, messages)
		return out
	}
	start := len(messages) - max
	out := make([]Message, len(messages[start:]))
	copy(out, messages[start:])
	return out
}
