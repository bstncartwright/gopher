package a2a

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
)

type Client interface {
	Discover(ctx context.Context, remote Remote) (AgentCard, error)
	GetExtendedCard(ctx context.Context, cardURL string, remote Remote) (AgentCard, error)
	SendMessage(ctx context.Context, endpoint string, remote Remote, req MessageSendRequest) (Task, error)
	GetTask(ctx context.Context, endpoint string, remote Remote, taskID string) (Task, error)
	SubscribeTask(ctx context.Context, endpoint string, remote Remote, taskID string, emit func(Task) error) error
	CancelTask(ctx context.Context, endpoint string, remote Remote, taskID string) error
}

type HTTPClient struct{}

func NewHTTPClient() *HTTPClient {
	return &HTTPClient{}
}

func (c *HTTPClient) Discover(ctx context.Context, remote Remote) (AgentCard, error) {
	targets := discoverCardURLs(remote)
	var lastErr error
	for _, target := range targets {
		card, err := c.GetExtendedCard(ctx, target, remote)
		if err == nil {
			return card, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no discovery URL available")
	}
	return AgentCard{}, lastErr
}

func (c *HTTPClient) GetExtendedCard(ctx context.Context, cardURL string, remote Remote) (AgentCard, error) {
	body, err := c.doJSON(ctx, http.MethodGet, cardURL, remote, nil)
	if err != nil {
		return AgentCard{}, err
	}
	return decodeAgentCard(body)
}

func (c *HTTPClient) SendMessage(ctx context.Context, endpoint string, remote Remote, req MessageSendRequest) (Task, error) {
	body, err := c.doJSON(ctx, http.MethodPost, joinURL(endpoint, "message:send"), remote, req)
	if err != nil {
		return Task{}, err
	}
	return decodeTask(body)
}

func (c *HTTPClient) GetTask(ctx context.Context, endpoint string, remote Remote, taskID string) (Task, error) {
	body, err := c.doJSON(ctx, http.MethodGet, joinURL(endpoint, "tasks", url.PathEscape(strings.TrimSpace(taskID))), remote, nil)
	if err != nil {
		return Task{}, err
	}
	return decodeTask(body)
}

func (c *HTTPClient) SubscribeTask(ctx context.Context, endpoint string, remote Remote, taskID string, emit func(Task) error) error {
	if emit == nil {
		return fmt.Errorf("emit callback is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(endpoint, "tasks", url.PathEscape(strings.TrimSpace(taskID))+":subscribe"), bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream, application/json")
	req.Header.Set("Content-Type", "application/json")
	applyHeaders(req, remote.Headers)

	resp, err := buildHTTPClient(remote).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		blob, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
		return fmt.Errorf("subscribe task failed: %s", httpError(resp.StatusCode, blob))
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if strings.Contains(contentType, "text/event-stream") {
		return streamSSE(resp.Body, emit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	task, err := decodeTask(body)
	if err != nil {
		return err
	}
	return emit(task)
}

func (c *HTTPClient) CancelTask(ctx context.Context, endpoint string, remote Remote, taskID string) error {
	_, err := c.doJSON(ctx, http.MethodPost, joinURL(endpoint, "tasks", url.PathEscape(strings.TrimSpace(taskID))+":cancel"), remote, map[string]any{})
	return err
}

func (c *HTTPClient) doJSON(ctx context.Context, method string, target string, remote Remote, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		blob, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(blob)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	applyHeaders(req, remote.Headers)

	resp, err := buildHTTPClient(remote).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	blob, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", httpError(resp.StatusCode, blob))
	}
	return blob, nil
}

func discoverCardURLs(remote Remote) []string {
	out := make([]string, 0, 3)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	add(remote.CardURL)
	base := strings.TrimSpace(remote.BaseURL)
	if base != "" {
		add(joinURL(base, ".well-known", "agent-card.json"))
		if remote.CompatLegacyWellKnownPath {
			add(joinURL(base, ".well-known", "agent.json"))
		}
	}
	return out
}

func decodeAgentCard(blob []byte) (AgentCard, error) {
	card := AgentCard{}
	if err := json.Unmarshal(blob, &card); err == nil && (strings.TrimSpace(card.Name) != "" || strings.TrimSpace(card.URL) != "" || len(card.Interfaces) > 0) {
		return card, nil
	}
	wrapper := struct {
		AgentCard AgentCard `json:"agentCard"`
		Card      AgentCard `json:"card"`
		Result    AgentCard `json:"result"`
	}{}
	if err := json.Unmarshal(blob, &wrapper); err != nil {
		return AgentCard{}, err
	}
	switch {
	case strings.TrimSpace(wrapper.AgentCard.Name) != "" || strings.TrimSpace(wrapper.AgentCard.URL) != "" || len(wrapper.AgentCard.Interfaces) > 0:
		return wrapper.AgentCard, nil
	case strings.TrimSpace(wrapper.Card.Name) != "" || strings.TrimSpace(wrapper.Card.URL) != "" || len(wrapper.Card.Interfaces) > 0:
		return wrapper.Card, nil
	case strings.TrimSpace(wrapper.Result.Name) != "" || strings.TrimSpace(wrapper.Result.URL) != "" || len(wrapper.Result.Interfaces) > 0:
		return wrapper.Result, nil
	default:
		return AgentCard{}, fmt.Errorf("response did not contain an agent card")
	}
}

func decodeTask(blob []byte) (Task, error) {
	task := Task{}
	if err := json.Unmarshal(blob, &task); err == nil && task.NormalizedID() != "" {
		return task, nil
	}
	wrapper := struct {
		Task   Task `json:"task"`
		Result Task `json:"result"`
	}{}
	if err := json.Unmarshal(blob, &wrapper); err != nil {
		return Task{}, err
	}
	switch {
	case wrapper.Task.NormalizedID() != "":
		return wrapper.Task, nil
	case wrapper.Result.NormalizedID() != "":
		return wrapper.Result, nil
	default:
		return Task{}, fmt.Errorf("response did not contain a task")
	}
}

func streamSSE(body io.Reader, emit func(Task) error) error {
	reader := bufio.NewReader(body)
	dataLines := make([]string, 0, 4)
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		task, err := decodeTask([]byte(strings.Join(dataLines, "\n")))
		dataLines = dataLines[:0]
		if err != nil {
			return err
		}
		return emit(task)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil && len(line) == 0 {
			if err == io.EOF {
				return flush()
			}
			return err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if flushErr := flush(); flushErr != nil {
				return flushErr
			}
			if err == io.EOF {
				return nil
			}
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
		if err == io.EOF {
			return flush()
		}
	}
}

func buildHTTPClient(remote Remote) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if remote.AllowInsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &http.Client{Transport: transport}
}

func applyHeaders(req *http.Request, headers map[string]string) {
	for key, value := range headers {
		k := strings.TrimSpace(key)
		v := strings.TrimSpace(value)
		if k == "" || v == "" {
			continue
		}
		req.Header.Set(k, v)
	}
}

func joinURL(base string, elems ...string) string {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed == nil {
		return strings.TrimRight(strings.TrimSpace(base), "/") + "/" + strings.Join(elems, "/")
	}
	parts := make([]string, 0, len(elems))
	for _, elem := range elems {
		parts = append(parts, strings.Trim(elem, "/"))
	}
	joined := path.Join(append([]string{parsed.Path}, parts...)...)
	if strings.HasSuffix(elems[len(elems)-1], ":cancel") || strings.HasSuffix(elems[len(elems)-1], ":subscribe") {
		joined = strings.TrimSuffix(joined, "/")
	}
	parsed.Path = joined
	return parsed.String()
}

func httpError(status int, body []byte) string {
	message := strings.TrimSpace(string(body))
	if message == "" {
		return fmt.Sprintf("http %d", status)
	}
	return fmt.Sprintf("http %d: %s", status, message)
}
