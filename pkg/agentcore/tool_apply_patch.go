package agentcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type applyPatchTool struct{}

func (t *applyPatchTool) Name() string {
	return "apply_patch"
}

func (t *applyPatchTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Apply structured patches across one or more files. Use for multi-hunk edits.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{"type": "string"},
			},
			"required": []any{"input"},
		},
	}
}

type patchOpKind int

const (
	patchOpAdd patchOpKind = iota
	patchOpUpdate
	patchOpDelete
)

type patchOp struct {
	kind  patchOpKind
	path  string
	lines []string // +lines for Add; unused for Delete
	hunks []patchHunk // for Update only
}

type patchHunk struct {
	oldLines []string
	newLines []string
}

func (t *applyPatchTool) Run(_ context.Context, input ToolInput) (ToolOutput, error) {
	raw, err := requiredStringArg(input.Args, "input")
	if err != nil {
		return ToolOutput{Status: ToolStatusError}, err
	}

	ops, err := parsePatch(raw)
	if err != nil {
		return ToolOutput{
			Status: ToolStatusError,
			Result: map[string]any{"error": err.Error()},
		}, err
	}

	var files []string

	for _, op := range ops {
		absPath, err := input.Agent.ResolvePath(op.path)
		if err != nil {
			return patchErr(op.path, err.Error())
		}
		files = append(files, op.path)

		switch op.kind {
		case patchOpAdd:
			if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
				return patchErr(op.path, err.Error())
			}
			content := strings.Join(op.lines, "\n")
			if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
				return patchErr(op.path, err.Error())
			}

		case patchOpUpdate:
			data, err := os.ReadFile(absPath)
			if err != nil {
				return patchErr(op.path, err.Error())
			}
			current := string(data)
			for _, hunk := range op.hunks {
				oldText := strings.Join(hunk.oldLines, "\n")
				newText := strings.Join(hunk.newLines, "\n")
				idx := strings.Index(current, oldText)
				if idx == -1 {
					msg := fmt.Sprintf("hunk not found in %s: old text %q", op.path, oldText)
					return patchErr(op.path, msg)
				}
				current = current[:idx] + newText + current[idx+len(oldText):]
			}
			if err := os.WriteFile(absPath, []byte(current), 0o644); err != nil {
				return patchErr(op.path, err.Error())
			}

		case patchOpDelete:
			if err := os.Remove(absPath); err != nil {
				return patchErr(op.path, err.Error())
			}
		}
	}

	return ToolOutput{
		Status: ToolStatusOK,
		Result: map[string]any{
			"applied": len(ops),
			"files":   files,
		},
	}, nil
}

func patchErr(file, msg string) (ToolOutput, error) {
	return ToolOutput{
		Status: ToolStatusError,
		Result: map[string]any{
			"error": msg,
			"file":  file,
		},
	}, fmt.Errorf("%s", msg)
}

func parsePatch(raw string) ([]patchOp, error) {
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty patch input")
	}

	// Trim leading/trailing blank lines
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) < 2 {
		return nil, fmt.Errorf("patch must start with '*** Begin Patch' and end with '*** End Patch'")
	}
	if strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with '*** Begin Patch', got %q", lines[0])
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return nil, fmt.Errorf("patch must end with '*** End Patch', got %q", lines[len(lines)-1])
	}

	var ops []patchOp
	i := 1
	end := len(lines) - 1

	for i < end {
		line := lines[i]

		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			path := strings.TrimPrefix(line, "*** Add File: ")
			i++
			var addLines []string
			for i < end && !strings.HasPrefix(lines[i], "*** ") {
				if strings.HasPrefix(lines[i], "+") {
					addLines = append(addLines, lines[i][1:])
				}
				i++
			}
			ops = append(ops, patchOp{kind: patchOpAdd, path: path, lines: addLines})

		case strings.HasPrefix(line, "*** Update File: "):
			path := strings.TrimPrefix(line, "*** Update File: ")
			i++
			var hunks []patchHunk
			for i < end && !strings.HasPrefix(lines[i], "*** ") {
				if strings.TrimSpace(lines[i]) == "@@" {
					i++
					var hunk patchHunk
					for i < end && !strings.HasPrefix(lines[i], "*** ") && strings.TrimSpace(lines[i]) != "@@" {
						switch {
						case strings.HasPrefix(lines[i], "-"):
							hunk.oldLines = append(hunk.oldLines, lines[i][1:])
						case strings.HasPrefix(lines[i], "+"):
							hunk.newLines = append(hunk.newLines, lines[i][1:])
						}
						i++
					}
					hunks = append(hunks, hunk)
				} else {
					i++
				}
			}
			if len(hunks) == 0 {
				return nil, fmt.Errorf("Update File %q has no hunks", path)
			}
			ops = append(ops, patchOp{kind: patchOpUpdate, path: path, hunks: hunks})

		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimPrefix(line, "*** Delete File: ")
			ops = append(ops, patchOp{kind: patchOpDelete, path: path})
			i++

		default:
			i++
		}
	}

	if len(ops) == 0 {
		return nil, fmt.Errorf("patch contains no operations")
	}
	return ops, nil
}
