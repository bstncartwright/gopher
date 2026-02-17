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
	file *os.File
}

func NewJSONLEventLogger(path string) *JSONLEventLogger {
	return &JSONLEventLogger{path: path}
}

func (l *JSONLEventLogger) Append(event Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
			return fmt.Errorf("create log directory: %w", err)
		}
		f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		l.file = f
	}

	blob, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if _, err := l.file.Write(append(blob, '\n')); err != nil {
		return fmt.Errorf("write event: %w", err)
	}

	return nil
}

func (l *JSONLEventLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		err := l.file.Close()
		l.file = nil
		return err
	}
	return nil
}
