package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type OpenAIResponsesOptions struct {
	StreamOptions
	ReasoningEffort  ThinkingLevel `json:"reasoningEffort,omitempty"`
	ReasoningSummary string        `json:"reasoningSummary,omitempty"`
	ServiceTier      string        `json:"serviceTier,omitempty"`
}

var openAIResponsesToolCallProviders = map[Provider]struct{}{
	ProviderOpenAI:       {},
	ProviderOpenAICodex:  {},
	Provider("opencode"): {},
}

func StreamOpenAIResponses(model Model, conversation Context, options *StreamOptions) *AssistantMessageEventStream {
	opts := &OpenAIResponsesOptions{}
	if options != nil {
		opts.StreamOptions = *options
	}
	return streamOpenAIResponses(model, conversation, opts)
}

func StreamSimpleOpenAIResponses(model Model, conversation Context, options *SimpleStreamOptions) *AssistantMessageEventStream {
	apiKey := ""
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == "" {
		apiKey = GetEnvAPIKey(string(model.Provider))
	}
	base := BuildBaseOptions(model, options, apiKey)
	reasoningEffort := ThinkingLevel("")
	if options != nil {
		reasoningEffort = options.Reasoning
	}
	if !SupportsXHigh(model) {
		reasoningEffort = ClampReasoning(reasoningEffort)
	}
	return streamOpenAIResponses(model, conversation, &OpenAIResponsesOptions{
		StreamOptions:   *base,
		ReasoningEffort: reasoningEffort,
	})
}

func streamOpenAIResponses(model Model, conversation Context, options *OpenAIResponsesOptions) *AssistantMessageEventStream {
	stream := CreateAssistantMessageEventStream()
	if options == nil {
		options = &OpenAIResponsesOptions{}
	}
	ctx := resolveRequestContext(&options.StreamOptions)

	go func() {
		output := NewAssistantMessage(model)
		defer stream.End(&output)

		apiKey := options.APIKey
		if apiKey == "" {
			apiKey = GetEnvAPIKey(string(model.Provider))
		}
		if apiKey == "" {
			output.StopReason = StopReasonError
			output.ErrorMessage = fmt.Sprintf("no API key for provider %s", model.Provider)
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		payload := buildOpenAIResponsesParams(model, conversation, options)
		if options.OnPayload != nil {
			options.OnPayload(payload)
		}

		body, err := json.Marshal(payload)
		if err != nil {
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		endpoint := strings.TrimRight(model.BaseURL, "/") + "/responses"
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			output.StopReason = StopReasonError
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}
		headers := withJSONContentType(mergeHeaders(model.Headers, options.Headers))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		resp, err := defaultHTTPClient.Do(req)
		if err != nil {
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			raw, _ := io.ReadAll(resp.Body)
			output.StopReason = StopReasonError
			output.ErrorMessage = fmt.Sprintf("%d status code (%s)", resp.StatusCode, strings.TrimSpace(string(raw)))
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
			return
		}

		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: &output})
		events := make(chan sseEvent, 32)
		errCh := make(chan error, 1)
		go func() {
			errCh <- readSSE(ctx, resp.Body, events)
		}()

		state := &responsesStreamState{}
		for ev := range events {
			if ev.Data == "[DONE]" || ev.Data == "" {
				continue
			}
			payload := decodeJSON(ev.Data)
			if err := processResponsesStreamEvent(payload, &output, stream, model, state, &openAIResponsesStreamOptions{
				ServiceTier:             options.ServiceTier,
				ApplyServiceTierPricing: applyServiceTierPricing,
			}); err != nil {
				output.StopReason = StopReasonError
				output.ErrorMessage = err.Error()
				stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &output})
				return
			}
		}
		if err := <-errCh; err != nil && ctx.Err() == nil {
			output.StopReason = stopReasonForError(ctx, err)
			output.ErrorMessage = err.Error()
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		if ctx.Err() != nil {
			output.StopReason = StopReasonAborted
			output.ErrorMessage = "request was aborted"
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonAborted, Error: &output})
			return
		}
		if output.StopReason == StopReasonError || output.StopReason == StopReasonAborted {
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: &output})
			return
		}
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: &output})
	}()

	return stream
}

func buildOpenAIResponsesParams(model Model, conversation Context, options *OpenAIResponsesOptions) map[string]any {
	messages := convertResponsesMessages(model, conversation, openAIResponsesToolCallProviders, true)
	cacheRetention := resolveCacheRetention(options.CacheRetention)
	params := map[string]any{
		"model":  model.ID,
		"input":  messages,
		"stream": true,
		"store":  false,
	}
	if cacheRetention != CacheRetentionNone && options.SessionID != "" {
		params["prompt_cache_key"] = options.SessionID
	}
	if retention := getPromptCacheRetention(model.BaseURL, cacheRetention); retention != "" {
		params["prompt_cache_retention"] = retention
	}
	if options.MaxTokens != nil {
		params["max_output_tokens"] = *options.MaxTokens
	}
	if options.Temperature != nil {
		params["temperature"] = *options.Temperature
	}
	if options.ServiceTier != "" {
		params["service_tier"] = options.ServiceTier
	}
	if len(conversation.Tools) > 0 {
		params["tools"] = convertResponsesTools(conversation.Tools, nil)
	}
	if model.Reasoning {
		if options.ReasoningEffort != "" || options.ReasoningSummary != "" {
			summary := options.ReasoningSummary
			if summary == "" {
				summary = "auto"
			}
			effort := options.ReasoningEffort
			if effort == "" {
				effort = ThinkingMedium
			}
			params["reasoning"] = map[string]any{"effort": string(effort), "summary": summary}
			params["include"] = []string{"reasoning.encrypted_content"}
		} else if strings.HasPrefix(model.Name, "gpt-5") {
			messages = append(messages, map[string]any{
				"role":    "developer",
				"content": []map[string]any{{"type": "input_text", "text": "# Juice: 0 !important"}},
			})
			params["input"] = messages
		}
	}
	return params
}

func getPromptCacheRetention(baseURL string, cacheRetention CacheRetention) string {
	if cacheRetention != CacheRetentionLong {
		return ""
	}
	if strings.Contains(baseURL, "api.openai.com") {
		return "24h"
	}
	return ""
}

func resolveCacheRetention(cacheRetention CacheRetention) CacheRetention {
	if cacheRetention != "" {
		return cacheRetention
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("PI_CACHE_RETENTION")), "long") {
		return CacheRetentionLong
	}
	return CacheRetentionShort
}

func getServiceTierCostMultiplier(serviceTier string) float64 {
	switch serviceTier {
	case "flex":
		return 0.5
	case "priority":
		return 2
	default:
		return 1
	}
}

func applyServiceTierPricing(usage *Usage, serviceTier string) {
	if usage == nil {
		return
	}
	multiplier := getServiceTierCostMultiplier(serviceTier)
	if multiplier == 1 {
		return
	}
	usage.Cost.Input *= multiplier
	usage.Cost.Output *= multiplier
	usage.Cost.CacheRead *= multiplier
	usage.Cost.CacheWrite *= multiplier
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}
