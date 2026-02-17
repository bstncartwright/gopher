package ingest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type ExtractorOptions struct {
	Summarizer *DeterministicSummarizer
	Now        func() time.Time
}

type Extractor struct {
	summarizer *DeterministicSummarizer
	now        func() time.Time
}

func NewExtractor(opts ExtractorOptions) *Extractor {
	summarizer := opts.Summarizer
	if summarizer == nil {
		summarizer = NewDeterministicSummarizer(4)
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Extractor{summarizer: summarizer, now: nowFn}
}

func (e *Extractor) ExtractSession(sessionID string, agentID string, events []sessionrt.Event) []memory.MemoryRecord {
	sessionID = strings.TrimSpace(sessionID)
	agentID = strings.TrimSpace(agentID)
	if sessionID == "" {
		for _, event := range events {
			if strings.TrimSpace(string(event.SessionID)) != "" {
				sessionID = string(event.SessionID)
				break
			}
		}
	}
	if len(events) == 0 {
		return nil
	}

	records := make([]memory.MemoryRecord, 0, 16)
	userMessages := make([]string, 0, 8)
	agentMessages := make([]string, 0, 8)
	successfulTools := make([]string, 0, 4)
	pendingCalls := make([]toolCall, 0, 4)
	seenToolIDs := map[string]struct{}{}
	seenSemanticIDs := map[string]struct{}{}

	for _, event := range events {
		if agentID == "" {
			if role, ok := messageRole(event.Payload); ok && role == sessionrt.RoleAgent {
				agentID = string(event.From)
			}
		}
		scope := scopeForAgent(agentID)

		switch event.Type {
		case sessionrt.EventMessage:
			msg, ok := payloadToMessage(event.Payload)
			if !ok {
				continue
			}
			content := strings.TrimSpace(msg.Content)
			if content == "" {
				continue
			}
			switch msg.Role {
			case sessionrt.RoleUser:
				userMessages = append(userMessages, content)
				if fact, isFact := parseExplicitFact(content); isFact {
					id := fmt.Sprintf("semantic:%s:%d", sessionID, event.Seq)
					if _, exists := seenSemanticIDs[id]; exists {
						continue
					}
					seenSemanticIDs[id] = struct{}{}
					records = append(records, memory.MemoryRecord{
						ID:         id,
						Type:       memory.MemorySemantic,
						Scope:      scope,
						SessionID:  sessionID,
						AgentID:    agentID,
						Content:    fact,
						Metadata:   map[string]string{"kind": "explicit_fact", "source": "user"},
						Importance: 0.95,
						Timestamp:  chooseTimestamp(event.Timestamp, e.now),
					})
				}
			case sessionrt.RoleAgent:
				agentMessages = append(agentMessages, content)
			}

		case sessionrt.EventStatePatch:
			id := fmt.Sprintf("semantic-state:%s:%d", sessionID, event.Seq)
			if _, exists := seenSemanticIDs[id]; exists {
				continue
			}
			seenSemanticIDs[id] = struct{}{}
			records = append(records, memory.MemoryRecord{
				ID:         id,
				Type:       memory.MemorySemantic,
				Scope:      scope,
				SessionID:  sessionID,
				AgentID:    agentID,
				Content:    "State update: " + compactJSON(event.Payload),
				Metadata:   map[string]string{"kind": "state_patch"},
				Importance: 0.85,
				Timestamp:  chooseTimestamp(event.Timestamp, e.now),
			})

		case sessionrt.EventToolCall:
			name, args := payloadToToolCall(event.Payload)
			if name == "" {
				continue
			}
			pendingCalls = append(pendingCalls, toolCall{
				seq:  event.Seq,
				name: name,
				args: args,
			})

		case sessionrt.EventToolResult:
			name, status, result := payloadToToolResult(event.Payload)
			if name == "" {
				continue
			}
			callIndex := indexOfPendingCall(pendingCalls, name)
			args := map[string]any{}
			if callIndex >= 0 {
				args = pendingCalls[callIndex].args
				pendingCalls = append(pendingCalls[:callIndex], pendingCalls[callIndex+1:]...)
			}

			recordID := fmt.Sprintf("tool:%s:%d", sessionID, event.Seq)
			if _, exists := seenToolIDs[recordID]; exists {
				continue
			}
			seenToolIDs[recordID] = struct{}{}

			toolContent := e.summarizer.SummarizeToolExperience(name, args, status, result)
			records = append(records, memory.MemoryRecord{
				ID:        recordID,
				Type:      memory.MemoryTool,
				Scope:     scope,
				SessionID: sessionID,
				AgentID:   agentID,
				Content:   toolContent,
				Metadata: map[string]string{
					"tool":   name,
					"status": status,
				},
				Importance: importanceForToolStatus(status),
				Timestamp:  chooseTimestamp(event.Timestamp, e.now),
			})
			if strings.EqualFold(status, "ok") {
				successfulTools = append(successfulTools, name)
			}
		}
	}

	episodicSummary := strings.TrimSpace(e.summarizer.SummarizeSession(userMessages, agentMessages, len(events)))
	if episodicSummary != "" {
		lastSeq := events[len(events)-1].Seq
		records = append(records, memory.MemoryRecord{
			ID:         fmt.Sprintf("episodic:%s:%d", sessionID, lastSeq),
			Type:       memory.MemoryEpisodic,
			Scope:      scopeForAgent(agentID),
			SessionID:  sessionID,
			AgentID:    agentID,
			Content:    episodicSummary,
			Metadata:   map[string]string{"kind": "session_summary"},
			Importance: 0.72,
			Timestamp:  chooseTimestamp(events[len(events)-1].Timestamp, e.now),
		})
	}

	if len(successfulTools) > 0 {
		toolsOrdered := uniqueOrdered(successfulTools)
		procedure := strings.TrimSpace(e.summarizer.SummarizeProcedure(lastOrEmpty(userMessages), toolsOrdered, lastOrEmpty(agentMessages)))
		if procedure != "" {
			lastSeq := events[len(events)-1].Seq
			records = append(records, memory.MemoryRecord{
				ID:         fmt.Sprintf("procedural:%s:%d", sessionID, lastSeq),
				Type:       memory.MemoryProcedural,
				Scope:      scopeForAgent(agentID),
				SessionID:  sessionID,
				AgentID:    agentID,
				Content:    procedure,
				Metadata:   map[string]string{"kind": "successful_workflow"},
				Importance: 0.82,
				Timestamp:  chooseTimestamp(events[len(events)-1].Timestamp, e.now),
			})
		}
	}

	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].Timestamp.Equal(records[j].Timestamp) {
			return records[i].Timestamp.Before(records[j].Timestamp)
		}
		return records[i].ID < records[j].ID
	})
	return records
}

type toolCall struct {
	seq  uint64
	name string
	args map[string]any
}

func scopeForAgent(agentID string) memory.MemoryScope {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return memory.ScopeGlobal
	}
	return memory.AgentScope(agentID)
}

func chooseTimestamp(ts time.Time, nowFn func() time.Time) time.Time {
	if ts.IsZero() {
		if nowFn == nil {
			return time.Now().UTC()
		}
		return nowFn().UTC()
	}
	return ts.UTC()
}

func payloadToMessage(payload any) (sessionrt.Message, bool) {
	switch v := payload.(type) {
	case sessionrt.Message:
		return v, true
	case map[string]any:
		roleRaw, ok := v["role"].(string)
		if !ok {
			return sessionrt.Message{}, false
		}
		content, ok := v["content"].(string)
		if !ok {
			return sessionrt.Message{}, false
		}
		return sessionrt.Message{Role: sessionrt.Role(roleRaw), Content: content}, true
	default:
		return sessionrt.Message{}, false
	}
}

func messageRole(payload any) (sessionrt.Role, bool) {
	msg, ok := payloadToMessage(payload)
	if !ok {
		return "", false
	}
	return msg.Role, true
}

func parseExplicitFact(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "remember:") {
		fact := strings.TrimSpace(trimmed[len("remember:"):])
		if fact != "" {
			return fact, true
		}
	}
	return "", false
}

func payloadToToolCall(payload any) (string, map[string]any) {
	value, ok := payload.(map[string]any)
	if !ok {
		return "", nil
	}
	name, _ := value["name"].(string)
	args, _ := value["args"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	return strings.TrimSpace(name), cloneAnyMap(args)
}

func payloadToToolResult(payload any) (name string, status string, result any) {
	value, ok := payload.(map[string]any)
	if !ok {
		return "", "", nil
	}
	name, _ = value["name"].(string)
	status, _ = value["status"].(string)
	result = value["result"]
	if status == "" {
		status = "ok"
	}
	return strings.TrimSpace(name), status, result
}

func importanceForToolStatus(status string) float64 {
	if strings.EqualFold(strings.TrimSpace(status), "ok") {
		return 0.74
	}
	return 0.45
}

func indexOfPendingCall(calls []toolCall, name string) int {
	for i := range calls {
		if calls[i].name == name {
			return i
		}
	}
	return -1
}

func uniqueOrdered(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func lastOrEmpty(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(items[len(items)-1])
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		switch nested := value.(type) {
		case map[string]any:
			out[key] = cloneAnyMap(nested)
		case []any:
			copySlice := make([]any, len(nested))
			copy(copySlice, nested)
			out[key] = copySlice
		default:
			out[key] = value
		}
	}
	return out
}

func compactJSON(value any) string {
	blob, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(blob)
}
