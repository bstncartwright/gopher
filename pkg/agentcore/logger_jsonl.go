package agentcore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type JSONLEventLogger struct {
	path string
	mu   sync.Mutex
}

func NewJSONLEventLogger(path string) *JSONLEventLogger {
	return &JSONLEventLogger{path: path}
}

func (l *JSONLEventLogger) Append(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}

	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer file.Close()

	blob, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := file.Write(append(blob, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}
