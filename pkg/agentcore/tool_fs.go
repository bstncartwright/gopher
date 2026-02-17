package agentcore

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
		return ToolOutput{Status: ToolStatusError}, err
	}

	f, err := os.Open(path)
	if err != nil {
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
		return readRange(f, path, offset, hasOffset, limit, hasLimit)
	}
	return readFull(f, path)
}

func readRange(f *os.File, path string, offset int, hasOffset bool, limit int, hasLimit bool) (ToolOutput, error) {
	startLine := 1
	if hasOffset && offset > 1 {
		startLine = offset
	}

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
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}

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

	blob, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil {
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}

	truncated := len(blob) > maxBytes
	if truncated {
		blob = blob[:maxBytes]
	}

	rawLines := strings.Split(string(blob), "\n")
	numbered := make([]string, len(rawLines))
	for i, line := range rawLines {
		numbered[i] = fmt.Sprintf("%6d|%s", i+1, line)
	}

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
		return ToolOutput{Status: ToolStatusError}, err
	}
	content, err := requiredStringArg(input.Args, "content")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"path": path, "error": err.Error()}}, err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"path": path, "error": err.Error()}}, err
	}

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
