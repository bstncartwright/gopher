package agentcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

const (
	defaultExaWebSearchMCPEndpoint    = "https://mcp.exa.ai/mcp"
	defaultTavilyWebSearchMCPEndpoint = "https://mcp.tavily.com/mcp"
	defaultMCPProtocolVersion         = "2025-06-18"
)

type webIntelOperation string

const (
	operationSearch         webIntelOperation = "search"
	operationFetchContent   webIntelOperation = "fetch_content"
	operationStartResearch  webIntelOperation = "start_research"
	operationCheckResearch  webIntelOperation = "check_research"
	operationAdvancedSearch webIntelOperation = "advanced_search"
	operationCodeContext    webIntelOperation = "code_context"
	operationCompany        webIntelOperation = "company_research"
	operationPeople         webIntelOperation = "people_search"
	operationMapSite        webIntelOperation = "map_site"
	operationCrawlSite      webIntelOperation = "crawl_site"
)

var exaPreferredTools = map[webIntelOperation][]string{
	operationSearch: {
		"web_search_exa",
		"web_search_advanced_exa",
		"web_search",
		"search",
	},
	operationAdvancedSearch: {
		"web_search_advanced_exa",
		"web_search_exa",
	},
	operationFetchContent: {
		"crawling_exa",
	},
	operationStartResearch: {
		"deep_researcher_start",
	},
	operationCheckResearch: {
		"deep_researcher_check",
	},
	operationCodeContext: {
		"get_code_context_exa",
	},
	operationCompany: {
		"company_research_exa",
	},
	operationPeople: {
		"people_search_exa",
	},
}

var tavilyPreferredTools = map[webIntelOperation][]string{
	operationSearch: {
		"tavily_search",
		"tavily-search",
		"search",
	},
	operationFetchContent: {
		"tavily_extract",
		"tavily-extract",
	},
	operationStartResearch: {
		"tavily_research",
		"tavily-research",
	},
	operationMapSite: {
		"tavily_map",
		"tavily-map",
	},
	operationCrawlSite: {
		"tavily_crawl",
		"tavily-crawl",
	},
}

type SearchRequest struct {
	Query      string
	Params     map[string]any
	ToolName   string
	Provider   string
	NoFallback bool
}

type SearchResult struct {
	Provider      string
	Endpoint      string
	SelectedTool  string
	AvailableTool []string
	Summary       string
	Payload       map[string]any
}

type ContentFetchRequest struct {
	URLs          []string
	Depth         string
	Format        string
	IncludeImages bool
	Params        map[string]any
	ToolName      string
}

type ResearchRequest struct {
	Input        string
	Model        string
	OutputSchema map[string]any
	Params       map[string]any
	ToolName     string
}

type ResearchStatus struct {
	State   string
	Content string
	JobID   string
	Payload map[string]any
}

// WebIntelCore defines the shared cross-provider operations implemented by
// both Exa and Tavily adapters.
type WebIntelCore interface {
	Name() string
	Search(ctx context.Context, req SearchRequest) (SearchResult, error)
	FetchContent(ctx context.Context, req ContentFetchRequest) (map[string]any, error)
	StartResearch(ctx context.Context, req ResearchRequest) (ResearchStatus, error)
	CheckResearch(ctx context.Context, jobID string) (ResearchStatus, error)
}

// ExaSpecialized contains Exa-only operations with no Tavily equivalent.
type ExaSpecialized interface {
	AdvancedSearch(ctx context.Context, req SearchRequest) (SearchResult, error)
	GetCodeContext(ctx context.Context, query string, maxCharacters int) (map[string]any, error)
	CompanyResearch(ctx context.Context, company string, maxResults int) (map[string]any, error)
	PeopleSearch(ctx context.Context, query string, maxResults int) (map[string]any, error)
}

// TavilySpecialized contains Tavily-only operations with no Exa equivalent.
type TavilySpecialized interface {
	MapSite(ctx context.Context, url string, options map[string]any) (map[string]any, error)
	CrawlSite(ctx context.Context, url string, options map[string]any) (map[string]any, error)
}

type webSearchMCPTool struct {
	primary  WebIntelCore
	fallback WebIntelCore
}

func newWebSearchMCPTool() *webSearchMCPTool {
	client := &http.Client{Timeout: 45 * time.Second}
	return newWebSearchMCPToolWithProviders(
		newExaMCPProvider(defaultExaWebSearchMCPEndpoint, "EXA_API_KEY", client),
		newTavilyMCPProvider(defaultTavilyWebSearchMCPEndpoint, "TAVILY_API_KEY", client),
	)
}

func newWebSearchMCPToolWithProviders(primary WebIntelCore, fallback WebIntelCore) *webSearchMCPTool {
	return &webSearchMCPTool{primary: primary, fallback: fallback}
}

func (t *webSearchMCPTool) Name() string {
	return "web_search"
}

func (t *webSearchMCPTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: "Search the web through Exa MCP with Tavily MCP fallback on Exa quota/rate-limit responses.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "Search query.",
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
			"required": []any{"query"},
		},
	}
}

func (t *webSearchMCPTool) Run(ctx context.Context, input ToolInput) (ToolOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query, err := requiredStringArg(input.Args, "query")
	if err != nil {
		return t.fail("", err)
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return t.fail(query, fmt.Errorf("query is required"))
	}

	extraParams, err := optionalMapArg(input.Args, "params")
	if err != nil {
		return t.fail(query, err)
	}
	toolName, _ := optionalStringArg(input.Args, "tool_name")
	providerOverride, _ := optionalStringArg(input.Args, "provider")
	providerOverride = strings.ToLower(strings.TrimSpace(providerOverride))
	noFallback, _ := optionalBoolArg(input.Args, "no_fallback")

	req := SearchRequest{
		Query:      query,
		Params:     extraParams,
		ToolName:   strings.TrimSpace(toolName),
		Provider:   providerOverride,
		NoFallback: noFallback,
	}

	if req.Provider == "tavily" {
		result, runErr := t.fallback.Search(ctx, req)
		if runErr != nil {
			return t.fail(query, runErr)
		}
		return t.success(query, result, false, ""), nil
	}

	primaryResult, primaryErr := t.primary.Search(ctx, req)
	if primaryErr == nil {
		return t.success(query, primaryResult, false, ""), nil
	}

	if req.NoFallback {
		return t.fail(query, primaryErr)
	}

	allowFallback, reason := shouldFallbackToTavily(primaryErr)
	if !allowFallback {
		return t.fail(query, primaryErr)
	}

	fallbackResult, fallbackErr := t.fallback.Search(ctx, req)
	if fallbackErr != nil {
		return t.fail(query, fmt.Errorf("exa search failed (%s): %v; tavily fallback failed: %w", reason, primaryErr, fallbackErr))
	}

	return t.success(query, fallbackResult, true, reason), nil
}

func (t *webSearchMCPTool) success(query string, result SearchResult, fallbackUsed bool, fallbackReason string) ToolOutput {
	payload := map[string]any{
		"provider":        result.Provider,
		"endpoint":        result.Endpoint,
		"query":           query,
		"selected_tool":   result.SelectedTool,
		"available_tools": result.AvailableTool,
		"mcp_result":      result.Payload,
		"fallback_used":   fallbackUsed,
	}
	if result.Summary != "" {
		payload["summary"] = result.Summary
	}
	if strings.TrimSpace(fallbackReason) != "" {
		payload["fallback_reason"] = fallbackReason
	}
	return ToolOutput{Status: ToolStatusOK, Result: payload}
}

func (t *webSearchMCPTool) fail(query string, err error) (ToolOutput, error) {
	result := map[string]any{
		"error": err.Error(),
		"query": strings.TrimSpace(query),
	}
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

type mcpWebIntelProvider struct {
	name           string
	endpoint       string
	apiKeyEnv      string
	client         *http.Client
	preferredTools map[webIntelOperation][]string
}

func newExaMCPProvider(endpoint, apiKeyEnv string, client *http.Client) *exaMCPProvider {
	return &exaMCPProvider{base: &mcpWebIntelProvider{
		name:           "exa",
		endpoint:       strings.TrimSpace(endpoint),
		apiKeyEnv:      strings.TrimSpace(apiKeyEnv),
		client:         client,
		preferredTools: exaPreferredTools,
	}}
}

func newTavilyMCPProvider(endpoint, apiKeyEnv string, client *http.Client) *tavilyMCPProvider {
	return &tavilyMCPProvider{base: &mcpWebIntelProvider{
		name:           "tavily",
		endpoint:       strings.TrimSpace(endpoint),
		apiKeyEnv:      strings.TrimSpace(apiKeyEnv),
		client:         client,
		preferredTools: tavilyPreferredTools,
	}}
}

type exaMCPProvider struct {
	base *mcpWebIntelProvider
}

func (p *exaMCPProvider) Name() string {
	if p == nil || p.base == nil {
		return "exa"
	}
	return p.base.name
}

func (p *exaMCPProvider) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	return p.base.runSearch(ctx, operationSearch, req)
}

func (p *exaMCPProvider) FetchContent(ctx context.Context, req ContentFetchRequest) (map[string]any, error) {
	args := mergeParams(req.Params)
	if len(req.URLs) > 0 {
		args["urls"] = req.URLs
		args["url"] = req.URLs[0]
	}
	if strings.TrimSpace(req.Depth) != "" {
		args["depth"] = strings.TrimSpace(req.Depth)
	}
	if strings.TrimSpace(req.Format) != "" {
		args["format"] = strings.TrimSpace(req.Format)
	}
	args["include_images"] = req.IncludeImages
	res, err := p.base.runOperation(ctx, operationFetchContent, args, req.ToolName, false)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (p *exaMCPProvider) StartResearch(ctx context.Context, req ResearchRequest) (ResearchStatus, error) {
	args := mergeParams(req.Params)
	if text := strings.TrimSpace(req.Input); text != "" {
		args["query"] = text
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		args["model"] = model
	}
	if req.OutputSchema != nil {
		args["output_schema"] = ai.CloneMap(req.OutputSchema)
	}
	res, err := p.base.runOperation(ctx, operationStartResearch, args, req.ToolName, false)
	if err != nil {
		return ResearchStatus{}, err
	}
	return statusFromPayload(res.Payload), nil
}

func (p *exaMCPProvider) CheckResearch(ctx context.Context, jobID string) (ResearchStatus, error) {
	args := map[string]any{"job_id": strings.TrimSpace(jobID), "task_id": strings.TrimSpace(jobID)}
	res, err := p.base.runOperation(ctx, operationCheckResearch, args, "", false)
	if err != nil {
		return ResearchStatus{}, err
	}
	return statusFromPayload(res.Payload), nil
}

func (p *exaMCPProvider) AdvancedSearch(ctx context.Context, req SearchRequest) (SearchResult, error) {
	return p.base.runSearch(ctx, operationAdvancedSearch, req)
}

func (p *exaMCPProvider) GetCodeContext(ctx context.Context, query string, maxCharacters int) (map[string]any, error) {
	args := map[string]any{"query": strings.TrimSpace(query)}
	if maxCharacters > 0 {
		args["max_characters"] = maxCharacters
	}
	res, err := p.base.runOperation(ctx, operationCodeContext, args, "", false)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (p *exaMCPProvider) CompanyResearch(ctx context.Context, company string, maxResults int) (map[string]any, error) {
	args := map[string]any{"query": strings.TrimSpace(company), "company": strings.TrimSpace(company)}
	if maxResults > 0 {
		args["num_results"] = maxResults
		args["max_results"] = maxResults
	}
	res, err := p.base.runOperation(ctx, operationCompany, args, "", false)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (p *exaMCPProvider) PeopleSearch(ctx context.Context, query string, maxResults int) (map[string]any, error) {
	args := map[string]any{"query": strings.TrimSpace(query)}
	if maxResults > 0 {
		args["num_results"] = maxResults
		args["max_results"] = maxResults
	}
	res, err := p.base.runOperation(ctx, operationPeople, args, "", false)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

type tavilyMCPProvider struct {
	base *mcpWebIntelProvider
}

func (p *tavilyMCPProvider) Name() string {
	if p == nil || p.base == nil {
		return "tavily"
	}
	return p.base.name
}

func (p *tavilyMCPProvider) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	return p.base.runSearch(ctx, operationSearch, req)
}

func (p *tavilyMCPProvider) FetchContent(ctx context.Context, req ContentFetchRequest) (map[string]any, error) {
	args := mergeParams(req.Params)
	if len(req.URLs) > 0 {
		args["urls"] = req.URLs
		if len(req.URLs) == 1 {
			args["url"] = req.URLs[0]
		}
	}
	if strings.TrimSpace(req.Depth) != "" {
		args["extract_depth"] = strings.TrimSpace(req.Depth)
	}
	if strings.TrimSpace(req.Format) != "" {
		args["format"] = strings.TrimSpace(req.Format)
	}
	args["include_images"] = req.IncludeImages
	res, err := p.base.runOperation(ctx, operationFetchContent, args, req.ToolName, false)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (p *tavilyMCPProvider) StartResearch(ctx context.Context, req ResearchRequest) (ResearchStatus, error) {
	args := mergeParams(req.Params)
	if text := strings.TrimSpace(req.Input); text != "" {
		args["query"] = text
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		args["model"] = model
	}
	if req.OutputSchema != nil {
		args["output_schema"] = ai.CloneMap(req.OutputSchema)
	}
	res, err := p.base.runOperation(ctx, operationStartResearch, args, req.ToolName, false)
	if err != nil {
		return ResearchStatus{}, err
	}
	status := statusFromPayload(res.Payload)
	if strings.TrimSpace(status.State) == "" {
		status.State = "completed"
	}
	return status, nil
}

func (p *tavilyMCPProvider) CheckResearch(ctx context.Context, jobID string) (ResearchStatus, error) {
	return ResearchStatus{}, fmt.Errorf("tavily does not expose async research status checks")
}

func (p *tavilyMCPProvider) MapSite(ctx context.Context, url string, options map[string]any) (map[string]any, error) {
	args := mergeParams(options)
	args["url"] = strings.TrimSpace(url)
	res, err := p.base.runOperation(ctx, operationMapSite, args, "", false)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (p *tavilyMCPProvider) CrawlSite(ctx context.Context, url string, options map[string]any) (map[string]any, error) {
	args := mergeParams(options)
	args["url"] = strings.TrimSpace(url)
	res, err := p.base.runOperation(ctx, operationCrawlSite, args, "", false)
	if err != nil {
		return nil, err
	}
	return res.Payload, nil
}

func (p *mcpWebIntelProvider) runSearch(ctx context.Context, operation webIntelOperation, req SearchRequest) (SearchResult, error) {
	args := mergeParams(req.Params)
	args["query"] = strings.TrimSpace(req.Query)
	args["search_query"] = strings.TrimSpace(req.Query)
	return p.runOperation(ctx, operation, args, req.ToolName, true)
}

func (p *mcpWebIntelProvider) runOperation(ctx context.Context, operation webIntelOperation, args map[string]any, toolOverride string, allowAnyTool bool) (SearchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil {
		return SearchResult{}, fmt.Errorf("web intel provider is not configured")
	}
	apiKey := strings.TrimSpace(os.Getenv(p.apiKeyEnv))
	if apiKey == "" {
		return SearchResult{}, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Message: fmt.Sprintf("%s requires %s", p.name, p.apiKeyEnv)}
	}

	sessionID := ""
	initialize := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": defaultMCPProtocolVersion,
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "gopher",
				"version": "1.0.0",
			},
		},
	}
	if _, nextSession, err := p.callMCP(ctx, apiKey, sessionID, initialize); err != nil {
		return SearchResult{}, err
	} else {
		sessionID = nextSession
	}

	initialized := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	}
	if _, nextSession, err := p.callMCP(ctx, apiKey, sessionID, initialized); err != nil {
		return SearchResult{}, err
	} else {
		sessionID = nextSession
	}

	listToolsReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	listPayload, nextSession, err := p.callMCP(ctx, apiKey, sessionID, listToolsReq)
	if err != nil {
		return SearchResult{}, err
	}
	sessionID = nextSession

	availableTools := extractMCPToolNames(listPayload)
	if len(availableTools) == 0 {
		return SearchResult{}, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Message: "mcp tools/list returned no tools"}
	}

	preferred := p.preferredTools[operation]
	selectedTool := selectMCPToolName(strings.TrimSpace(toolOverride), availableTools, preferred, allowAnyTool)
	if selectedTool == "" {
		operationLabel := strings.TrimSpace(string(operation))
		if operationLabel == "" {
			operationLabel = "requested"
		}
		return SearchResult{}, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Message: fmt.Sprintf("no compatible MCP tool found for %s operation", operationLabel)}
	}

	callToolReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      selectedTool,
			"arguments": args,
		},
	}
	callPayload, _, err := p.callMCP(ctx, apiKey, sessionID, callToolReq)
	if err != nil {
		return SearchResult{}, err
	}

	return SearchResult{
		Provider:      p.name,
		Endpoint:      p.endpoint,
		SelectedTool:  selectedTool,
		AvailableTool: availableTools,
		Summary:       extractMCPCallSummary(callPayload),
		Payload:       callPayload,
	}, nil
}

type mcpRequestError struct {
	Provider   string
	Endpoint   string
	StatusCode int
	Code       string
	Message    string
	Cause      error
}

func (e *mcpRequestError) Error() string {
	if e == nil {
		return "mcp request failed"
	}
	parts := []string{}
	if strings.TrimSpace(e.Provider) != "" {
		parts = append(parts, e.Provider)
	}
	if e.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("status=%d", e.StatusCode))
	}
	if strings.TrimSpace(e.Code) != "" {
		parts = append(parts, "code="+strings.TrimSpace(e.Code))
	}
	if strings.TrimSpace(e.Message) != "" {
		parts = append(parts, strings.TrimSpace(e.Message))
	}
	if len(parts) == 0 {
		parts = append(parts, "mcp request failed")
	}
	if e.Cause != nil {
		parts = append(parts, e.Cause.Error())
	}
	return strings.Join(parts, ": ")
}

func (e *mcpRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (p *mcpWebIntelProvider) callMCP(ctx context.Context, apiKey string, sessionID string, payload map[string]any) (map[string]any, string, error) {
	if p == nil {
		return nil, sessionID, fmt.Errorf("mcp provider is nil")
	}
	if p.client == nil {
		p.client = &http.Client{Timeout: 45 * time.Second}
	}

	blob, err := json.Marshal(payload)
	if err != nil {
		return nil, sessionID, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Message: "marshal mcp payload", Cause: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(blob))
	if err != nil {
		return nil, sessionID, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Message: "build mcp request", Cause: err}
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, sessionID, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Message: "mcp request failed", Cause: err}
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, sessionID, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Message: "read mcp response", Cause: err}
	}

	nextSession := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	if nextSession == "" {
		nextSession = sessionID
	}

	parsed, parseErr := parseMCPResponsePayload(responseBody, resp.Header.Get("Content-Type"))
	if parseErr != nil {
		if resp.StatusCode >= 400 {
			return nil, nextSession, &mcpRequestError{
				Provider:   p.name,
				Endpoint:   p.endpoint,
				StatusCode: resp.StatusCode,
				Message:    strings.TrimSpace(string(responseBody)),
				Cause:      parseErr,
			}
		}
		if len(bytes.TrimSpace(responseBody)) == 0 {
			return map[string]any{}, nextSession, nil
		}
		return nil, nextSession, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, StatusCode: resp.StatusCode, Message: "parse mcp response", Cause: parseErr}
	}

	if resp.StatusCode >= 400 {
		code := formatMCPCode(parsed["code"])
		if code == "" {
			if errObj, ok := parsed["error"].(map[string]any); ok {
				code = formatMCPCode(errObj["code"])
			}
		}
		return nil, nextSession, &mcpRequestError{
			Provider:   p.name,
			Endpoint:   p.endpoint,
			StatusCode: resp.StatusCode,
			Code:       code,
			Message:    summarizeMCPError(parsed),
		}
	}

	if successRaw, exists := parsed["success"]; exists {
		if success, ok := successRaw.(bool); ok && !success {
			code := formatMCPCode(parsed["code"])
			msg := strings.TrimSpace(fmt.Sprintf("%v", parsed["msg"]))
			if msg == "" {
				msg = "mcp wrapper returned success=false"
			}
			return nil, nextSession, &mcpRequestError{Provider: p.name, Endpoint: p.endpoint, Code: code, Message: msg}
		}
	}

	if errObj, ok := parsed["error"].(map[string]any); ok {
		msg := strings.TrimSpace(fmt.Sprintf("%v", errObj["message"]))
		if msg == "" {
			msg = strings.TrimSpace(fmt.Sprintf("%v", errObj))
		}
		return nil, nextSession, &mcpRequestError{
			Provider: p.name,
			Endpoint: p.endpoint,
			Code:     formatMCPCode(errObj["code"]),
			Message:  msg,
		}
	}

	return parsed, nextSession, nil
}

func shouldFallbackToTavily(err error) (bool, string) {
	var reqErr *mcpRequestError
	if !errors.As(err, &reqErr) {
		return false, ""
	}
	if reqErr == nil || strings.TrimSpace(strings.ToLower(reqErr.Provider)) != "exa" {
		return false, ""
	}

	switch reqErr.StatusCode {
	case 402:
		return true, "exa payment-required/quota"
	case 429:
		return true, "exa rate-limit"
	case 401, 403:
		return false, ""
	}

	code := strings.ToLower(strings.TrimSpace(reqErr.Code))
	message := strings.ToLower(strings.TrimSpace(reqErr.Message))
	combined := strings.TrimSpace(code + " " + message)
	if combined == "" {
		return false, ""
	}

	if strings.Contains(combined, "unauthorized") ||
		strings.Contains(combined, "invalid api key") ||
		strings.Contains(combined, "forbidden") ||
		strings.Contains(combined, "authentication") {
		return false, ""
	}

	if strings.Contains(combined, "rate limit") ||
		strings.Contains(combined, "too many requests") ||
		strings.Contains(combined, "quota") ||
		strings.Contains(combined, "credit") ||
		strings.Contains(combined, "insufficient") ||
		strings.Contains(combined, "budget") ||
		strings.Contains(combined, "payment required") {
		if strings.Contains(combined, "rate") {
			return true, "exa rate-limit"
		}
		return true, "exa quota"
	}

	if code == "402" || code == "429" {
		if code == "429" {
			return true, "exa rate-limit"
		}
		return true, "exa quota"
	}

	return false, ""
}

func parseMCPResponsePayload(body []byte, contentType string) (map[string]any, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}

	var out map[string]any
	if err := json.Unmarshal(trimmed, &out); err == nil {
		return out, nil
	}

	if strings.Contains(strings.ToLower(contentType), "text/event-stream") || bytes.Contains(trimmed, []byte("data:")) {
		var last map[string]any
		scanner := bufio.NewScanner(bytes.NewReader(trimmed))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
			var item map[string]any
			if err := json.Unmarshal([]byte(payload), &item); err == nil {
				last = item
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		if last != nil {
			return last, nil
		}
	}

	return nil, fmt.Errorf("unsupported mcp response format")
}

func extractMCPToolNames(payload map[string]any) []string {
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return nil
	}
	rawTools, ok := result["tools"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(rawTools))
	for _, rawTool := range rawTools {
		toolMap, ok := rawTool.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(fmt.Sprintf("%v", toolMap["name"]))
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func selectMCPToolName(override string, available []string, preferred []string, allowAny bool) string {
	if override != "" {
		return override
	}
	if len(available) == 0 {
		return ""
	}
	availableSet := make(map[string]struct{}, len(available))
	for _, item := range available {
		availableSet[item] = struct{}{}
	}
	for _, candidate := range preferred {
		if _, ok := availableSet[candidate]; ok {
			return candidate
		}
	}
	if allowAny {
		return available[0]
	}
	return ""
}

func extractMCPCallSummary(payload map[string]any) string {
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		return ""
	}
	if text := strings.TrimSpace(fmt.Sprintf("%v", result["output_text"])); text != "" && text != "<nil>" {
		return text
	}
	if text := strings.TrimSpace(fmt.Sprintf("%v", result["text"])); text != "" && text != "<nil>" {
		return text
	}
	blocks, ok := result["content"].([]any)
	if !ok {
		return ""
	}
	lines := make([]string, 0, len(blocks))
	for _, block := range blocks {
		entry, ok := block.(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(fmt.Sprintf("%v", entry["type"])) != "text" {
			continue
		}
		text := strings.TrimSpace(fmt.Sprintf("%v", entry["text"]))
		if text != "" && text != "<nil>" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n")
}

func summarizeMCPError(payload map[string]any) string {
	if payload == nil {
		return "unknown error"
	}
	if msg := strings.TrimSpace(fmt.Sprintf("%v", payload["msg"])); msg != "" && msg != "<nil>" {
		return msg
	}
	if errObj, ok := payload["error"].(map[string]any); ok {
		if msg := strings.TrimSpace(fmt.Sprintf("%v", errObj["message"])); msg != "" && msg != "<nil>" {
			return msg
		}
	}
	return strings.TrimSpace(fmt.Sprintf("%v", payload))
}

func formatMCPCode(raw any) string {
	switch typed := raw.(type) {
	case int:
		return fmt.Sprintf("%d", typed)
	case int32:
		return fmt.Sprintf("%d", typed)
	case int64:
		return fmt.Sprintf("%d", typed)
	case float32:
		return fmt.Sprintf("%.0f", typed)
	case float64:
		return fmt.Sprintf("%.0f", typed)
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func optionalMapArg(args map[string]any, key string) (map[string]any, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return map[string]any{}, nil
	}
	out, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return ai.CloneMap(out), nil
}

func optionalBoolArg(args map[string]any, key string) (bool, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return false, false
	}
	typed, ok := value.(bool)
	if !ok {
		return false, false
	}
	return typed, true
}

func mergeParams(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return ai.CloneMap(params)
}

func statusFromPayload(payload map[string]any) ResearchStatus {
	if payload == nil {
		return ResearchStatus{}
	}
	result, _ := payload["result"].(map[string]any)
	if result == nil {
		result = payload
	}
	state := strings.TrimSpace(fmt.Sprintf("%v", result["state"]))
	if state == "" || state == "<nil>" {
		state = strings.TrimSpace(fmt.Sprintf("%v", result["status"]))
	}
	jobID := strings.TrimSpace(fmt.Sprintf("%v", result["job_id"]))
	if jobID == "" || jobID == "<nil>" {
		jobID = strings.TrimSpace(fmt.Sprintf("%v", result["task_id"]))
	}
	content := strings.TrimSpace(extractMCPCallSummary(payload))
	if content == "" {
		content = strings.TrimSpace(fmt.Sprintf("%v", result["content"]))
	}
	if content == "<nil>" {
		content = ""
	}
	return ResearchStatus{
		State:   state,
		Content: content,
		JobID:   jobID,
		Payload: ai.CloneMap(payload),
	}
}
