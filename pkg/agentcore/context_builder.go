package agentcore

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type memoryExistence interface {
	Exists() (bool, error)
}

func (a *Agent) buildProviderContext(s *Session, userMessage string) (ai.Context, error) {
	working, err := a.Memory.LoadWorking()
	if err != nil {
		return ai.Context{}, fmt.Errorf("load working memory: %w", err)
	}
	if working == nil {
		working = map[string]any{}
	}
	s.WorkingState = cloneAnyMap(working)

	includeWorking := true
	if store, ok := a.Memory.(memoryExistence); ok {
		exists, err := store.Exists()
		if err != nil {
			return ai.Context{}, fmt.Errorf("check working memory: %w", err)
		}
		includeWorking = exists
	}

	systemPrompt, err := buildStaticSystemPrompt(a.agentsDoc, a.soulDoc, working, includeWorking)
	if err != nil {
		return ai.Context{}, err
	}

	messages := boundMessages(s.Messages, a.Config.MaxContextMessages)
	userTimestamp := int64(0)
	if len(messages) > 0 {
		userTimestamp = messages[len(messages)-1].Timestamp + 1
	}
	messages = append(messages, ai.Message{
		Role:      ai.RoleUser,
		Content:   userMessage,
		Timestamp: userTimestamp,
	})

	return ai.Context{
		SystemPrompt: systemPrompt,
		Messages:     messages,
		Tools:        toolSchemasToAITools(a.Tools),
	}, nil
}

func buildStaticSystemPrompt(agentsDoc, soulDoc string, working map[string]any, includeWorking bool) (string, error) {
	sections := []string{
		"### AGENTS.md\n" + strings.TrimSpace(agentsDoc),
		"### soul.md\n" + strings.TrimSpace(soulDoc),
	}

	if includeWorking {
		blob, err := marshalStableJSON(working)
		if err != nil {
			return "", fmt.Errorf("encode working memory: %w", err)
		}
		sections = append(sections, "### working memory\n```json\n"+string(blob)+"\n```")
	}

	return strings.Join(sections, "\n\n"), nil
}

func marshalStableJSON(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func cloneAnyMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case map[string]any:
			out[key] = cloneAnyMap(typed)
		case []any:
			copySlice := make([]any, len(typed))
			for idx, item := range typed {
				if nestedMap, ok := item.(map[string]any); ok {
					copySlice[idx] = cloneAnyMap(nestedMap)
					continue
				}
				copySlice[idx] = item
			}
			out[key] = copySlice
		default:
			out[key] = value
		}
	}
	return out
}
