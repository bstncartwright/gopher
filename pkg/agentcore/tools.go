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
		case "group:memory":
			add(&memorySearchTool{})
			add(&memoryGetTool{})
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
			add(&codeExecTool{})
			add(&processTool{})
			add(&gopherMetaTool{})
			add(&gopherUpdateTool{})
		case "group:collaboration":
			add(&delegateTool{})
			add(&delegateTargetsTool{})
			add(&heartbeatTool{})
			add(&messageTool{})
			add(&reactionTool{})
		case "group:web":
			add(newWebSearchMCPTool())
			add(newWebFetchMCPTool())
		case "shell", "shell.exec", "exec":
			add(&execTool{})
		case "code_exec", "workspace_runner":
			add(&codeExecTool{})
		case "process":
			add(&processTool{})
		case "gopher_meta":
			add(&gopherMetaTool{})
		case "gopher_update":
			add(&gopherUpdateTool{})
		case "cron":
			add(&cronTool{})
		case "delegate":
			add(&delegateTool{})
		case "delegate_targets":
			add(&delegateTargetsTool{})
		case "heartbeat":
			add(&heartbeatTool{})
		case "message":
			add(&messageTool{})
		case "reaction":
			add(&reactionTool{})
		case "memory_search":
			add(&memorySearchTool{})
		case "memory_get":
			add(&memoryGetTool{})
		case "web_search", "search_mcp", "search":
			add(newWebSearchMCPTool())
		case "web_fetch", "fetch_mcp", "fetch", "fetch_content":
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
			Kind:        ai.ToolKindFunction,
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  schema.Parameters,
		})
	}
	return out
}

func buildProviderAITools(registry ToolRegistry, model ai.Model, config AgentConfig, policies AgentPolicies, forceMCP bool) []ai.Tool {
	schemas := registry.Schemas()
	out := make([]ai.Tool, 0, len(schemas))
	for _, schema := range schemas {
		if schema.Name == "web_search" {
			if !policies.Network.Enabled {
				continue
			}
			if tool, ok := selectWebSearchTool(schema, model, config, forceMCP); ok {
				out = append(out, tool)
			}
			continue
		}
		out = append(out, ai.Tool{
			Kind:        ai.ToolKindFunction,
			Name:        schema.Name,
			Description: schema.Description,
			Parameters:  schema.Parameters,
		})
	}
	return out
}

func selectWebSearchTool(schema ToolSchema, model ai.Model, config AgentConfig, forceMCP bool) (ai.Tool, bool) {
	if !forceMCP {
		switch config.NativeWebSearchModeValue(model) {
		case NativeWebSearchModeCached:
			return ai.Tool{
				Kind:              ai.ToolKindHostedWebSearch,
				Name:              schema.Name,
				Description:       schema.Description,
				ExternalWebAccess: boolPtr(false),
			}, true
		case NativeWebSearchModeLive:
			return ai.Tool{
				Kind:              ai.ToolKindHostedWebSearch,
				Name:              schema.Name,
				Description:       schema.Description,
				ExternalWebAccess: boolPtr(true),
			}, true
		}
	}
	return ai.Tool{
		Kind:        ai.ToolKindFunction,
		Name:        schema.Name,
		Description: schema.Description,
		Parameters:  schema.Parameters,
	}, true
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
