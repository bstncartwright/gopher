package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

func resolveOpenAIResponsesTransport(model Model, options *OpenAIResponsesOptions) Transport {
	if options != nil && options.Transport != "" {
		return options.Transport
	}
	if supportsOpenAIResponsesWebSocketByDefault(model) {
		return TransportAuto
	}
	return TransportSSE
}

func supportsOpenAIResponsesWebSocketByDefault(model Model) bool {
	if model.Provider != ProviderOpenAI {
		return false
	}
	baseURL := strings.TrimSpace(model.BaseURL)
	if baseURL == "" {
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com")
}

func shouldAttemptOpenAIResponsesWebSocket(transport Transport) bool {
	return transport == TransportWebSocket || transport == TransportAuto
}

func resolveOpenAIResponsesURL(baseURL string) string {
	normalized := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return normalized + "/responses"
}

func resolveOpenAIResponsesWebSocketURL(baseURL string) string {
	endpoint := resolveOpenAIResponsesURL(baseURL)
	u, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	return u.String()
}

func processOpenAIResponsesWebSocket(
	ctx context.Context,
	wsURL string,
	payload map[string]any,
	baseHeaders map[string]string,
	apiKey string,
	output *AssistantMessage,
	stream *AssistantMessageEventStream,
	model Model,
	options *OpenAIResponsesOptions,
	started *bool,
) error {
	headers := withJSONContentType(baseHeaders)
	delete(headers, "Accept")
	delete(headers, "accept")

	wsHeaders := http.Header{}
	for k, v := range headers {
		wsHeaders.Set(k, v)
	}
	wsHeaders.Set("Authorization", "Bearer "+apiKey)

	conn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: wsHeaders})
	if err != nil {
		return err
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	create := map[string]any{"type": "response.create"}
	for k, v := range payload {
		create[k] = v
	}
	delete(create, "stream")
	delete(create, "background")

	if err := writeOpenAIResponsesWebSocketJSON(ctx, conn, create); err != nil {
		return err
	}
	if started != nil {
		*started = true
	}
	stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})

	state := &responsesStreamState{}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		if err := processResponsesStreamEvent(event, output, stream, model, state, &openAIResponsesStreamOptions{
			ServiceTier:             options.ServiceTier,
			ApplyServiceTierPricing: applyServiceTierPricing,
		}); err != nil {
			return err
		}
		if stringFrom(event, "type") == "response.completed" {
			return nil
		}
	}
}

func writeOpenAIResponsesWebSocketJSON(ctx context.Context, conn *websocket.Conn, payload any) error {
	blob, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal websocket payload: %w", err)
	}
	return conn.Write(ctx, websocket.MessageText, blob)
}

func isNormalWebSocketClosureError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "statusnormalclosure") || strings.Contains(msg, "close frame")
}
