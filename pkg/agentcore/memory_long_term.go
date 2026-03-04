package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
	"github.com/bstncartwright/gopher/pkg/memory"
)

type toolObservation struct {
	Name   string
	Args   map[string]any
	Status ToolStatus
	Result any
}

const turnMemoryPersistenceTimeout = 5 * time.Second

func (a *Agent) persistTurnMemories(ctx context.Context, s *Session, userMessage string, finalText string, tools []toolObservation, turnErr error) {
	if a == nil || s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc = func() {}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > turnMemoryPersistenceTimeout {
		ctx, cancel = context.WithTimeout(ctx, turnMemoryPersistenceTimeout)
	}
	defer cancel()

	if ctx.Err() != nil {
		timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), turnMemoryPersistenceTimeout)
		defer timeoutCancel()
		ctx = timeoutCtx
	}
	now := time.Now().UTC()
	scope := memory.AgentScope(a.ID)

	outcome := strings.TrimSpace(finalText)
	if turnErr != nil {
		if outcome == "" {
			outcome = "error: " + strings.TrimSpace(turnErr.Error())
		} else {
			outcome = outcome + " (error: " + strings.TrimSpace(turnErr.Error()) + ")"
		}
	}
	dailyWritten := false
	if a.MemoryFiles != nil && a.Config.Memory.EnabledValue() {
		lines := []string{}
		if strings.TrimSpace(userMessage) != "" {
			lines = append(lines, "Task: "+squeezeWhitespace(userMessage))
		}
		if strings.TrimSpace(outcome) != "" {
			lines = append(lines, "Outcome: "+squeezeWhitespace(outcome))
		}
		if len(tools) > 0 {
			toolParts := make([]string, 0, len(tools))
			for _, tool := range tools {
				if strings.TrimSpace(tool.Name) == "" {
					continue
				}
				toolParts = append(toolParts, strings.TrimSpace(tool.Name)+":"+strings.TrimSpace(string(tool.Status)))
			}
			if len(toolParts) > 0 {
				lines = append(lines, "Tools: "+strings.Join(toolParts, ", "))
			}
		}
		if len(lines) > 0 {
			if _, err := a.MemoryFiles.AppendDailyEntry(strings.Join(lines, "\n")); err == nil {
				dailyWritten = true
			}
		}
		if fact, ok := parseRememberFact(userMessage); ok {
			if _, err := a.MemoryFiles.AppendOrUpsertMemoryFact(fact, "Facts"); err == nil {
				dailyWritten = true
			}
		}
		if dailyWritten && a.MemorySearch != nil {
			_ = a.MemorySearch.Sync(ctx, false)
		}
	}
	if a.LongTermMemory == nil {
		return
	}

	if strings.TrimSpace(userMessage) != "" || outcome != "" {
		summary := fmt.Sprintf("Task: %s\nOutcome: %s", squeezeWhitespace(userMessage), squeezeWhitespace(outcome))
		importance := 0.76
		if turnErr != nil {
			importance = 0.52
		}
		a.storeLongTermMemoryRecord(ctx, memory.MemoryRecord{
			Type:      memory.MemoryEpisodic,
			Scope:     scope,
			SessionID: s.ID,
			AgentID:   a.ID,
			Content:   summary,
			Metadata: map[string]string{
				"kind":       "turn_summary",
				"tool_count": fmt.Sprintf("%d", len(tools)),
			},
			Importance: importance,
			Timestamp:  now,
		})
	}

	successfulToolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		status := strings.TrimSpace(string(tool.Status))
		if status == "" {
			status = string(ToolStatusOK)
		}
		if status == string(ToolStatusOK) {
			successfulToolNames = append(successfulToolNames, name)
		}
		content := fmt.Sprintf(
			"Tool %s finished with status=%s. args=%s result=%s",
			name,
			status,
			compactAny(tool.Args),
			compactAny(tool.Result),
		)
		importance := 0.74
		if !strings.EqualFold(status, string(ToolStatusOK)) {
			importance = 0.46
		}
		a.storeLongTermMemoryRecord(ctx, memory.MemoryRecord{
			Type:      memory.MemoryTool,
			Scope:     scope,
			SessionID: s.ID,
			AgentID:   a.ID,
			Content:   squeezeWhitespace(content),
			Metadata: map[string]string{
				"kind":   "turn_tool",
				"tool":   name,
				"status": status,
			},
			Importance: importance,
			Timestamp:  now,
		})
	}

	if turnErr == nil && len(successfulToolNames) > 0 && strings.TrimSpace(finalText) != "" {
		sequence := strings.Join(uniqueStrings(successfulToolNames), " -> ")
		procedure := fmt.Sprintf(
			"Procedure\nTask: %s\nTool sequence: %s\nOutcome: %s",
			squeezeWhitespace(userMessage),
			sequence,
			squeezeWhitespace(finalText),
		)
		a.storeLongTermMemoryRecord(ctx, memory.MemoryRecord{
			Type:       memory.MemoryProcedural,
			Scope:      scope,
			SessionID:  s.ID,
			AgentID:    a.ID,
			Content:    procedure,
			Metadata:   map[string]string{"kind": "turn_workflow"},
			Importance: 0.83,
			Timestamp:  now,
		})
	}

	if fact, ok := parseRememberFact(userMessage); ok {
		a.storeLongTermMemoryRecord(ctx, memory.MemoryRecord{
			Type:       memory.MemorySemantic,
			Scope:      scope,
			SessionID:  s.ID,
			AgentID:    a.ID,
			Content:    fact,
			Metadata:   map[string]string{"kind": "explicit_fact", "source": "user"},
			Importance: 0.95,
			Timestamp:  now,
		})
	}
}

func (a *Agent) storeLongTermMemoryRecord(ctx context.Context, record memory.MemoryRecord) {
	if a == nil || a.LongTermMemory == nil {
		return
	}
	if err := a.LongTermMemory.Store(ctx, record); err != nil {
		slog.Warn("agent_memory: failed to persist long-term memory record",
			"agent_id", a.ID,
			"session_id", record.SessionID,
			"type", record.Type.String(),
			"importance", record.Importance,
			"error", err,
		)
	}
}

func parseRememberFact(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	if !strings.HasPrefix(lower, "remember:") {
		return "", false
	}
	fact := strings.TrimSpace(trimmed[len("remember:"):])
	if fact == "" {
		return "", false
	}
	return fact, true
}

func compactAny(value any) string {
	if value == nil {
		return "null"
	}
	blob, err := json.Marshal(value)
	if err != nil {
		if text, ok := value.(string); ok {
			return text
		}
		return "{}"
	}
	text := string(blob)
	if len(text) > 300 {
		text = text[:297] + "..."
	}
	return text
}

func squeezeWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func uniqueStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func cloneAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	return ai.CloneMap(input)
}
