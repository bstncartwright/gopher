package agentcore

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

type readTool struct{}

func (t *readTool) Name() string {
	return "read"
}

func (t *readTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Read a UTF-8 text file from the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string"},
				"offset": map[string]any{"type": "integer"},
				"limit":  map[string]any{"type": "integer"},
			},
			"required": []any{"path"},
		},
	}
}

func (t *readTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	path, err := requiredStringArg(input.Args, "path")
	if err != nil {
		slog.Error("read_tool: path arg required")
		return ToolOutput{Status: ToolStatusError}, err
	}

	slog.Debug("read_tool: reading file", "path", path, "session_id", input.Session.ID)

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			if created, createErr := ensureDailyMemoryNote(path); createErr != nil {
				slog.Error("read_tool: failed to scaffold daily memory note", "path", path, "error", createErr)
			} else if created {
				f, err = os.Open(path)
			}
		}
	}
	if err != nil {
		slog.Error("read_tool: failed to open file", "path", path, "error", err)
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}
	defer f.Close()

	var offset, limit int
	var hasOffset, hasLimit bool
	if raw, exists := input.Args["offset"]; exists {
		if v, ok := toInt(raw); ok {
			offset = v
			hasOffset = true
		}
	}
	if raw, exists := input.Args["limit"]; exists {
		if v, ok := toInt(raw); ok {
			limit = v
			hasLimit = true
		}
	}

	if hasOffset || hasLimit {
		slog.Debug("read_tool: reading range", "path", path, "offset", offset, "limit", limit)
		return readRange(f, path, offset, hasOffset, limit, hasLimit)
	}
	slog.Debug("read_tool: reading full file", "path", path)
	return readFull(f, path)
}

func readRange(f *os.File, path string, offset int, hasOffset bool, limit int, hasLimit bool) (ToolOutput, error) {
	startLine := 1
	if hasOffset && offset > 1 {
		startLine = offset
	}

	slog.Debug("read_tool: scanning file range", "path", path, "start_line", startLine)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var lines []string
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if !hasLimit || len(lines) < limit {
			lines = append(lines, fmt.Sprintf("%6d|%s", lineNum, scanner.Text()))
		}
		// keep scanning to EOF so lineNum reflects actual total line count
	}
	if err := scanner.Err(); err != nil {
		slog.Error("read_tool: scanner error", "path", path, "error", err)
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}

	slog.Debug("read_tool: range read complete", "path", path, "lines_returned", len(lines), "total_lines", lineNum)
	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{
			"path":        path,
			"content":     strings.Join(lines, "\n"),
			"total_lines": lineNum,
		},
	}, nil
}

func readFull(f *os.File, path string) (ToolOutput, error) {
	const maxBytes = 10 << 20 // 10 MiB

	slog.Debug("read_tool: reading full file", "path", path, "max_bytes", maxBytes)
	blob, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		slog.Error("read_tool: failed to read file", "path", path, "error", err)
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}

	truncated := len(blob) > maxBytes
	if truncated {
		blob = blob[:maxBytes]
		slog.Warn("read_tool: file truncated", "path", path, "max_bytes", maxBytes)
	}

	rawLines := strings.Split(string(blob), "\n")
	numbered := make([]string, len(rawLines))
	for i, line := range rawLines {
		numbered[i] = fmt.Sprintf("%6d|%s", i+1, line)
	}

	slog.Debug("read_tool: full read complete", "path", path, "total_lines", len(rawLines), "truncated", truncated)
	result := map[string]any{
		"path":        path,
		"content":     strings.Join(numbered, "\n"),
		"total_lines": len(rawLines),
	}
	if truncated {
		result["truncated"] = true
	}
	return ToolOutput{Status: ToolStatusOK, Result: result}, nil
}

type writeTool struct{}

func (t *writeTool) Name() string {
	return "write"
}

func (t *writeTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Write UTF-8 text content to a file in the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []any{"path", "content"},
		},
	}
}

func (t *writeTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	path, err := requiredStringArg(input.Args, "path")
	if err != nil {
		slog.Error("write_tool: path arg required")
		return ToolOutput{Status: ToolStatusError}, err
	}
	content, err := requiredStringArg(input.Args, "content")
	if err != nil {
		slog.Error("write_tool: content arg required")
		return ToolOutput{Status: ToolStatusError}, err
	}

	slog.Debug("write_tool: writing file", "path", path, "content_length", len(content), "session_id", input.Session.ID)

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Error("write_tool: failed to create parent dirs", "path", path, "error", err)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"path": path, "error": err.Error()}}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		slog.Error("write_tool: failed to write file", "path", path, "error", err)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"path": path, "error": err.Error()}}, err
	}

	slog.Info("write_tool: file written", "path", path, "bytes_written", len(content))
	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{
			"path":          path,
			"bytes_written": len(content),
		},
	}, nil
}

func formatToolResultText(output ToolOutput) string {
	if output.Result == nil {
		return ""
	}
	blob, err := marshalStableJSON(output.Result)
	if err != nil {
		return fmt.Sprintf("%v", output.Result)
	}
	return string(blob)
}

func formatToolResultTextForContext(output ToolOutput, cfg ContextManagementConfig) (string, bool) {
	text := formatToolResultText(output)
	if text == "" {
		return "", false
	}

	maxChars := cfg.ToolResultContextMaxCharsValue()
	headChars := cfg.ToolResultContextHeadCharsValue()
	tailChars := cfg.ToolResultContextTailCharsValue()
	if maxChars <= 0 || len(text) <= maxChars {
		return text, false
	}
	if headChars+tailChars > maxChars {
		headChars = maxChars / 2
		tailChars = maxChars - headChars
	}
	if headChars < 0 {
		headChars = 0
	}
	if tailChars < 0 {
		tailChars = 0
	}
	if len(text) < headChars {
		headChars = len(text)
	}
	if len(text) < tailChars {
		tailChars = len(text)
	}

	head := text[:headChars]
	tail := text[len(text)-tailChars:]
	envelope := map[string]any{
		"truncated":      true,
		"original_chars": len(text),
		"head":           head,
		"tail":           tail,
	}
	blob, err := marshalStableJSON(envelope)
	if err != nil {
		return text[:maxChars], true
	}
	return string(blob), true
}
