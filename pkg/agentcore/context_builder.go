package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
	ctxbundle "github.com/bstncartwright/gopher/pkg/context"
	"github.com/bstncartwright/gopher/pkg/memory"
)

type memoryExistence interface {
	Exists() (bool, error)
}

func (a *Agent) buildProviderContext(ctx context.Context, s *Session, userMessage string, modeOverride ...PromptMode) (ai.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	mode := PromptModeFull
	if len(modeOverride) > 0 {
		mode = normalizePromptMode(modeOverride[0])
	}

	working, err := a.Memory.LoadWorking()
	if err != nil {
		return ai.Context{}, fmt.Errorf("load working memory: %w", err)
	}
	if working == nil {
		working = map[string]any{}
	}
	s.WorkingState = ai.CloneMap(working)

	includeWorking := true
	if store, ok := a.Memory.(memoryExistence); ok {
		exists, err := store.Exists()
		if err != nil {
			return ai.Context{}, fmt.Errorf("check working memory: %w", err)
		}
		includeWorking = exists
	}

	contextFiles, err := loadBootstrapContextFiles(a.Workspace, mode, a.Config.BootstrapMaxChars, a.Config.BootstrapTotalMaxChars)
	if err != nil {
		return ai.Context{}, fmt.Errorf("load bootstrap context files: %w", err)
	}

	skillsPrompt := ""
	if mode == PromptModeFull && hasTool(a.Tools, "read") {
		skillsPrompt = strings.TrimSpace(formatSkillsForPrompt(a.skills))
	}

	systemPrompt, err := buildAgentSystemPrompt(systemPromptInput{
		Workspace:      a.Workspace,
		PromptMode:     mode,
		Tools:          a.Tools,
		SkillsPrompt:   skillsPrompt,
		ContextFiles:   contextFiles,
		IncludeWorking: includeWorking,
		Working:        working,
		UserTimezone:   a.Config.UserTimezone,
		Model:          a.model,
	})
	if err != nil {
		return ai.Context{}, fmt.Errorf("build system prompt: %w", err)
	}

	expandedUserMessage := expandSkillCommand(userMessage, a.skills)
	messages := boundMessages(s.Messages, a.Config.MaxContextMessages)
	userTimestamp := int64(0)
	if len(messages) > 0 {
		userTimestamp = messages[len(messages)-1].Timestamp + 1
	}
	messages = append(messages, ai.Message{
		Role:      ai.RoleUser,
		Content:   expandedUserMessage,
		Timestamp: userTimestamp,
	})

	var retrieved []memory.MemoryRecord
	if mode == PromptModeFull {
		retrieved = a.retrieveLongTermMemory(ctx, s, userMessage)
	}

	if a.Assembler != nil {
		assembled, err := a.Assembler.Build(ctx, ctxbundle.ContextRequest{
			BaseSystemPrompt: systemPrompt,
			Messages:         messages,
			Retrieved:        retrieved,
			CurrentTask:      expandedUserMessage,
			MaxTokens:        a.model.ContextWindow,
		})
		if err != nil {
			return ai.Context{}, fmt.Errorf("assemble context: %w", err)
		}
		systemPrompt = assembled.SystemPrompt
		messages = assembled.Messages
	} else if len(retrieved) > 0 {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + renderMemoryFallbackSection(retrieved))
	}

	return ai.Context{
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        toolSchemasToAITools(a.Tools),
	}, nil
}

func (a *Agent) retrieveLongTermMemory(ctx context.Context, s *Session, userMessage string) []memory.MemoryRecord {
	if a == nil || a.LongTermMemory == nil {
		return nil
	}

	scopes := []memory.MemoryScope{
		memory.ScopeGlobal,
		memory.AgentScope(a.ID),
		memory.SessionScope(s.ID),
	}
	projectName := strings.TrimSpace(filepath.Base(a.Workspace))
	if projectName != "" {
		scopes = append(scopes, memory.ProjectScope(projectName))
	}

	records, err := a.LongTermMemory.Retrieve(ctx, memory.MemoryQuery{
		SessionID: s.ID,
		AgentID:   a.ID,
		Topic:     userMessage,
		Keywords:  memory.ExtractKeywords(userMessage, 10),
		Limit:     8,
		Scopes:    scopes,
	})
	if err != nil {
		return nil
	}
	return records
}

func renderMemoryFallbackSection(records []memory.MemoryRecord) string {
	if len(records) == 0 {
		return ""
	}
	lines := []string{"### retrieved memory"}
	for _, record := range records {
		content := strings.Join(strings.Fields(strings.TrimSpace(record.Content)), " ")
		if content == "" {
			continue
		}
		if len(content) > 320 {
			content = content[:317] + "..."
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", record.Type.String(), content))
	}
	if len(lines) == 1 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func hasTool(registry ToolRegistry, name string) bool {
	if registry == nil {
		return false
	}
	_, ok := registry.Get(name)
	return ok
}

func marshalStableJSON(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}
