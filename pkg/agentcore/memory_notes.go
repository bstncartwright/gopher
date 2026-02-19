package agentcore

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var dailyMemoryFilePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.md$`)

func ensureDailyMemoryNote(path string) (bool, error) {
	clean := filepath.Clean(path)
	if !strings.EqualFold(filepath.Base(filepath.Dir(clean)), "memory") {
		return false, nil
	}
	fileName := filepath.Base(clean)
	if !dailyMemoryFilePattern.MatchString(fileName) {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return false, fmt.Errorf("create memory directory: %w", err)
	}

	date := strings.TrimSuffix(fileName, ".md")
	file, err := os.OpenFile(clean, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("create daily memory note: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(defaultDailyMemoryNote(date)); err != nil {
		return false, fmt.Errorf("write daily memory note: %w", err)
	}
	return true, nil
}

func defaultDailyMemoryNote(date string) string {
	return fmt.Sprintf("# Daily Memory - %s\n\n", date)
}
