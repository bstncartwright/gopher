package agentcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBootstrapContextFilesMissingMarkers(t *testing.T) {
	workspace := t.TempDir()

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}

	if len(files) == 0 {
		t.Fatalf("expected missing bootstrap markers")
	}
	if files[0].Name != "AGENTS.md" || !files[0].Missing {
		t.Fatalf("expected first file to be missing AGENTS.md, got %#v", files[0])
	}
	if !strings.HasPrefix(files[0].Content, "[MISSING] Expected at: ") {
		t.Fatalf("expected missing marker content, got: %q", files[0].Content)
	}
}

func TestLoadBootstrapContextFilesTruncatesPerFile(t *testing.T) {
	workspace := t.TempDir()
	writeRequiredBootstrapFiles(t, workspace)
	mustWriteFile(t, filepath.Join(workspace, "TOOLS.md"), strings.Repeat("x", 4000))

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, 120, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}

	tools := findBootstrapByName(files, "TOOLS.md")
	if tools == nil {
		t.Fatalf("expected TOOLS.md to be injected")
	}
	if !strings.Contains(tools.Content, "[...truncated, read TOOLS.md for full content...]") {
		t.Fatalf("expected truncation marker in TOOLS.md content, got: %q", tools.Content)
	}
}

func TestLoadBootstrapContextFilesRespectsTotalCap(t *testing.T) {
	workspace := t.TempDir()
	writeRequiredBootstrapFiles(t, workspace)
	for _, name := range []string{"AGENTS.md", "SOUL.md", "TOOLS.md", "IDENTITY.md", "USER.md", "HEARTBEAT.md"} {
		mustWriteFile(t, filepath.Join(workspace, name), strings.Repeat(name+" ", 500))
	}

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, 300, 240)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}

	total := 0
	for _, file := range files {
		total += len([]rune(file.Content))
	}
	if total > 240 {
		t.Fatalf("expected total injected chars <= 240, got %d", total)
	}
}

func TestLoadBootstrapContextFilesMinimalModeAllowlist(t *testing.T) {
	workspace := t.TempDir()
	writeRequiredBootstrapFiles(t, workspace)

	files, err := loadBootstrapContextFiles(workspace, PromptModeMinimal, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files in minimal mode, got %d", len(files))
	}
	if files[0].Name != "AGENTS.md" || files[1].Name != "TOOLS.md" {
		t.Fatalf("unexpected minimal mode files: %#v", []string{files[0].Name, files[1].Name})
	}
}

func TestLoadBootstrapContextFilesMemoryInclusion(t *testing.T) {
	workspace := t.TempDir()
	writeRequiredBootstrapFiles(t, workspace)

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	if findBootstrapByName(files, "MEMORY.md") != nil || findBootstrapByName(files, "memory.md") != nil {
		t.Fatalf("did not expect memory files when absent")
	}

	mustWriteFile(t, filepath.Join(workspace, "MEMORY.md"), "remember this")
	files, err = loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	if findBootstrapByName(files, "MEMORY.md") == nil {
		t.Fatalf("expected MEMORY.md to be injected when present")
	}
}

func TestLoadBootstrapContextFilesSkipsTemplateUpdatesFile(t *testing.T) {
	workspace := t.TempDir()
	writeRequiredBootstrapFiles(t, workspace)
	mustWriteFile(t, filepath.Join(workspace, "TEMPLATE_UPDATES.md"), "template update notice")

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	if findBootstrapByName(files, "TEMPLATE_UPDATES.md") != nil {
		t.Fatalf("did not expect TEMPLATE_UPDATES.md in full mode")
	}

	minimalFiles, err := loadBootstrapContextFiles(workspace, PromptModeMinimal, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() minimal mode error: %v", err)
	}
	if findBootstrapByName(minimalFiles, "TEMPLATE_UPDATES.md") != nil {
		t.Fatalf("did not expect TEMPLATE_UPDATES.md in minimal mode")
	}
}

func TestLoadBootstrapContextFilesSkipsMissingHeartbeatFile(t *testing.T) {
	workspace := t.TempDir()
	mustWriteFile(t, filepath.Join(workspace, "AGENTS.md"), "agents")
	mustWriteFile(t, filepath.Join(workspace, "SOUL.md"), "soul")
	mustWriteFile(t, filepath.Join(workspace, "TOOLS.md"), "tools")
	mustWriteFile(t, filepath.Join(workspace, "IDENTITY.md"), "identity")
	mustWriteFile(t, filepath.Join(workspace, "USER.md"), "user")

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	if hb := findBootstrapByName(files, "HEARTBEAT.md"); hb != nil && hb.Missing {
		t.Fatalf("did not expect missing heartbeat marker when HEARTBEAT.md is absent")
	}
}

func TestLoadBootstrapContextFilesCanonicalPrecedenceAndFallback(t *testing.T) {
	workspace := t.TempDir()
	if !isCaseSensitiveFilesystem(workspace) {
		t.Skip("filesystem is case-insensitive; cannot verify uppercase/lowercase precedence")
	}
	writeRequiredBootstrapFiles(t, workspace)
	mustWriteFile(t, filepath.Join(workspace, "SOUL.md"), "canonical soul")
	mustWriteFile(t, filepath.Join(workspace, "soul.md"), "legacy soul")

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	soul := findBootstrapByName(files, "SOUL.md")
	if soul == nil {
		t.Fatalf("expected SOUL.md entry")
	}
	if filepath.Base(soul.Path) != "SOUL.md" {
		t.Fatalf("expected canonical SOUL.md path, got %s", soul.Path)
	}
	if !strings.Contains(soul.Content, "canonical soul") {
		t.Fatalf("expected canonical SOUL.md content, got %q", soul.Content)
	}

	if err := os.Remove(filepath.Join(workspace, "SOUL.md")); err != nil {
		t.Fatalf("remove SOUL.md: %v", err)
	}
	files, err = loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	soul = findBootstrapByName(files, "SOUL.md")
	if soul == nil || soul.Missing {
		t.Fatalf("expected SOUL.md slot to load from legacy fallback")
	}
	if filepath.Base(soul.Path) != "soul.md" {
		t.Fatalf("expected legacy fallback path soul.md, got %s", soul.Path)
	}
	if !strings.Contains(soul.Content, "legacy soul") {
		t.Fatalf("expected legacy soul content, got %q", soul.Content)
	}
}

func TestLoadBootstrapContextFilesUserUsesSharedProfileFromCollectionRoot(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agents", "planner")
	writeRequiredBootstrapFiles(t, workspace)
	localUserPath := filepath.Join(workspace, "USER.md")
	sharedUserPath := filepath.Join(root, "agents", "USER.md")
	mustWriteFile(t, localUserPath, "local user profile")
	mustWriteFile(t, sharedUserPath, "shared user profile")

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	user := findBootstrapByName(files, "USER.md")
	if user == nil || user.Missing {
		t.Fatalf("expected USER.md entry")
	}
	if user.Path != sharedUserPath {
		t.Fatalf("expected shared USER.md path %q, got %q", sharedUserPath, user.Path)
	}
	if !strings.Contains(user.Content, "shared user profile") {
		t.Fatalf("expected shared USER.md content, got %q", user.Content)
	}
}

func TestLoadBootstrapContextFilesUserFallsBackToLocalWhenSharedMissing(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agents", "planner")
	writeRequiredBootstrapFiles(t, workspace)
	localUserPath := filepath.Join(workspace, "USER.md")
	mustWriteFile(t, localUserPath, "local user profile")

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	user := findBootstrapByName(files, "USER.md")
	if user == nil || user.Missing {
		t.Fatalf("expected USER.md entry")
	}
	if user.Path != localUserPath {
		t.Fatalf("expected local USER.md path %q, got %q", localUserPath, user.Path)
	}
	if !strings.Contains(user.Content, "local user profile") {
		t.Fatalf("expected local USER.md content, got %q", user.Content)
	}
}

func TestLoadBootstrapContextFilesUserSharedLowercaseFallback(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "agents", "planner")
	writeRequiredBootstrapFiles(t, workspace)
	if err := os.Remove(filepath.Join(workspace, "USER.md")); err != nil {
		t.Fatalf("remove USER.md: %v", err)
	}
	mustWriteFile(t, filepath.Join(root, "agents", "user.md"), "shared lowercase user profile")

	files, err := loadBootstrapContextFiles(workspace, PromptModeFull, DefaultBootstrapMaxChars, DefaultBootstrapTotalMaxChars)
	if err != nil {
		t.Fatalf("loadBootstrapContextFiles() error: %v", err)
	}
	user := findBootstrapByName(files, "USER.md")
	if user == nil || user.Missing {
		t.Fatalf("expected USER.md entry")
	}
	if filepath.Dir(user.Path) != filepath.Join(root, "agents") {
		t.Fatalf("expected shared USER.md path under collection root, got %q", user.Path)
	}
	if !strings.Contains(user.Content, "shared lowercase user profile") {
		t.Fatalf("expected shared lowercase user profile, got %q", user.Content)
	}
}

func findBootstrapByName(files []BootstrapContextFile, name string) *BootstrapContextFile {
	for i := range files {
		if files[i].Name == name {
			return &files[i]
		}
	}
	return nil
}

func writeRequiredBootstrapFiles(t *testing.T, workspace string) {
	t.Helper()
	mustWriteFile(t, filepath.Join(workspace, "AGENTS.md"), "agents")
	mustWriteFile(t, filepath.Join(workspace, "SOUL.md"), "soul")
	mustWriteFile(t, filepath.Join(workspace, "TOOLS.md"), "tools")
	mustWriteFile(t, filepath.Join(workspace, "IDENTITY.md"), "identity")
	mustWriteFile(t, filepath.Join(workspace, "USER.md"), "user")
	mustWriteFile(t, filepath.Join(workspace, "HEARTBEAT.md"), "heartbeat")
}

func isCaseSensitiveFilesystem(dir string) bool {
	upper := filepath.Join(dir, "CaseSensitivityProbe")
	lower := filepath.Join(dir, "casesensitivityprobe")
	_ = os.Remove(upper)
	_ = os.Remove(lower)

	if err := os.WriteFile(upper, []byte("UPPER"), 0o644); err != nil {
		return false
	}
	if err := os.WriteFile(lower, []byte("lower"), 0o644); err != nil {
		_ = os.Remove(upper)
		return false
	}

	upperBytes, upperErr := os.ReadFile(upper)
	lowerBytes, lowerErr := os.ReadFile(lower)
	_ = os.Remove(upper)
	_ = os.Remove(lower)
	if upperErr != nil || lowerErr != nil {
		return false
	}
	return string(upperBytes) != string(lowerBytes)
}
