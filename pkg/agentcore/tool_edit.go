package agentcore

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type editTool struct{}

func (t *editTool) Name() string {
	return "edit"
}

func (t *editTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Edit a file by replacing an exact string match. Fails if the match is ambiguous or not found.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":     map[string]any{"type": "string"},
				"old_text": map[string]any{"type": "string"},
				"new_text": map[string]any{"type": "string"},
			},
			"required": []any{"path", "old_text", "new_text"},
		},
	}
}

func (t *editTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	path, err := requiredStringArg(input.Args, "path")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}
	oldText, err := requiredStringArg(input.Args, "old_text")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}
	newText, err := requiredStringArg(input.Args, "new_text")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}

	original := string(content)
	count := strings.Count(original, oldText)

	if count == 0 {
		err := fmt.Errorf("old_text not found in file")
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}
	if count > 1 {
		err := fmt.Errorf("old_text is ambiguous: found %d matches, provide more context", count)
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}

	updated := strings.Replace(original, oldText, newText, 1)

	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}

	snippet := extractSnippet(updated, strings.Index(updated, newText), len(newText))

	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{
			"path":            path,
			"old_text_length": len(oldText),
			"new_text_length": len(newText),
			"snippet":         snippet,
		},
	}, nil
}

func extractSnippet(content string, matchIndex, matchLen int) string {
	lines := strings.Split(content, "\n")
	charCount := 0
	startLine := 0
	for i, line := range lines {
		charCount += len(line) + 1
		if charCount > matchIndex {
			startLine = i
			break
		}
	}

	endLine := startLine
	charCount = 0
	for i, line := range lines {
		charCount += len(line) + 1
		if charCount > matchIndex+matchLen {
			endLine = i
			break
		}
	}

	const contextLines = 3
	from := startLine - contextLines
	if from < 0 {
		from = 0
	}
	to := endLine + contextLines + 1
	if to > len(lines) {
		to = len(lines)
	}

	return strings.Join(lines[from:to], "\n")
}
