package agentcore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type FileMemoryStore struct {
	path string
}

func NewFileMemoryStore(path string) *FileMemoryStore {
	return &FileMemoryStore{path: path}
}

func (m *FileMemoryStore) LoadWorking() (map[string]any, error) {
	blob, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read working memory: %w", err)
	}
	if len(blob) == 0 {
		return map[string]any{}, nil
	}

	out := map[string]any{}
	if err := json.Unmarshal(blob, &out); err != nil {
		return nil, fmt.Errorf("decode working memory: %w", err)
	}
	return out, nil
}

func (m *FileMemoryStore) SaveWorking(working map[string]any) error {
	if working == nil {
		working = map[string]any{}
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return fmt.Errorf("create memory directory: %w", err)
	}
	blob, err := json.MarshalIndent(working, "", "  ")
	if err != nil {
		return fmt.Errorf("encode working memory: %w", err)
	}
	if err := os.WriteFile(m.path, blob, 0o644); err != nil {
		return fmt.Errorf("write working memory: %w", err)
	}
	return nil
}

func (m *FileMemoryStore) Exists() (bool, error) {
	_, err := os.Stat(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
