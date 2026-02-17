package ingest

import (
	"fmt"
	"strings"
)

type DeterministicSummarizer struct {
	MaxMessages int
}

func NewDeterministicSummarizer(maxMessages int) *DeterministicSummarizer {
	if maxMessages <= 0 {
		maxMessages = 4
	}
	return &DeterministicSummarizer{MaxMessages: maxMessages}
}

func (s *DeterministicSummarizer) SummarizeSession(userMessages, agentMessages []string, eventCount int) string {
	user := tail(userMessages, s.MaxMessages)
	agent := tail(agentMessages, s.MaxMessages)
	parts := []string{
		fmt.Sprintf("Session event count: %d", eventCount),
	}
	if len(user) > 0 {
		parts = append(parts, "User intents: "+strings.Join(user, " | "))
	}
	if len(agent) > 0 {
		parts = append(parts, "Agent outcomes: "+strings.Join(agent, " | "))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (s *DeterministicSummarizer) SummarizeToolExperience(name string, args map[string]any, status string, result any) string {
	tool := strings.TrimSpace(name)
	if tool == "" {
		tool = "unknown"
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = "ok"
	}
	argsBlob := compactJSON(args)
	resultBlob := compactJSON(result)
	return fmt.Sprintf("Tool %s finished with status=%s. args=%s result=%s", tool, status, argsBlob, resultBlob)
}

func (s *DeterministicSummarizer) SummarizeProcedure(userMessage string, tools []string, outcome string) string {
	userMessage = strings.TrimSpace(userMessage)
	outcome = strings.TrimSpace(outcome)
	toolOrder := strings.Join(tools, " -> ")
	if toolOrder == "" {
		toolOrder = "none"
	}

	parts := []string{"Procedure"}
	if userMessage != "" {
		parts = append(parts, "Task: "+userMessage)
	}
	parts = append(parts, "Tool sequence: "+toolOrder)
	if outcome != "" {
		parts = append(parts, "Outcome: "+outcome)
	}
	return strings.Join(parts, "\n")
}

func tail(items []string, max int) []string {
	if len(items) == 0 {
		return nil
	}
	if max <= 0 || len(items) <= max {
		out := make([]string, 0, len(items))
		for _, item := range items {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			out = append(out, item)
		}
		return out
	}
	start := len(items) - max
	out := make([]string, 0, max)
	for _, item := range items[start:] {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}
