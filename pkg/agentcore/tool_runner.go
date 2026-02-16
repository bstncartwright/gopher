package agentcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

const (
	ToolStatusOK     = "ok"
	ToolStatusError  = "error"
	ToolStatusDenied = "denied"
)

type PolicyError struct {
	Message string
}

func (e *PolicyError) Error() string {
	return e.Message
}

func IsPolicyError(err error) bool {
	var policyErr *PolicyError
	return errors.As(err, &policyErr)
}

type ToolRunner struct {
	agent *Agent
}

func NewToolRunner(agent *Agent) *ToolRunner {
	return &ToolRunner{agent: agent}
}

func (r *ToolRunner) Run(ctx context.Context, s *Session, call ai.ContentBlock) (ToolOutput, error) {
	tool, ok := r.agent.Tools.Get(call.Name)
	if !ok {
		err := fmt.Errorf("tool %q is not registered", call.Name)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	validatedArgs, err := ai.ValidateToolCall(toolSchemasToAITools(r.agent.Tools), call)
	if err != nil {
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	sanitizedArgs, err := r.enforcePolicy(call.Name, validatedArgs)
	if err != nil {
		return ToolOutput{Status: ToolStatusDenied, Result: map[string]any{"error": err.Error()}}, err
	}

	output, runErr := tool.Run(ctx, ToolInput{
		Agent:   r.agent,
		Session: s,
		Args:    sanitizedArgs,
	})
	if output.Status == "" {
		if runErr != nil {
			output.Status = ToolStatusError
		} else {
			output.Status = ToolStatusOK
		}
	}
	if runErr != nil && output.Result == nil {
		output.Result = map[string]any{"error": runErr.Error()}
	}

	return output, runErr
}

func (r *ToolRunner) enforcePolicy(name string, args map[string]any) (map[string]any, error) {
	out := cloneAnyMap(args)

	switch name {
	case "fs.read", "fs.write":
		rawPath, err := requiredStringArg(out, "path")
		if err != nil {
			return nil, err
		}
		resolved, err := r.resolvePathInAllowedRoots(rawPath)
		if err != nil {
			return nil, err
		}
		out["path"] = resolved
		return out, nil
	case "shell.exec":
		if !r.agent.Policies.CanShell {
			return nil, &PolicyError{Message: "shell.exec denied: policies.can_shell=false"}
		}

		command, err := requiredStringArg(out, "command")
		if err != nil {
			return nil, err
		}
		if !isInAllowlist(command, r.agent.Policies.ShellAllowlist) {
			return nil, &PolicyError{Message: fmt.Sprintf("shell.exec denied: command %q is not in shell_allowlist", command)}
		}

		cwd := r.agent.Workspace
		if rawCWD, ok := optionalStringArg(out, "cwd"); ok && rawCWD != "" {
			cwd = rawCWD
		}
		resolvedCWD, err := r.resolvePathInAllowedRoots(cwd)
		if err != nil {
			return nil, err
		}

		timeoutSeconds := 30
		if rawTimeout, exists := out["timeout_seconds"]; exists {
			if parsed, ok := toInt(rawTimeout); ok {
				timeoutSeconds = parsed
			}
		}
		if timeoutSeconds <= 0 {
			timeoutSeconds = 30
		}
		if timeoutSeconds > 600 {
			timeoutSeconds = 600
		}

		toolArgs := []string{}
		if rawArgs, exists := out["args"]; exists {
			parsed, err := parseStringSlice(rawArgs)
			if err != nil {
				return nil, err
			}
			toolArgs = parsed
		}

		out["command"] = command
		out["cwd"] = resolvedCWD
		out["timeout_seconds"] = timeoutSeconds
		out["args"] = toolArgs
		return out, nil
	default:
		return out, nil
	}
}

func (r *ToolRunner) resolvePathInAllowedRoots(rawPath string) (string, error) {
	candidate := strings.TrimSpace(rawPath)
	if candidate == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(r.agent.Workspace, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rawPath, err)
	}
	abs = filepath.Clean(abs)
	for _, root := range r.agent.allowedFSRoots {
		if isWithinRoot(abs, root) {
			return abs, nil
		}
	}
	return "", &PolicyError{Message: fmt.Sprintf("fs access denied for path %q", rawPath)}
}

func isWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func requiredStringArg(args map[string]any, key string) (string, error) {
	val, ok := optionalStringArg(args, key)
	if !ok || val == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return val, nil
}

func optionalStringArg(args map[string]any, key string) (string, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", false
	}
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	return s, true
}

func toInt(v any) (int, bool) {
	switch typed := v.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func parseStringSlice(v any) ([]string, error) {
	switch typed := v.(type) {
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out, nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("args must contain only strings")
			}
			out = append(out, str)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("args must be an array of strings")
	}
}

func isInAllowlist(command string, allowlist []string) bool {
	for _, allowed := range allowlist {
		if command == allowed {
			return true
		}
	}
	return false
}
