package agentcore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultBootstrapMaxChars      = 20_000
	DefaultBootstrapTotalMaxChars = 150_000

	minBootstrapFileBudgetChars = 64
	bootstrapHeadRatio          = 0.7
	bootstrapTailRatio          = 0.2
)

type BootstrapContextFile struct {
	Name    string
	Path    string
	Content string
	Missing bool
}

type bootstrapSlot struct {
	name      string
	required  bool
	fallbacks []string
}

var bootstrapCanonicalSlots = []bootstrapSlot{
	{name: "AGENTS.md", required: true, fallbacks: []string{"agents.md"}},
	{name: "SOUL.md", required: true, fallbacks: []string{"soul.md"}},
	{name: "TOOLS.md", required: true, fallbacks: []string{"tools.md"}},
	{name: "IDENTITY.md", required: true, fallbacks: []string{"identity.md"}},
	{name: "USER.md", required: true, fallbacks: []string{"user.md"}},
	{name: "HEARTBEAT.md", required: false, fallbacks: []string{"heartbeat.md"}},
	{name: "BOOTSTRAP.md", required: false, fallbacks: []string{"bootstrap.md"}},
}

func loadBootstrapContextFiles(workspace string, mode PromptMode, maxChars, totalMaxChars int) ([]BootstrapContextFile, error) {
	files, err := loadBootstrapFiles(workspace, mode)
	if err != nil {
		return nil, err
	}
	return buildBootstrapContextFiles(files, maxChars, totalMaxChars), nil
}

func loadBootstrapFiles(workspace string, mode PromptMode) ([]BootstrapContextFile, error) {
	mode = normalizePromptMode(mode)
	if mode == PromptModeNone {
		return nil, nil
	}

	slots := bootstrapCanonicalSlots
	if mode == PromptModeMinimal {
		filtered := make([]bootstrapSlot, 0, 2)
		for _, slot := range slots {
			if slot.name == "AGENTS.md" || slot.name == "TOOLS.md" {
				filtered = append(filtered, slot)
			}
		}
		slots = filtered
	}

	out := make([]BootstrapContextFile, 0, slotsForMode(mode))
	for _, slot := range slots {
		resolvedPath, err := resolveBootstrapSlotPath(workspace, slot)
		if err != nil {
			return nil, err
		}

		if resolvedPath == "" {
			if !slot.required {
				continue
			}
			out = append(out, BootstrapContextFile{
				Name:    slot.name,
				Path:    filepath.Join(workspace, slot.name),
				Content: "",
				Missing: true,
			})
			continue
		}

		content, err := os.ReadFile(resolvedPath)
		if err != nil {
			return nil, fmt.Errorf("read bootstrap file %s: %w", resolvedPath, err)
		}
		out = append(out, BootstrapContextFile{
			Name:    slot.name,
			Path:    resolvedPath,
			Content: string(content),
			Missing: false,
		})
	}

	if mode == PromptModeMinimal {
		return out, nil
	}

	memoryEntries, err := resolveMemoryBootstrapEntries(workspace)
	if err != nil {
		return nil, err
	}
	out = append(out, memoryEntries...)
	return out, nil
}

func slotsForMode(mode PromptMode) int {
	switch mode {
	case PromptModeMinimal:
		return 2
	case PromptModeNone:
		return 0
	default:
		return len(bootstrapCanonicalSlots) + 2
	}
}

func resolveBootstrapSlotPath(workspace string, slot bootstrapSlot) (string, error) {
	if slot.name == "USER.md" {
		sharedPath, err := resolveSharedUserBootstrapPath(workspace, slot)
		if err != nil {
			return "", err
		}
		if sharedPath != "" {
			return sharedPath, nil
		}
	}

	return resolveBootstrapSlotPathInDir(workspace, slot)
}

func resolveSharedUserBootstrapPath(workspace string, slot bootstrapSlot) (string, error) {
	workspaceDir := filepath.Clean(filepath.Dir(workspace))
	if workspaceDir == "." || workspaceDir == string(filepath.Separator) {
		return "", nil
	}

	collectionRoot, ok := resolveAgentCollectionRoot(workspaceDir)
	if !ok {
		return "", nil
	}
	return resolveBootstrapSlotPathInDir(collectionRoot, slot)
}

func resolveAgentCollectionRoot(dir string) (string, bool) {
	clean := filepath.Clean(dir)
	if clean == "." || clean == string(filepath.Separator) {
		return "", false
	}
	if strings.EqualFold(filepath.Base(clean), "agents") {
		return clean, true
	}
	if fileExists(filepath.Join(clean, "index.json")) {
		return clean, true
	}
	if hasAgentWorkspaceChild(clean) {
		return clean, true
	}
	return "", false
}

func hasAgentWorkspaceChild(root string) bool {
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if fileExists(filepath.Join(candidate, "config.json")) && fileExists(filepath.Join(candidate, "policies.json")) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func resolveBootstrapSlotPathInDir(dir string, slot bootstrapSlot) (string, error) {
	candidates := make([]string, 0, len(slot.fallbacks)+1)
	candidates = append(candidates, slot.name)
	candidates = append(candidates, slot.fallbacks...)

	for _, name := range candidates {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat bootstrap file %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		return path, nil
	}

	return "", nil
}

func resolveMemoryBootstrapEntries(workspace string) ([]BootstrapContextFile, error) {
	candidates := []string{"MEMORY.md", "memory.md"}
	seen := map[string]struct{}{}
	out := make([]BootstrapContextFile, 0, len(candidates))

	for _, name := range candidates {
		path := filepath.Join(workspace, name)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat bootstrap file %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}

		dedupeKey := path
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			dedupeKey = resolved
		}
		if _, exists := seen[dedupeKey]; exists {
			continue
		}
		seen[dedupeKey] = struct{}{}

		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read bootstrap file %s: %w", path, err)
		}
		out = append(out, BootstrapContextFile{
			Name:    name,
			Path:    path,
			Content: string(content),
		})
	}

	return out, nil
}

func buildBootstrapContextFiles(files []BootstrapContextFile, maxChars, totalMaxChars int) []BootstrapContextFile {
	if maxChars <= 0 {
		maxChars = DefaultBootstrapMaxChars
	}
	if totalMaxChars <= 0 {
		totalMaxChars = DefaultBootstrapTotalMaxChars
	}

	remaining := totalMaxChars
	out := make([]BootstrapContextFile, 0, len(files))
	for _, file := range files {
		if remaining <= 0 {
			break
		}

		if file.Missing {
			text := "[MISSING] Expected at: " + file.Path
			text = clampToRunes(text, remaining)
			if text == "" {
				break
			}
			file.Content = text
			out = append(out, file)
			remaining -= runeLen(text)
			continue
		}

		content := strings.TrimSpace(file.Content)
		if content == "" {
			continue
		}
		if remaining < minBootstrapFileBudgetChars {
			break
		}

		fileMax := maxChars
		if fileMax > remaining {
			fileMax = remaining
		}
		content = trimBootstrapContent(content, file.Name, fileMax)
		content = clampToRunes(content, remaining)
		if content == "" {
			continue
		}

		file.Content = content
		out = append(out, file)
		remaining -= runeLen(content)
	}

	return out
}

func trimBootstrapContent(content, fileName string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if runeLen(content) <= maxChars {
		return content
	}

	headChars := int(float64(maxChars) * bootstrapHeadRatio)
	tailChars := int(float64(maxChars) * bootstrapTailRatio)
	if headChars < 1 {
		headChars = 1
	}
	if tailChars < 1 {
		tailChars = 1
	}

	head := firstRunes(content, headChars)
	tail := lastRunes(content, tailChars)
	marker := strings.Join([]string{
		"",
		"[...truncated, read " + fileName + " for full content...]",
		"...(truncated " + fileName + ": kept " + strconv.Itoa(headChars) + "+" + strconv.Itoa(tailChars) + " chars of " + strconv.Itoa(runeLen(content)) + ")...",
		"",
	}, "\n")
	return strings.Join([]string{head, marker, tail}, "\n")
}

func clampToRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	if runeLen(text) <= max {
		return text
	}
	if max <= 3 {
		return firstRunes(text, max)
	}
	return firstRunes(text, max-3) + "..."
}

func runeLen(text string) int {
	return len([]rune(text))
}

func firstRunes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n])
}

func lastRunes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[len(runes)-n:])
}
