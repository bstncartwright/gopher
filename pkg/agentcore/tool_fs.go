package agentcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type fsReadTool struct{}

func (t *fsReadTool) Name() string {
	return "fs.read"
}

func (t *fsReadTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Read a UTF-8 text file from the workspace.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string"},
			},
			"required": []any{"path"},
		},
	}
}

func (t *fsReadTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	path, err := requiredStringArg(input.Args, "path")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"path": path, "error": err.Error()},
		}, err
	}
	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{
			"path":    path,
			"content": string(blob),
		},
	}, nil
}

type fsWriteTool struct{}

func (t *fsWriteTool) Name() string {
	return "fs.write"
}

func (t *fsWriteTool) Schema() ToolSchema {
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

func (t *fsWriteTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
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
