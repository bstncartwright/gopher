package agentcore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type ToolStatus string

const (
	ToolStatusOK     ToolStatus = "ok"
	ToolStatusError  ToolStatus = "error"
	ToolStatusDenied ToolStatus = "denied"
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
	agent        *Agent
	loopDetector *LoopDetector
}

func NewToolRunner(agent *Agent) *ToolRunner {
	return &ToolRunner{
		agent:        agent,
		loopDetector: NewLoopDetector(agent.Policies.LoopDetection),
	}
}

func (r *ToolRunner) Run(ctx context.Context, s *Session, call ai.ContentBlock) (ToolOutput, error) {
	slog.Debug("tool_runner: starting tool execution",
		"tool_name", call.Name,
		"tool_id", call.ID,
		"session_id", s.ID,
	)
	tool, ok := r.agent.Tools.Get(call.Name)
	if !ok {
		err := fmt.Errorf("tool %q is not registered", call.Name)
		slog.Error("tool_runner: tool not registered",
			"tool_name", call.Name,
			"session_id", s.ID,
			"available_tools", r.getToolNames(),
		)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}

	validatedArgs, err := ai.ValidateToolCall(toolSchemasToAITools(r.agent.Tools), call)
	if err != nil {
		slog.Error("tool_runner: tool call validation failed",
			"tool_name", call.Name,
			"session_id", s.ID,
			"error", err,
		)
		return ToolOutput{Status: ToolStatusError, Result: map[string]any{"error": err.Error()}}, err
	}
	slog.Debug("tool_runner: args validated", "tool_name", call.Name, "session_id", s.ID)

	loopResult := r.loopDetector.Check(call.Name, validatedArgs)
	if loopResult.Level == LoopLevelCircuitBreaker {
		slog.Warn("tool_runner: circuit breaker triggered",
			"tool_name", call.Name,
			"session_id", s.ID,
			"message", loopResult.Message,
		)
		return ToolOutput{
			Status: ToolStatusDenied,
			Result: map[string]any{"error": loopResult.Message},
		}, fmt.Errorf("%s", loopResult.Message)
	}
	if loopResult.Level == LoopLevelCritical {
		slog.Warn("tool_runner: critical loop detected",
			"tool_name", call.Name,
			"session_id", s.ID,
			"message", loopResult.Message,
		)
		return ToolOutput{
			Status: ToolStatusDenied,
			Result: map[string]any{"error": loopResult.Message},
		}, fmt.Errorf("%s", loopResult.Message)
	}

	sanitizedArgs, err := r.enforcePolicy(call.Name, validatedArgs)
	if err != nil {
		slog.Warn("tool_runner: policy enforcement denied tool",
			"tool_name", call.Name,
			"session_id", s.ID,
			"error", err,
			"is_policy_error", IsPolicyError(err),
		)
		return ToolOutput{Status: ToolStatusDenied, Result: map[string]any{"error": err.Error()}}, err
	}
	slog.Debug("tool_runner: policy enforced", "tool_name", call.Name, "session_id", s.ID)

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

	slog.Info("tool_runner: tool execution complete",
		"tool_name", call.Name,
		"session_id", s.ID,
		"status", output.Status,
		"has_error", runErr != nil,
	)

	r.loopDetector.Record(call.Name, validatedArgs, output.Result)

	if loopResult.Level == LoopLevelWarning {
		slog.Warn("tool_runner: loop warning",
			"tool_name", call.Name,
			"session_id", s.ID,
			"message", loopResult.Message,
		)
		if resultMap, ok := output.Result.(map[string]any); ok {
			resultMap["_loop_warning"] = loopResult.Message
		}
	}

	return output, runErr
}

func (r *ToolRunner) getToolNames() []string {
	schemas := r.agent.Tools.Schemas()
	names := make([]string, len(schemas))
	for i, s := range schemas {
		names[i] = s.Name
	}
	return names
}

func (r *ToolRunner) enforcePolicy(name string, args map[string]any) (map[string]any, error) {
	slog.Debug("tool_runner: enforcing policy", "tool_name", name, "args_keys", getMapKeys(args))
	out := ai.CloneMap(args)

	switch name {
	case "read", "write", "edit":
		rawPath, err := requiredStringArg(out, "path")
		if err != nil {
			slog.Error("tool_runner: path arg required", "tool_name", name)
			return nil, err
		}
		resolved, err := r.resolvePathInAllowedRoots(rawPath)
		if err != nil {
			slog.Warn("tool_runner: path resolution denied",
				"tool_name", name,
				"raw_path", rawPath,
				"error", err,
			)
			return nil, err
		}
		slog.Debug("tool_runner: path resolved", "tool_name", name, "raw_path", rawPath, "resolved_path", resolved)
		out["path"] = resolved
		return out, nil

	case "apply_patch":
		if !r.agent.Policies.ApplyPatchEnabled {
			slog.Warn("tool_runner: apply_patch denied by policy", "apply_patch_enabled", false)
			return nil, &PolicyError{Message: "apply_patch denied: policies.apply_patch_enabled=false"}
		}
		return out, nil

	case "exec":
		if !r.agent.Policies.CanShell {
			slog.Warn("tool_runner: exec denied by policy", "can_shell", false)
			return nil, &PolicyError{Message: "exec denied: policies.can_shell=false"}
		}
		command, err := requiredStringArg(out, "command")
		if err != nil {
			return nil, err
		}
		if len(r.agent.Policies.ShellAllowlist) > 0 {
			if containsShellOperators(command) {
				slog.Warn("tool_runner: exec denied - shell operators detected",
					"command", command,
				)
				return nil, &PolicyError{Message: "exec denied: command contains shell operators (;, |, &&, ||, `, $(), newline, &) which bypass allowlist; use a single command"}
			}
			bin := extractCommandBinary(command)
			if !isInAllowlist(bin, r.agent.Policies.ShellAllowlist) {
				slog.Warn("tool_runner: exec denied - binary not in allowlist",
					"binary", bin,
					"allowlist", r.agent.Policies.ShellAllowlist,
				)
				return nil, &PolicyError{Message: fmt.Sprintf("exec denied: command %q is not in shell_allowlist", bin)}
			}
			slog.Debug("tool_runner: exec allowed via allowlist", "binary", bin)
		}
		workdir := r.agent.Workspace
		if rawWorkdir, ok := optionalStringArg(out, "workdir"); ok && rawWorkdir != "" {
			workdir = rawWorkdir
		}
		resolvedWorkdir, err := r.resolvePathInAllowedRoots(workdir)
		if err != nil {
			slog.Warn("tool_runner: workdir resolution denied",
				"workdir", workdir,
				"error", err,
			)
			return nil, err
		}
		out["workdir"] = resolvedWorkdir
		return out, nil

	case "process":
		if !r.agent.Policies.CanShell {
			slog.Warn("tool_runner: process denied by policy", "can_shell", false)
			return nil, &PolicyError{Message: "process denied: policies.can_shell=false"}
		}
		return out, nil

	case "web_search":
		if !r.agent.Policies.Network.Enabled {
			return nil, &PolicyError{Message: "web_search denied: policies.network.enabled=false"}
		}
		if !domainAllowed("api.z.ai", r.agent.Policies.Network.AllowDomains) {
			return nil, &PolicyError{Message: `web_search denied: "api.z.ai" is not allowed by policies.network.allow_domains`}
		}
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
	// Resolve symlinks to prevent escaping allowed roots via symlink.
	abs = evalSymlinksOrAncestor(abs)
	for _, root := range r.agent.allowedFSRoots {
		if isWithinRoot(abs, root) {
			return abs, nil
		}
	}
	return "", &PolicyError{Message: fmt.Sprintf("fs access denied for path %q", rawPath)}
}

// evalSymlinksOrAncestor resolves symlinks for the given path. If the path
// doesn't exist (e.g. writing a new file), it walks up to the nearest existing
// ancestor, resolves that, and re-joins the remaining segments.
func evalSymlinksOrAncestor(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir := filepath.Dir(path)
	if dir == path {
		return path
	}
	return filepath.Join(evalSymlinksOrAncestor(dir), filepath.Base(path))
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

func isInAllowlist(command string, allowlist []string) bool {
	for _, allowed := range allowlist {
		if command == allowed {
			return true
		}
	}
	return false
}

// containsShellOperators reports whether the command contains operators that could
// invoke additional commands beyond the first word. Used when shell_allowlist is
// active to prevent bypasses (e.g. "echo ok; curl evil.com").
func containsShellOperators(command string) bool {
	if strings.Contains(command, ";") ||
		strings.Contains(command, "|") ||
		strings.Contains(command, "&&") ||
		strings.Contains(command, "||") ||
		strings.Contains(command, "`") ||
		strings.Contains(command, "$(") ||
		strings.Contains(command, "\n") ||
		strings.Contains(command, " & ") {
		return true
	}
	// "cmd &" (background)
	if strings.HasSuffix(strings.TrimRight(command, " \t"), "&") {
		return true
	}
	return false
}

func extractCommandBinary(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return command
	}
	return filepath.Base(fields[0])
}

func domainAllowed(target string, allowDomains []string) bool {
	target = normalizeDomainHost(target)
	if target == "" {
		return false
	}
	for _, candidate := range allowDomains {
		host := normalizeDomainHost(candidate)
		if host == "" {
			continue
		}
		if host == "*" || host == target {
			return true
		}
		if strings.HasPrefix(host, "*.") {
			suffix := strings.TrimPrefix(host, "*")
			if strings.HasSuffix(target, suffix) {
				return true
			}
		}
	}
	return false
}

func normalizeDomainHost(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" {
		return ""
	}
	if value == "*" {
		return value
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			value = parsed.Host
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "/") {
		value = strings.SplitN(value, "/", 2)[0]
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, ".")
	return value
}

func (a *Agent) ResolvePath(rawPath string) (string, error) {
	candidate := strings.TrimSpace(rawPath)
	if candidate == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(a.Workspace, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rawPath, err)
	}
	abs = filepath.Clean(abs)
	abs = evalSymlinksOrAncestor(abs)
	for _, root := range a.allowedFSRoots {
		if isWithinRoot(abs, root) {
			return abs, nil
		}
	}
	return "", &PolicyError{Message: fmt.Sprintf("fs access denied for path %q", rawPath)}
}
