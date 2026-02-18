package agentcore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const agentSkillsPathEnv = "AGENT_SKILLS_PATH"

type skillFrontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

func discoverSkills(workspaceAbs string, configuredRoots []string) ([]Skill, error) {
	roots, err := resolveSkillRoots(workspaceAbs, configuredRoots)
	if err != nil {
		return nil, err
	}

	out := make([]Skill, 0, 8)
	seen := make(map[string]struct{})

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read skills directory %s: %w", root, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}

			skillPath := filepath.Join(root, entry.Name(), "SKILL.md")
			blob, err := os.ReadFile(skillPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read skill file %s: %w", skillPath, err)
			}

			skill, err := parseSkillMarkdown(skillPath, blob)
			if err != nil {
				continue
			}

			key := normalizeSkillName(skill.Name)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, skill)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		left := normalizeSkillName(out[i].Name)
		right := normalizeSkillName(out[j].Name)
		if left == right {
			return out[i].Location < out[j].Location
		}
		return left < right
	})

	return out, nil
}

func resolveSkillRoots(workspaceAbs string, configuredRoots []string) ([]string, error) {
	candidates := make([]string, 0, 8)

	for _, root := range configuredRoots {
		root = strings.TrimSpace(root)
		if root != "" {
			candidates = append(candidates, root)
		}
	}

	if env := strings.TrimSpace(os.Getenv(agentSkillsPathEnv)); env != "" {
		for _, root := range filepath.SplitList(env) {
			root = strings.TrimSpace(root)
			if root != "" {
				candidates = append(candidates, root)
			}
		}
	}

	if len(candidates) == 0 {
		candidates = append(candidates, filepath.Join(workspaceAbs, ".agents", "skills"))
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			candidates = append(candidates, filepath.Join(home, ".agents", "skills"))
		}
	}

	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(workspaceAbs, candidate)
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			return nil, fmt.Errorf("resolve skill path %q: %w", candidate, err)
		}
		clean := filepath.Clean(abs)
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out, nil
}

func parseSkillMarkdown(location string, blob []byte) (Skill, error) {
	text := strings.ReplaceAll(string(blob), "\r\n", "\n")
	frontmatterText, body, ok := splitYAMLFrontmatter(text)
	if !ok {
		return Skill{}, fmt.Errorf("missing YAML frontmatter")
	}

	frontmatter := skillFrontmatter{}
	if err := yaml.Unmarshal([]byte(frontmatterText), &frontmatter); err != nil {
		return Skill{}, fmt.Errorf("parse frontmatter: %w", err)
	}

	name := strings.TrimSpace(frontmatter.Name)
	if name == "" {
		name = strings.TrimSpace(filepath.Base(filepath.Dir(location)))
	}

	description := strings.TrimSpace(frontmatter.Description)
	if description == "" {
		return Skill{}, fmt.Errorf("frontmatter must include non-empty description")
	}

	return Skill{
		Name:                   name,
		Description:            description,
		Location:               location,
		BaseDir:                filepath.Dir(location),
		Instruction:            strings.TrimSpace(body),
		DisableModelInvocation: frontmatter.DisableModelInvocation,
	}, nil
}

func splitYAMLFrontmatter(text string) (string, string, bool) {
	lines := strings.Split(text, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", false
	}

	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "---" {
			continue
		}
		frontmatter := strings.Join(lines[1:i], "\n")
		body := strings.Join(lines[i+1:], "\n")
		return frontmatter, body, true
	}

	return "", "", false
}

func formatSkillsForPrompt(skills []Skill) string {
	visible := make([]Skill, 0, len(skills))
	for _, skill := range skills {
		if skill.DisableModelInvocation {
			continue
		}
		visible = append(visible, skill)
	}
	if len(visible) == 0 {
		return ""
	}

	lines := []string{
		"The following skills provide specialized instructions for specific tasks.",
		"Use the read tool to load a skill's file when the task matches its description.",
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	}
	for _, skill := range visible {
		lines = append(lines, "  <skill>")
		lines = append(lines, "    <name>"+escapeXML(skill.Name)+"</name>")
		lines = append(lines, "    <description>"+escapeXML(skill.Description)+"</description>")
		lines = append(lines, "    <location>"+escapeXML(skill.Location)+"</location>")
		lines = append(lines, "  </skill>")
	}
	lines = append(lines, "</available_skills>")

	return strings.Join(lines, "\n")
}

func expandSkillCommand(userMessage string, skills []Skill) string {
	if !strings.HasPrefix(userMessage, "/skill:") {
		return userMessage
	}

	spaceIndex := strings.Index(userMessage, " ")
	skillName := ""
	args := ""
	if spaceIndex == -1 {
		skillName = userMessage[len("/skill:"):]
	} else {
		skillName = userMessage[len("/skill:"):spaceIndex]
		args = strings.TrimSpace(userMessage[spaceIndex+1:])
	}
	skillName = strings.TrimSpace(skillName)
	if skillName == "" {
		return userMessage
	}

	var selected *Skill
	for i := range skills {
		if skills[i].Name == skillName {
			selected = &skills[i]
			break
		}
	}
	if selected == nil {
		return userMessage
	}

	lines := []string{
		fmt.Sprintf(`<skill name="%s" location="%s">`, escapeXML(selected.Name), escapeXML(selected.Location)),
		fmt.Sprintf("References are relative to %s.", selected.BaseDir),
	}
	if strings.TrimSpace(selected.Instruction) != "" {
		lines = append(lines, "", selected.Instruction)
	}
	lines = append(lines, "</skill>")

	out := strings.Join(lines, "\n")
	if args != "" {
		out += "\n\n" + args
	}
	return out
}

func escapeXML(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	).Replace(s)
}

func normalizeSkillName(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	if lower == "" {
		return ""
	}
	return strings.ReplaceAll(lower, " ", "-")
}
