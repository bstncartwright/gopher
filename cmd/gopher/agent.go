package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type agentRecord struct {
	AgentID      string `json:"agent_id"`
	MatrixUserID string `json:"matrix_user_id"`
	Workspace    string `json:"workspace_path"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

var validAgentIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func runAgentSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || wantsHelp(args) {
		printAgentUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "create":
		return runAgentCreate(args[1:], stdout)
	case "list", "ls":
		return runAgentList(args[1:], stdout)
	case "delete", "remove", "rm":
		return runAgentDelete(args[1:], stdout)
	default:
		printAgentUsage(stderr)
		return fmt.Errorf("unknown agent command %q", args[0])
	}
}

func printAgentUsage(out io.Writer) {
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher agent create --id <agent_id> --matrix-user @<id>:<server> [--workspace <path>]")
	fmt.Fprintln(out, "  gopher agent list")
	fmt.Fprintln(out, "  gopher agent delete --id <agent_id> [--hard]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "shared flags:")
	fmt.Fprintln(out, "  --registry-path <path>  override agent registry path (default: ~/.gopher/agents/index.json)")
	fmt.Fprintln(out, "  --workspace-root <path> override workspace root for default workspace paths")
}

func runAgentCreate(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("agent create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	registryPathFlag := flags.String("registry-path", "", "registry path")
	workspaceRootFlag := flags.String("workspace-root", "", "workspace root")
	agentID := flags.String("id", "", "agent id")
	matrixUser := flags.String("matrix-user", "", "matrix user id")
	workspaceFlag := flags.String("workspace", "", "workspace path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	id := strings.TrimSpace(*agentID)
	if err := validateAgentID(id); err != nil {
		return err
	}
	matrixUserID := strings.TrimSpace(*matrixUser)
	if err := validateMatrixUserID(matrixUserID); err != nil {
		return err
	}

	registryPath, workspaceRoot, err := resolveAgentPaths(strings.TrimSpace(*registryPathFlag), strings.TrimSpace(*workspaceRootFlag))
	if err != nil {
		return err
	}
	registry, err := loadAgentRegistry(registryPath)
	if err != nil {
		return err
	}

	workspace := strings.TrimSpace(*workspaceFlag)
	if workspace != "" {
		workspace, err = expandAndAbsPath(workspace)
		if err != nil {
			return fmt.Errorf("resolve workspace path: %w", err)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	found := false
	for i := range registry {
		if registry[i].AgentID != id {
			continue
		}
		if registry[i].Status != "deleted" {
			return fmt.Errorf("agent %q already exists with status %q", id, registry[i].Status)
		}
		if workspace == "" {
			workspace = strings.TrimSpace(registry[i].Workspace)
		}
		if workspace == "" {
			workspace = filepath.Join(workspaceRoot, id)
		}
		registry[i].MatrixUserID = matrixUserID
		registry[i].Workspace = workspace
		registry[i].Status = "active"
		registry[i].UpdatedAt = now
		if strings.TrimSpace(registry[i].CreatedAt) == "" {
			registry[i].CreatedAt = now
		}
		found = true
		break
	}

	if workspace == "" {
		workspace = filepath.Join(workspaceRoot, id)
	}
	if !found {
		registry = append(registry, agentRecord{
			AgentID:      id,
			MatrixUserID: matrixUserID,
			Workspace:    workspace,
			Status:       "active",
			CreatedAt:    now,
			UpdatedAt:    now,
		})
	}

	if err := ensureAgentWorkspace(id, workspace); err != nil {
		return err
	}
	if err := ensureSharedUserProfile(workspaceRoot, workspace); err != nil {
		return err
	}
	if err := saveAgentRegistry(registryPath, registry); err != nil {
		return err
	}
	fmt.Fprintf(out, "created agent %s (%s)\n", id, workspace)
	return nil
}

func runAgentList(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("agent list", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	registryPathFlag := flags.String("registry-path", "", "registry path")
	workspaceRootFlag := flags.String("workspace-root", "", "workspace root")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	registryPath, _, err := resolveAgentPaths(strings.TrimSpace(*registryPathFlag), strings.TrimSpace(*workspaceRootFlag))
	if err != nil {
		return err
	}
	registry, err := loadAgentRegistry(registryPath)
	if err != nil {
		return err
	}
	if len(registry) == 0 {
		fmt.Fprintln(out, "no agents found")
		return nil
	}
	sort.Slice(registry, func(i, j int) bool {
		return registry[i].AgentID < registry[j].AgentID
	})

	fmt.Fprintln(out, "agents:")
	for _, agent := range registry {
		fmt.Fprintf(out, "  - %s | %s | %s | %s\n", agent.AgentID, agent.Status, agent.MatrixUserID, agent.Workspace)
	}
	return nil
}

func runAgentDelete(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("agent delete", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	registryPathFlag := flags.String("registry-path", "", "registry path")
	workspaceRootFlag := flags.String("workspace-root", "", "workspace root")
	agentID := flags.String("id", "", "agent id")
	hard := flags.Bool("hard", false, "remove workspace directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}

	id := strings.TrimSpace(*agentID)
	if err := validateAgentID(id); err != nil {
		return err
	}

	registryPath, workspaceRoot, err := resolveAgentPaths(strings.TrimSpace(*registryPathFlag), strings.TrimSpace(*workspaceRootFlag))
	if err != nil {
		return err
	}
	registry, err := loadAgentRegistry(registryPath)
	if err != nil {
		return err
	}

	found := false
	workspace := filepath.Join(workspaceRoot, id)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := range registry {
		if registry[i].AgentID != id {
			continue
		}
		if strings.TrimSpace(registry[i].Workspace) != "" {
			workspace = strings.TrimSpace(registry[i].Workspace)
		}
		registry[i].Status = "deleted"
		registry[i].UpdatedAt = now
		found = true
		break
	}
	if !found {
		return fmt.Errorf("agent %q not found", id)
	}

	if *hard {
		if err := os.RemoveAll(workspace); err != nil {
			return fmt.Errorf("remove workspace %s: %w", workspace, err)
		}
	}
	if err := saveAgentRegistry(registryPath, registry); err != nil {
		return err
	}
	if *hard {
		fmt.Fprintf(out, "deleted agent %s (hard)\n", id)
		return nil
	}
	fmt.Fprintf(out, "deleted agent %s (soft)\n", id)
	return nil
}

func validateAgentID(id string) error {
	if id == "" {
		return fmt.Errorf("agent --id is required")
	}
	if !validAgentIDPattern.MatchString(id) {
		return fmt.Errorf("invalid agent id %q (allowed: letters, numbers, -, _)", id)
	}
	return nil
}

func validateMatrixUserID(matrixUser string) error {
	if matrixUser == "" {
		return fmt.Errorf("agent --matrix-user is required")
	}
	if !strings.HasPrefix(matrixUser, "@") || !strings.Contains(matrixUser, ":") {
		return fmt.Errorf("invalid matrix user id %q", matrixUser)
	}
	return nil
}

func resolveAgentPaths(registryPathFlag, workspaceRootFlag string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve user home: %w", err)
	}

	workspaceRoot := workspaceRootFlag
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(home, ".gopher", "agents")
	}
	workspaceRoot, err = expandAndAbsPath(workspaceRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace root: %w", err)
	}

	registryPath := registryPathFlag
	if registryPath == "" {
		registryPath = filepath.Join(workspaceRoot, "index.json")
	}
	registryPath, err = expandAndAbsPath(registryPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve registry path: %w", err)
	}
	return registryPath, workspaceRoot, nil
}

func expandAndAbsPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func loadAgentRegistry(path string) ([]agentRecord, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []agentRecord{}, nil
		}
		return nil, fmt.Errorf("read agent registry %s: %w", path, err)
	}
	if strings.TrimSpace(string(blob)) == "" {
		return []agentRecord{}, nil
	}

	var records []agentRecord
	if err := json.Unmarshal(blob, &records); err != nil {
		return nil, fmt.Errorf("decode agent registry %s: %w", path, err)
	}
	return records, nil
}

func saveAgentRegistry(path string, records []agentRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create registry dir %s: %w", filepath.Dir(path), err)
	}

	sort.Slice(records, func(i, j int) bool {
		return records[i].AgentID < records[j].AgentID
	})
	blob, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent registry %s: %w", path, err)
	}
	blob = append(blob, '\n')

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, blob, 0o644); err != nil {
		return fmt.Errorf("write registry temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace registry %s: %w", path, err)
	}
	return nil
}

func ensureAgentWorkspace(agentID, workspace string) error {
	if workspace == "" {
		return fmt.Errorf("workspace path is required")
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return fmt.Errorf("create workspace %s: %w", workspace, err)
	}

	brandNew := true
	for _, name := range []string{
		"AGENTS.md", "SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md", "BOOTSTRAP.md",
		"agents.md", "soul.md", "tools.md", "identity.md", "user.md", "heartbeat.md", "bootstrap.md",
	} {
		path := filepath.Join(workspace, name)
		info, err := os.Stat(path)
		if err == nil {
			if !info.IsDir() {
				brandNew = false
				break
			}
			continue
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("check workspace file %s: %w", path, err)
		}
	}

	files := map[string]string{
		"AGENTS.md":     defaultAgentsTemplate(agentID),
		"SOUL.md":       defaultSoulTemplate(),
		"TOOLS.md":      defaultToolsTemplate(),
		"IDENTITY.md":   defaultIdentityTemplate(),
		"USER.md":       defaultUserTemplate(),
		"HEARTBEAT.md":  defaultHeartbeatTemplate(),
		"config.json":   defaultConfigTemplate(agentID),
		"policies.json": defaultPoliciesTemplate(),
	}
	if brandNew {
		files["BOOTSTRAP.md"] = defaultBootstrapTemplate()
	}
	for name, content := range files {
		path := filepath.Join(workspace, name)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check workspace file %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write workspace file %s: %w", path, err)
		}
	}
	if err := ensureMemoryNotes(workspace, time.Now()); err != nil {
		return err
	}
	return nil
}

func ensureMemoryNotes(workspace string, now time.Time) error {
	memoryDir := filepath.Join(workspace, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return fmt.Errorf("create memory directory %s: %w", memoryDir, err)
	}

	dates := []string{
		now.Format("2006-01-02"),
		now.AddDate(0, 0, -1).Format("2006-01-02"),
	}
	for _, date := range dates {
		path := filepath.Join(memoryDir, date+".md")
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("check memory note %s: %w", path, err)
		}
		content := fmt.Sprintf("# Daily Memory - %s\n\n", date)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write memory note %s: %w", path, err)
		}
	}
	return nil
}

func ensureSharedUserProfile(workspaceRoot, workspace string) error {
	workspaceRoot = filepath.Clean(strings.TrimSpace(workspaceRoot))
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspaceRoot == "" || workspace == "" {
		return nil
	}

	if !isWithinPath(workspace, workspaceRoot) {
		return nil
	}

	sharedPath := filepath.Join(workspaceRoot, "USER.md")
	if _, err := os.Stat(sharedPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check shared user profile %s: %w", sharedPath, err)
	}

	if err := os.WriteFile(sharedPath, []byte(defaultSharedUserTemplate()), 0o644); err != nil {
		return fmt.Errorf("write shared user profile %s: %w", sharedPath, err)
	}
	return nil
}

func isWithinPath(target, root string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
}
