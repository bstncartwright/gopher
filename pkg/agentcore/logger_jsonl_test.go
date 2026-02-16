package agentcore

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONLEventLoggerAppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "events.jsonl")
	logger := NewJSONLEventLogger(path)

	first := Event{TS: "2026-01-01T00:00:00Z", SessionID: "s1", AgentID: "a1", Type: EventTypeAgentMsg, Payload: map[string]any{"text": "one"}}
	second := Event{TS: "2026-01-01T00:00:01Z", SessionID: "s1", AgentID: "a1", Type: EventTypeAgentMsg, Payload: map[string]any{"text": "two"}}

	if err := logger.Append(first); err != nil {
		t.Fatalf("append first: %v", err)
	}
	if err := logger.Append(second); err != nil {
		t.Fatalf("append second: %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log file: %v", err)
	}
	defer file.Close()

	lines := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan lines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "\"one\"") {
		t.Fatalf("first line mutated or missing payload: %s", lines[0])
	}
	if !strings.Contains(lines[1], "\"two\"") {
		t.Fatalf("second line missing payload: %s", lines[1])
	}
}
