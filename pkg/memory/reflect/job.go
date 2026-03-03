package reflect

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	memfiles "github.com/bstncartwright/gopher/pkg/memory/files"
)

type JobOptions struct {
	Files         *memfiles.Manager
	MaxDailyFiles int
	Now           func() time.Time
}

type Job struct {
	files         *memfiles.Manager
	maxDailyFiles int
	now           func() time.Time
}

type Result struct {
	Appended int
	Anchors  []string
}

func NewJob(opts JobOptions) *Job {
	maxFiles := opts.MaxDailyFiles
	if maxFiles <= 0 {
		maxFiles = 7
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Job{files: opts.Files, maxDailyFiles: maxFiles, now: nowFn}
}

func (j *Job) Run(ctx context.Context) (Result, error) {
	if j == nil || j.files == nil {
		return Result{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	memoryDir := j.files.MemoryRoot()
	entries, err := os.ReadDir(memoryDir)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{}, nil
		}
		return Result{}, fmt.Errorf("list memory directory: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		if _, err := time.Parse("2006-01-02", strings.TrimSuffix(name, ".md")); err != nil {
			continue
		}
		paths = append(paths, filepath.Join(memoryDir, name))
	}
	sort.Strings(paths)
	if len(paths) > j.maxDailyFiles {
		paths = paths[len(paths)-j.maxDailyFiles:]
	}
	if len(paths) == 0 {
		return Result{}, nil
	}

	target := j.files.MemoryDocPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Result{}, fmt.Errorf("create memory doc dir: %w", err)
	}
	if _, err := os.Stat(target); os.IsNotExist(err) {
		if err := os.WriteFile(target, []byte("# MEMORY\n\n"), 0o644); err != nil {
			return Result{}, fmt.Errorf("seed memory doc: %w", err)
		}
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return Result{}, fmt.Errorf("read memory doc: %w", err)
	}
	currentText := string(current)

	anchors := make([]string, 0, 16)
	linesToAppend := make([]string, 0, 16)
	for _, path := range paths {
		facts, err := extractFacts(path)
		if err != nil {
			continue
		}
		for _, fact := range facts {
			anchor := fmt.Sprintf("%s:%d", path, fact.line)
			if strings.Contains(currentText, anchor) {
				continue
			}
			confidence := "0.80"
			if fact.kind == "preference" {
				confidence = "0.65"
			}
			entry := fmt.Sprintf("- [%s confidence=%s source=%s] %s", fact.kind, confidence, anchor, fact.text)
			linesToAppend = append(linesToAppend, entry)
			anchors = append(anchors, anchor)
		}
	}
	if len(linesToAppend) == 0 {
		return Result{}, nil
	}
	section := "\n## Reflection " + j.now().UTC().Format("2006-01-02") + "\n" + strings.Join(linesToAppend, "\n") + "\n"
	f, err := os.OpenFile(target, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("append memory doc: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(section); err != nil {
		return Result{}, fmt.Errorf("write reflection section: %w", err)
	}
	return Result{Appended: len(linesToAppend), Anchors: anchors}, nil
}

type extractedFact struct {
	line int
	kind string
	text string
}

func extractFacts(path string) ([]extractedFact, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	facts := make([]extractedFact, 0, 8)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		kind := "fact"
		switch {
		case strings.Contains(lower, "prefer") || strings.Contains(lower, "likes") || strings.Contains(lower, "dislike"):
			kind = "preference"
		case strings.Contains(lower, "decision") || strings.Contains(lower, "decide"):
			kind = "decision"
		case strings.Contains(lower, "constraint") || strings.Contains(lower, "must") || strings.Contains(lower, "always"):
			kind = "constraint"
		default:
			continue
		}
		facts = append(facts, extractedFact{line: lineNo, kind: kind, text: strings.TrimPrefix(text, "- ")})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return facts, nil
}
