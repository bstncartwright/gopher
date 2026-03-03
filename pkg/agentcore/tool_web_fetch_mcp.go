package agentcore

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type webFetchMCPTool struct {
	primary  WebIntelCore
	fallback WebIntelCore
}

func newWebFetchMCPTool() *webFetchMCPTool {
	clientTool := newWebSearchMCPTool()
	return &webFetchMCPTool{
		primary:  clientTool.primary,
		fallback: clientTool.fallback,
	}
}

func newWebFetchMCPToolWithProviders(primary WebIntelCore, fallback WebIntelCore) *webFetchMCPTool {
	return &webFetchMCPTool{primary: primary, fallback: fallback}
}

func (t *webFetchMCPTool) Name() string {
	return "web_fetch"
}

func (t *webFetchMCPTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Fetch and extract page content from one or more URLs via Exa MCP with Tavily MCP fallback on Exa quota/rate-limit responses.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "Single URL to fetch.",
				},
				"urls": map[string]any{
					"type":        "array",
					"description": "List of URLs to fetch.",
					"items":       map[string]any{"type": "string"},
				},
				"depth": map[string]any{
					"type":        "string",
					"description": "Optional fetch depth hint for provider-specific extract behavior.",
				},
				"format": map[string]any{
					"type":        "string",
					"description": "Optional output format hint (for example markdown/text/html) if supported by provider.",
				},
				"include_images": map[string]any{
					"type":        "boolean",
					"description": "Whether extracted results should include image references.",
				},
				"params": map[string]any{
					"type":        "object",
					"description": "Additional MCP tool arguments to forward.",
				},
				"tool_name": map[string]any{
					"type":        "string",
					"description": "Override the MCP tool name to call.",
				},
				"provider": map[string]any{
					"type":        "string",
					"description": "Optional provider override: exa or tavily.",
				},
				"no_fallback": map[string]any{
					"type":        "boolean",
					"description": "Disable Exa->Tavily fallback when true.",
				},
			},
		},
	}
}

func (t *webFetchMCPTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	url, _ := optionalStringArg(input.Args, "url")
	urls, err := optionalStringArrayArg(input.Args, "urls")
	if err != nil {
		return t.fail(err)
	}
	urls = mergeAndNormalizeURLs(url, urls)
	if len(urls) == 0 {
		return t.fail(fmt.Errorf("web_fetch requires url or urls"))
	}

	extraParams, err := optionalMapArg(input.Args, "params")
	if err != nil {
		return t.fail(err)
	}
	depth, _ := optionalStringArg(input.Args, "depth")
	format, _ := optionalStringArg(input.Args, "format")
	includeImages, _ := optionalBoolArg(input.Args, "include_images")
	toolName, _ := optionalStringArg(input.Args, "tool_name")
	providerOverride, _ := optionalStringArg(input.Args, "provider")
	providerOverride = strings.ToLower(strings.TrimSpace(providerOverride))
	noFallback, _ := optionalBoolArg(input.Args, "no_fallback")

	req := ContentFetchRequest{
		URLs:          urls,
		Depth:         strings.TrimSpace(depth),
		Format:        strings.TrimSpace(format),
		IncludeImages: includeImages,
		Params:        extraParams,
		ToolName:      strings.TrimSpace(toolName),
	}

	if providerOverride == "tavily" {
		payload, runErr := t.fallback.FetchContent(ctx, req)
		if runErr != nil {
			return t.fail(runErr)
		}
		return t.success(urls, "tavily", payload, false, ""), nil
	}

	payload, primaryErr := t.primary.FetchContent(ctx, req)
	if primaryErr == nil {
		return t.success(urls, "exa", payload, false, ""), nil
	}

	if noFallback {
		return t.fail(primaryErr)
	}

	allowFallback, reason := shouldFallbackToTavily(primaryErr)
	if !allowFallback {
		return t.fail(primaryErr)
	}

	fallbackPayload, fallbackErr := t.fallback.FetchContent(ctx, req)
	if fallbackErr != nil {
		return t.fail(fmt.Errorf("exa fetch failed (%s): %v; tavily fallback failed: %w", reason, primaryErr, fallbackErr))
	}

	return t.success(urls, "tavily", fallbackPayload, true, reason), nil
}

func (t *webFetchMCPTool) success(urls []string, provider string, payload map[string]any, fallbackUsed bool, fallbackReason string) ToolOutput {
	result := map[string]any{
		"provider":      strings.TrimSpace(provider),
		"urls":          append([]string(nil), urls...),
		"mcp_result":    payload,
		"fallback_used": fallbackUsed,
	}
	if strings.TrimSpace(fallbackReason) != "" {
		result["fallback_reason"] = fallbackReason
	}
	return ToolOutput{Status: ToolStatusOK, Result: result}
}

func (t *webFetchMCPTool) fail(err error) (ToolOutput, error) {
	result := map[string]any{"error": err.Error()}
	var mcpErr *mcpRequestError
	if errors.As(err, &mcpErr) {
		result["provider"] = mcpErr.Provider
		result["endpoint"] = mcpErr.Endpoint
		if mcpErr.StatusCode > 0 {
			result["status_code"] = mcpErr.StatusCode
		}
		if strings.TrimSpace(mcpErr.Code) != "" {
			result["code"] = mcpErr.Code
		}
	}
	return ToolOutput{Status: ToolStatusError, Result: result}, err
}

func mergeAndNormalizeURLs(url string, urls []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(urls)+1)
	appendURL := func(raw string) {
		cleaned := strings.TrimSpace(raw)
		if cleaned == "" {
			return
		}
		if _, exists := seen[cleaned]; exists {
			return
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	appendURL(url)
	for _, item := range urls {
		appendURL(item)
	}
	return out
}

func optionalStringArrayArg(args map[string]any, key string) ([]string, error) {
	if args == nil {
		return nil, nil
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	entries, ok := raw.([]any)
	if !ok {
		if typed, castOK := raw.([]string); castOK {
			out := make([]string, 0, len(typed))
			for _, item := range typed {
				out = append(out, strings.TrimSpace(item))
			}
			return out, nil
		}
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	out := make([]string, 0, len(entries))
	for idx, entry := range entries {
		item, ok := entry.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", key, idx)
		}
		out = append(out, strings.TrimSpace(item))
	}
	return out, nil
}
