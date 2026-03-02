package agentcore

import "github.com/bstncartwright/gopher/pkg/ai"

type simpleToolRegistry struct {
	tools map[string]Tool
	order []string
}

type toolAvailability interface {
	Available(input ToolInput) bool
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

func buildRegistry(enabled []string, policies AgentPolicies) ToolRegistry {
	if len(enabled) == 0 {
		return NewToolRegistry(nil)
	}

	selected := make([]Tool, 0, 8)
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
		case "group:fs":
			add(&readTool{})
			add(&writeTool{})
			add(&editTool{})
			if policies.ApplyPatchEnabled {
				add(&applyPatchTool{})
			}
		case "fs", "read_write":
			add(&readTool{})
			add(&writeTool{})
		case "fs.read", "read":
			add(&readTool{})
		case "fs.write", "write":
			add(&writeTool{})
		case "edit":
			add(&editTool{})
		case "apply_patch":
			if policies.ApplyPatchEnabled {
				add(&applyPatchTool{})
			}
		case "group:runtime":
			add(&execTool{})
			add(&processTool{})
			add(&gopherMetaTool{})
		case "group:collaboration":
			add(&delegateTool{})
			add(&heartbeatTool{})
			add(&messageTool{})
		case "group:web":
			add(newWebSearchMCPTool())
			add(newWebFetchMCPTool())
		case "shell", "shell.exec", "exec":
			add(&execTool{})
		case "process":
			add(&processTool{})
		case "gopher_meta":
			add(&gopherMetaTool{})
		case "cron":
			add(&cronTool{})
		case "delegate":
			add(&delegateTool{})
		case "heartbeat":
			add(&heartbeatTool{})
		case "message":
			add(&messageTool{})
		case "web_search", "search_mcp", "search":
			add(newWebSearchMCPTool())
		case "web_fetch", "fetch_mcp", "fetch":
			add(newWebFetchMCPTool())
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

func activeToolRegistry(registry ToolRegistry, input ToolInput) ToolRegistry {
	if registry == nil {
		return NewToolRegistry(nil)
	}
	schemas := registry.Schemas()
	active := make([]Tool, 0, len(schemas))
	for _, schema := range schemas {
		tool, ok := registry.Get(schema.Name)
		if !ok || tool == nil {
			continue
		}
		if availability, ok := tool.(toolAvailability); ok {
			if !availability.Available(input) {
				continue
			}
		}
		active = append(active, tool)
	}
	return NewToolRegistry(active)
}
