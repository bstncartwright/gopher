package agentcore

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	osExec "os/exec"
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
		bin := extractCommandBinary(command)
		if len(r.agent.Policies.ShellAllowlist) > 0 {
			segments, err := splitAllowlistSegments(command)
			if err != nil {
				slog.Warn("tool_runner: exec denied - unsupported shell syntax in allowlist mode",
					"command", command,
					"error", err,
				)
				return nil, &PolicyError{Message: fmt.Sprintf("exec denied: %s", err.Error())}
			}
			for idx, segment := range segments {
				segmentBin, err := extractCommandBinaryFromSegment(segment)
				if err != nil {
					slog.Warn("tool_runner: exec denied - unable to resolve binary in segment",
						"segment", segment,
						"error", err,
					)
					return nil, &PolicyError{Message: "exec denied: unable to resolve executable from command segment"}
				}
				if idx == 0 {
					bin = segmentBin
				}
				if !isInAllowlist(segmentBin, r.agent.Policies.ShellAllowlist) {
					slog.Warn("tool_runner: exec denied - binary not in allowlist",
						"binary", segmentBin,
						"segment", segment,
						"allowlist", r.agent.Policies.ShellAllowlist,
					)
					return nil, &PolicyError{Message: fmt.Sprintf("exec denied: command %q is not in shell_allowlist", segmentBin)}
				}
				slog.Debug("tool_runner: exec segment allowed via allowlist", "binary", segmentBin, "segment", segment)
			}
		}
		if strings.EqualFold(bin, "opencode") {
			if err := enforceOpencodeExecPolicy(command, out); err != nil {
				return nil, err
			}
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

func extractCommandBinary(command string) string {
	args, err := splitShellArgs(command)
	if err != nil {
		return strings.TrimSpace(command)
	}
	for _, arg := range args {
		if isShellEnvAssignment(arg) {
			continue
		}
		return filepath.Base(arg)
	}
	return strings.TrimSpace(command)
}

func splitAllowlistSegments(command string) ([]string, error) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return nil, fmt.Errorf("command is required")
	}

	segments := make([]string, 0, 4)
	var segment strings.Builder

	pushSegment := func() error {
		part := strings.TrimSpace(segment.String())
		segment.Reset()
		if part == "" {
			return fmt.Errorf("malformed shell command")
		}
		segments = append(segments, part)
		return nil
	}

	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		next := byte(0)
		if i+1 < len(trimmed) {
			next = trimmed[i+1]
		}

		if escaped {
			segment.WriteByte(ch)
			escaped = false
			continue
		}

		if inSingle {
			if ch == '\'' {
				inSingle = false
			}
			segment.WriteByte(ch)
			continue
		}

		if inDouble {
			if ch == '\\' {
				segment.WriteByte(ch)
				if next != 0 {
					segment.WriteByte(next)
					i++
				}
				continue
			}
			if ch == '"' {
				inDouble = false
				segment.WriteByte(ch)
				continue
			}
			if ch == '`' || (ch == '$' && next == '(') {
				return nil, fmt.Errorf("command substitution is not supported in shell_allowlist mode")
			}
			segment.WriteByte(ch)
			continue
		}

		switch ch {
		case '\\':
			escaped = true
			segment.WriteByte(ch)
			continue
		case '\'':
			inSingle = true
			segment.WriteByte(ch)
			continue
		case '"':
			inDouble = true
			segment.WriteByte(ch)
			continue
		case '`':
			return nil, fmt.Errorf("command substitution is not supported in shell_allowlist mode")
		case '$':
			if next == '(' {
				return nil, fmt.Errorf("command substitution is not supported in shell_allowlist mode")
			}
		case '<', '>':
			return nil, fmt.Errorf("redirections are not supported in shell_allowlist mode")
		case '\n', '\r':
			return nil, fmt.Errorf("newlines are not supported in shell_allowlist mode")
		case '&':
			if next == '&' {
				if err := pushSegment(); err != nil {
					return nil, err
				}
				i++
				continue
			}
			return nil, fmt.Errorf("background commands are not supported in shell_allowlist mode")
		case '|':
			if next == '|' {
				if err := pushSegment(); err != nil {
					return nil, err
				}
				i++
				continue
			}
			if err := pushSegment(); err != nil {
				return nil, err
			}
			continue
		case ';':
			if err := pushSegment(); err != nil {
				return nil, err
			}
			continue
		case '(', ')':
			return nil, fmt.Errorf("subshell grouping is not supported in shell_allowlist mode")
		}

		segment.WriteByte(ch)
	}

	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("malformed shell command")
	}
	if err := pushSegment(); err != nil {
		return nil, err
	}
	return segments, nil
}

func extractCommandBinaryFromSegment(segment string) (string, error) {
	args, err := splitShellArgs(segment)
	if err != nil {
		return "", err
	}
	for _, arg := range args {
		if isShellEnvAssignment(arg) {
			continue
		}
		return filepath.Base(arg), nil
	}
	return "", fmt.Errorf("no executable in segment")
}

func splitShellArgs(command string) ([]string, error) {
	var (
		args     []string
		current  strings.Builder
		inSingle bool
		inDouble bool
		escaped  bool
	)

	pushArg := func() {
		if current.Len() == 0 {
			return
		}
		args = append(args, current.String())
		current.Reset()
	}

	for i := 0; i < len(command); i++ {
		ch := command[i]
		next := byte(0)
		if i+1 < len(command) {
			next = command[i+1]
		}

		if escaped {
			current.WriteByte(ch)
			escaped = false
			continue
		}
		if inSingle {
			if ch == '\'' {
				inSingle = false
				continue
			}
			current.WriteByte(ch)
			continue
		}
		if inDouble {
			if ch == '"' {
				inDouble = false
				continue
			}
			if ch == '\\' {
				if next != 0 {
					current.WriteByte(next)
					i++
					continue
				}
				return nil, fmt.Errorf("malformed shell command")
			}
			current.WriteByte(ch)
			continue
		}

		switch ch {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case ' ', '\t':
			pushArg()
		default:
			current.WriteByte(ch)
		}
	}

	if escaped || inSingle || inDouble {
		return nil, fmt.Errorf("malformed shell command")
	}
	pushArg()
	return args, nil
}

func isShellEnvAssignment(token string) bool {
	if token == "" {
		return false
	}
	eq := strings.IndexByte(token, '=')
	if eq <= 0 {
		return false
	}
	key := token[:eq]
	for i := 0; i < len(key); i++ {
		c := key[i]
		if i == 0 {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_') {
				return false
			}
			continue
		}
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

func enforceOpencodeExecPolicy(command string, args map[string]any) error {
	shellArgs, err := splitShellArgs(command)
	if err != nil {
		return &PolicyError{Message: fmt.Sprintf("exec denied: malformed opencode command: %s", err.Error())}
	}
	if len(shellArgs) == 0 {
		return &PolicyError{Message: "exec denied: opencode command is empty"}
	}

	background, _ := args["background"].(bool)
	if background {
		return &PolicyError{Message: "exec denied: opencode does not support background=true; use a foreground one-shot run"}
	}

	execIdx := 0
	for execIdx < len(shellArgs) && isShellEnvAssignment(shellArgs[execIdx]) {
		execIdx++
	}
	if execIdx >= len(shellArgs) {
		return &PolicyError{Message: "exec denied: opencode command is empty"}
	}
	if _, err := osExec.LookPath(shellArgs[execIdx]); err != nil {
		return &PolicyError{Message: "exec denied: opencode binary not found in PATH; install opencode and verify with `opencode --help`"}
	}

	commandArgs := shellArgs[execIdx+1:]
	subcommand, subcommandIdx := parseOpencodeSubcommand(commandArgs)
	if !strings.EqualFold(subcommand, "run") {
		return &PolicyError{Message: "exec denied: opencode must use the `run` subcommand for automation"}
	}

	if hasAnyFlag(commandArgs, "--continue", "-c", "--session", "-s", "--fork", "--attach") {
		return &PolicyError{Message: "exec denied: interactive opencode session flags are not allowed (`--continue`, `--session`, `--fork`, `--attach`)"}
	}
	if hasAnyFlag(commandArgs, "--dir") {
		return &PolicyError{Message: "exec denied: opencode `--dir` is not allowed; use the exec tool `workdir` field instead"}
	}

	runArgs := commandArgs[subcommandIdx+1:]
	if !flagHasValue(runArgs, "--format", "json") {
		return &PolicyError{Message: "exec denied: opencode one-shot automation requires `opencode run --format json ...`"}
	}

	return nil
}

func parseOpencodeSubcommand(args []string) (string, int) {
	valueFlags := map[string]struct{}{
		"--log-level": {},
		"--port":      {},
		"--hostname":  {},
		"--cors":      {},
		"--model":     {},
		"-m":          {},
		"--session":   {},
		"-s":          {},
		"--prompt":    {},
		"--agent":     {},
		"--command":   {},
		"--format":    {},
		"--file":      {},
		"-f":          {},
		"--title":     {},
		"--attach":    {},
		"--dir":       {},
		"--variant":   {},
	}

	for idx := 0; idx < len(args); idx++ {
		token := strings.TrimSpace(args[idx])
		if token == "" {
			continue
		}
		if strings.HasPrefix(token, "-") {
			if strings.Contains(token, "=") {
				continue
			}
			if _, hasValue := valueFlags[token]; hasValue {
				idx++
			}
			continue
		}
		return token, idx
	}
	return "", -1
}

func hasAnyFlag(args []string, names ...string) bool {
	for _, candidate := range args {
		for _, name := range names {
			if candidate == name || strings.HasPrefix(candidate, name+"=") {
				return true
			}
		}
	}
	return false
}

func flagHasValue(args []string, name string, expected string) bool {
	expect := strings.ToLower(strings.Trim(strings.TrimSpace(expected), `"'`))
	for idx, candidate := range args {
		if candidate == name {
			if idx+1 >= len(args) {
				return false
			}
			next := strings.ToLower(strings.Trim(strings.TrimSpace(args[idx+1]), `"'`))
			return next == expect
		}
		if strings.HasPrefix(candidate, name+"=") {
			value := strings.TrimPrefix(candidate, name+"=")
			value = strings.ToLower(strings.Trim(strings.TrimSpace(value), `"'`))
			return value == expect
		}
	}
	return false
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
