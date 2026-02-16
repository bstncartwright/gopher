package agentcore

import "github.com/bstncartwright/gopher/pkg/ai"

type simpleToolRegistry struct {
	tools map[string]Tool
	order []string
}

func NewToolRegistry(tools []Tool) ToolRegistry {
	registry := &simpleToolRegistry{
		tools: make(map[string]Tool, len(tools)),
		order: make([]string, 0, len(tools)),
	}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		name := tool.Name()
		if _, exists := registry.tools[name]; exists {
			continue
		}
		registry.tools[name] = tool
		registry.order = append(registry.order, name)
	}
	return registry
}

func (r *simpleToolRegistry) Schemas() []ToolSchema {
	out := make([]ToolSchema, 0, len(r.order))
	for _, name := range r.order {
		tool := r.tools[name]
		out = append(out, tool.Schema())
	}
	return out
}

func (r *simpleToolRegistry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

func buildRegistry(enabled []string) ToolRegistry {
	if len(enabled) == 0 {
		return NewToolRegistry(nil)
	}

	selected := make([]Tool, 0, 3)
	added := map[string]struct{}{}
	add := func(tool Tool) {
		if tool == nil {
			return
		}
		if _, exists := added[tool.Name()]; exists {
			return
		}
		added[tool.Name()] = struct{}{}
		selected = append(selected, tool)
	}

	for _, enabledTool := range enabled {
		switch enabledTool {
		case "fs":
			add(&fsReadTool{})
			add(&fsWriteTool{})
		case "fs.read":
			add(&fsReadTool{})
		case "fs.write":
			add(&fsWriteTool{})
		case "shell", "shell.exec":
			add(&shellExecTool{})
		case "git", "git.status", "git.diff":
			// intentionally ignored in v0
		}
	}

	return NewToolRegistry(selected)
}

func toolSchemasToAITools(registry ToolRegistry) []ai.Tool {
	schemas := registry.Schemas()
	out := make([]ai.Tool, 0, len(schemas))
	for _, schema := range schemas {
		out = append(out, ai.Tool{
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  schema.Parameters,
		})
	}
	return out
}
