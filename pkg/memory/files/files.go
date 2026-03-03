package files

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultDailyTitlePrefix = "# Daily Memory - "
	defaultMemoryDocName    = "MEMORY.md"
)

var dailyMemoryFilePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.md$`)

type Manager struct {
	workspace string
	location  *time.Location
	now       func() time.Time
}

type Window struct {
	Path      string
	StartLine int
	EndLine   int
	Text      string
}

func NewManager(workspace string, location *time.Location, nowFn func() time.Time) *Manager {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if nowFn == nil {
		nowFn = time.Now
	}
	if location == nil {
		location = time.Local
	}
	return &Manager{
		workspace: workspace,
		location:  location,
		now:       nowFn,
	}
}

func (m *Manager) Workspace() string {
	if m == nil {
		return ""
	}
	return m.workspace
}

func (m *Manager) MemoryRoot() string {
	if m == nil {
		return ""
	}
	return filepath.Join(m.workspace, "memory")
}

func (m *Manager) MemoryDocPath() string {
	if m == nil {
		return ""
	}
	return filepath.Join(m.workspace, defaultMemoryDocName)
}

func (m *Manager) DailyPathForTime(at time.Time) string {
	if m == nil {
		return ""
	}
	if at.IsZero() {
		at = m.now()
	}
	at = at.In(m.location)
	return filepath.Join(m.MemoryRoot(), at.Format("2006-01-02")+".md")
}

func (m *Manager) EnsureDailyFile(now time.Time) (string, error) {
	if m == nil {
		return "", fmt.Errorf("memory files manager is nil")
	}
	path := m.DailyPathForTime(now)
	if path == "" {
		return "", fmt.Errorf("daily memory path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create memory directory: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return path, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat daily memory file: %w", err)
	}

	date := strings.TrimSuffix(filepath.Base(path), ".md")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return path, nil
		}
		return "", fmt.Errorf("create daily memory file: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(defaultDailyMemoryNote(date)); err != nil {
		return "", fmt.Errorf("seed daily memory file: %w", err)
	}
	return path, nil
}

func (m *Manager) AppendDailyEntry(text string) (Window, error) {
	if m == nil {
		return Window{}, fmt.Errorf("memory files manager is nil")
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Window{}, nil
	}
	path, err := m.EnsureDailyFile(m.now())
	if err != nil {
		return Window{}, err
	}
	startLine, err := lineCount(path)
	if err != nil {
		return Window{}, err
	}
	entry := formatDailyEntry(trimmed, m.now().In(m.location))
	if err := appendText(path, entry); err != nil {
		return Window{}, err
	}
	linesAdded := countLines(entry)
	return Window{
		Path:      path,
		StartLine: startLine + 1,
		EndLine:   startLine + linesAdded,
		Text:      strings.TrimRight(entry, "\n"),
	}, nil
}

func (m *Manager) AppendOrUpsertMemoryFact(text string, section string) (Window, error) {
	if m == nil {
		return Window{}, fmt.Errorf("memory files manager is nil")
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return Window{}, nil
	}
	path := m.MemoryDocPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Window{}, fmt.Errorf("create memory doc directory: %w", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		seed := "# MEMORY\n\n"
		if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
			return Window{}, fmt.Errorf("seed memory doc: %w", err)
		}
	} else if err != nil {
		return Window{}, fmt.Errorf("stat memory doc: %w", err)
	}

	current, err := os.ReadFile(path)
	if err != nil {
		return Window{}, fmt.Errorf("read memory doc: %w", err)
	}
	if strings.Contains(strings.ToLower(string(current)), strings.ToLower(trimmed)) {
		return Window{Path: path, StartLine: 0, EndLine: 0, Text: ""}, nil
	}

	startLine, err := lineCount(path)
	if err != nil {
		return Window{}, err
	}
	sectionTitle := strings.TrimSpace(section)
	if sectionTitle == "" {
		sectionTitle = "Facts"
	}
	body := "\n## " + sectionTitle + "\n- " + trimmed + "\n"
	if err := appendText(path, body); err != nil {
		return Window{}, err
	}
	linesAdded := countLines(body)
	return Window{
		Path:      path,
		StartLine: startLine + 1,
		EndLine:   startLine + linesAdded,
		Text:      strings.TrimRight(body, "\n"),
	}, nil
}

func (m *Manager) SafeReadWindow(path string, from int, lines int) (Window, error) {
	if m == nil {
		return Window{}, fmt.Errorf("memory files manager is nil")
	}
	resolved, ok := m.ResolveMemoryPath(path)
	if !ok {
		return Window{}, nil
	}
	f, err := os.Open(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return Window{Path: resolved}, nil
		}
		return Window{}, fmt.Errorf("open memory path: %w", err)
	}
	defer f.Close()

	if from <= 0 {
		from = 1
	}
	if lines <= 0 {
		lines = 40
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var (
		lineNo   int
		selected []string
	)
	for scanner.Scan() {
		lineNo++
		if lineNo < from {
			continue
		}
		if len(selected) >= lines {
			break
		}
		selected = append(selected, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return Window{}, fmt.Errorf("scan memory file: %w", err)
	}
	end := from + len(selected) - 1
	if len(selected) == 0 {
		end = from - 1
	}
	return Window{
		Path:      resolved,
		StartLine: from,
		EndLine:   end,
		Text:      strings.Join(selected, "\n"),
	}, nil
}

func (m *Manager) ResolveMemoryPath(path string) (string, bool) {
	if m == nil {
		return "", false
	}
	raw := strings.TrimSpace(path)
	if raw == "" {
		return "", false
	}
	clean := filepath.Clean(raw)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(m.workspace, clean)
	}
	clean = filepath.Clean(clean)
	base := filepath.Base(clean)
	if strings.EqualFold(clean, m.MemoryDocPath()) {
		return clean, true
	}
	if strings.EqualFold(filepath.Base(filepath.Dir(clean)), "memory") && dailyMemoryFilePattern.MatchString(base) {
		return clean, true
	}
	return "", false
}

func defaultDailyMemoryNote(date string) string {
	return defaultDailyTitlePrefix + date + "\n\n"
}

func formatDailyEntry(text string, at time.Time) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 {
		return ""
	}
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return fmt.Sprintf("## %s\n- %s\n\n", at.Format("15:04:05"), strings.Join(lines, "\n- "))
}

func appendText(path string, body string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open for append %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(body); err != nil {
		return fmt.Errorf("append to %s: %w", path, err)
	}
	return nil
}

func lineCount(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	count := 0
	for scanner.Scan() {
		count++
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("scan %s: %w", path, err)
	}
	return count, nil
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n")
}
