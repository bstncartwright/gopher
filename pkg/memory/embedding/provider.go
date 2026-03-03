package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bstncartwright/gopher/pkg/memory"
)

const (
	defaultTimeout     = 12 * time.Second
	defaultMaxBatch    = 16
	defaultMaxInputLen = 6000
	defaultRetries     = 2
	defaultConcurrency = 4
)

var errUnavailable = fmt.Errorf("embedding provider unavailable")

type Provider interface {
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	Model() string
	Provider() string
	AvailabilityProbe(ctx context.Context) error
}

type Options struct {
	Name        string
	ModelName   string
	BaseURL     string
	APIKey      string
	Timeout     time.Duration
	MaxBatch    int
	MaxInputLen int
	Retries     int
	Concurrency int
}

func New(opts Options) Provider {
	name := strings.ToLower(strings.TrimSpace(opts.Name))
	switch name {
	case "", "none", "off", "disabled":
		return &noneProvider{}
	case "hash":
		return &hashProvider{model: firstNonEmpty(opts.ModelName, "hash-embed-128")}
	case "openai":
		return newHTTPProvider("openai", opts, openaiEmbedCall)
	case "gemini":
		return newHTTPProvider("gemini", opts, geminiEmbedCall)
	case "voyage":
		return newHTTPProvider("voyage", opts, voyageEmbedCall)
	case "ollama":
		return newHTTPProvider("ollama", opts, ollamaEmbedCall)
	default:
		return &noneProvider{reason: "unknown provider: " + name}
	}
}

type noneProvider struct {
	reason string
}

func (p *noneProvider) EmbedBatch(_ context.Context, _ []string) ([][]float32, error) {
	if strings.TrimSpace(p.reason) == "" {
		return nil, errUnavailable
	}
	return nil, fmt.Errorf("%w: %s", errUnavailable, p.reason)
}

func (p *noneProvider) Model() string {
	return "none"
}

func (p *noneProvider) Provider() string {
	return "none"
}

func (p *noneProvider) AvailabilityProbe(_ context.Context) error {
	if strings.TrimSpace(p.reason) == "" {
		return errUnavailable
	}
	return fmt.Errorf("%w: %s", errUnavailable, p.reason)
}

type hashProvider struct {
	model string
}

func (p *hashProvider) Provider() string { return "hash" }
func (p *hashProvider) Model() string    { return p.model }
func (p *hashProvider) AvailabilityProbe(_ context.Context) error {
	return nil
}

func (p *hashProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	embedder := hashEmbedderOnce()
	for _, text := range texts {
		vec, err := embedder.Embed(ctx, text)
		if err != nil {
			return nil, err
		}
		out = append(out, vec)
	}
	return out, nil
}

type embedCallFn func(ctx context.Context, c *http.Client, opts httpProviderOptions, texts []string) ([][]float32, error)

type httpProvider struct {
	opts httpProviderOptions
	call embedCallFn
}

type httpProviderOptions struct {
	name        string
	model       string
	baseURL     string
	apiKey      string
	timeout     time.Duration
	maxBatch    int
	maxInputLen int
	retries     int
	concurrency int
}

func newHTTPProvider(name string, opts Options, call embedCallFn) Provider {
	model := strings.TrimSpace(opts.ModelName)
	if model == "" {
		switch name {
		case "openai":
			model = "text-embedding-3-small"
		case "gemini":
			model = "text-embedding-004"
		case "voyage":
			model = "voyage-3-lite"
		case "ollama":
			model = "nomic-embed-text"
		}
	}
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		switch name {
		case "openai":
			baseURL = "https://api.openai.com"
		case "gemini":
			baseURL = "https://generativelanguage.googleapis.com"
		case "voyage":
			baseURL = "https://api.voyageai.com"
		case "ollama":
			baseURL = "http://127.0.0.1:11434"
		}
	}
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		switch name {
		case "openai":
			apiKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		case "gemini":
			apiKey = firstNonEmpty(strings.TrimSpace(os.Getenv("GEMINI_API_KEY")), strings.TrimSpace(os.Getenv("GOOGLE_API_KEY")))
		case "voyage":
			apiKey = strings.TrimSpace(os.Getenv("VOYAGE_API_KEY"))
		case "ollama":
			apiKey = strings.TrimSpace(os.Getenv("OLLAMA_API_KEY"))
		}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxBatch := opts.MaxBatch
	if maxBatch <= 0 {
		maxBatch = defaultMaxBatch
	}
	maxInputLen := opts.MaxInputLen
	if maxInputLen <= 0 {
		maxInputLen = defaultMaxInputLen
	}
	retries := opts.Retries
	if retries < 0 {
		retries = 0
	}
	if retries == 0 {
		retries = defaultRetries
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}
	return &httpProvider{
		opts: httpProviderOptions{
			name:        name,
			model:       model,
			baseURL:     strings.TrimSuffix(baseURL, "/"),
			apiKey:      apiKey,
			timeout:     timeout,
			maxBatch:    maxBatch,
			maxInputLen: maxInputLen,
			retries:     retries,
			concurrency: concurrency,
		},
		call: call,
	}
}

func (p *httpProvider) Provider() string { return p.opts.name }
func (p *httpProvider) Model() string    { return p.opts.model }

func (p *httpProvider) AvailabilityProbe(ctx context.Context) error {
	if p == nil {
		return fmt.Errorf("%w: nil provider", errUnavailable)
	}
	if strings.TrimSpace(p.opts.baseURL) == "" {
		return fmt.Errorf("%w: base URL missing", errUnavailable)
	}
	if p.opts.name != "ollama" && strings.TrimSpace(p.opts.apiKey) == "" {
		return fmt.Errorf("%w: missing API key", errUnavailable)
	}
	_, err := p.EmbedBatch(ctx, []string{"ping"})
	return err
}

func (p *httpProvider) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(texts) == 0 {
		return nil, nil
	}
	filtered := make([]string, 0, len(texts))
	for _, text := range texts {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			trimmed = "."
		}
		if len(trimmed) > p.opts.maxInputLen {
			trimmed = trimmed[:p.opts.maxInputLen]
		}
		filtered = append(filtered, trimmed)
	}
	client := &http.Client{Timeout: p.opts.timeout}
	out := make([][]float32, len(filtered))
	type job struct {
		start int
		end   int
	}
	jobs := make([]job, 0, (len(filtered)/p.opts.maxBatch)+1)
	for start := 0; start < len(filtered); start += p.opts.maxBatch {
		end := start + p.opts.maxBatch
		if end > len(filtered) {
			end = len(filtered)
		}
		jobs = append(jobs, job{start: start, end: end})
	}
	if len(jobs) == 0 {
		return out, nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		jobCh    = make(chan job, len(jobs))
		firstErr error
	)
	workers := p.opts.concurrency
	if workers > len(jobs) {
		workers = len(jobs)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for batch := range jobCh {
				if ctx.Err() != nil {
					return
				}
				vectors, err := p.embedWithRetries(ctx, client, filtered[batch.start:batch.end])
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					return
				}
				mu.Lock()
				for i := range vectors {
					out[batch.start+i] = vectors[i]
				}
				mu.Unlock()
			}
		}()
	}
	for _, job := range jobs {
		jobCh <- job
	}
	close(jobCh)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (p *httpProvider) embedWithRetries(ctx context.Context, client *http.Client, texts []string) ([][]float32, error) {
	var lastErr error
	for attempt := 0; attempt <= p.opts.retries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		vectors, err := p.call(ctx, client, p.opts, texts)
		if err == nil {
			return vectors, nil
		}
		lastErr = err
		if !isRetriableError(err) || attempt >= p.opts.retries {
			break
		}
		delay := time.Duration(200*(attempt+1)) * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("embedding request failed")
	}
	return nil, lastErr
}

func openaiEmbedCall(ctx context.Context, c *http.Client, opts httpProviderOptions, texts []string) ([][]float32, error) {
	body := map[string]any{
		"model": opts.model,
		"input": texts,
	}
	blob, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.baseURL+"/v1/embeddings", bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.apiKey)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, httpStatusError(resp)
	}
	var payload struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([][]float32, 0, len(payload.Data))
	for _, item := range payload.Data {
		out = append(out, item.Embedding)
	}
	return out, nil
}

func voyageEmbedCall(ctx context.Context, c *http.Client, opts httpProviderOptions, texts []string) ([][]float32, error) {
	body := map[string]any{
		"model": opts.model,
		"input": texts,
	}
	blob, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.baseURL+"/v1/embeddings", bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+opts.apiKey)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, httpStatusError(resp)
	}
	var payload struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := make([][]float32, 0, len(payload.Data))
	for _, item := range payload.Data {
		out = append(out, item.Embedding)
	}
	return out, nil
}

func geminiEmbedCall(ctx context.Context, c *http.Client, opts httpProviderOptions, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		body := map[string]any{
			"model": "models/" + opts.model,
			"content": map[string]any{
				"parts": []map[string]string{{"text": text}},
			},
		}
		blob, _ := json.Marshal(body)
		url := fmt.Sprintf("%s/v1beta/models/%s:embedContent?key=%s", opts.baseURL, opts.model, opts.apiKey)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(blob))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			err = httpStatusError(resp)
			resp.Body.Close()
			return nil, err
		}
		var payload struct {
			Embedding struct {
				Values []float32 `json:"values"`
			} `json:"embedding"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()
		out = append(out, payload.Embedding.Values)
	}
	return out, nil
}

func ollamaEmbedCall(ctx context.Context, c *http.Client, opts httpProviderOptions, texts []string) ([][]float32, error) {
	body := map[string]any{
		"model": opts.model,
		"input": texts,
	}
	blob, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, opts.baseURL+"/api/embed", bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(opts.apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+opts.apiKey)
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, httpStatusError(resp)
	}
	var payload struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Embeddings, nil
}

func httpStatusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("embedding request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func isRetriableError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "status=429") ||
		strings.Contains(text, "status=500") ||
		strings.Contains(text, "status=502") ||
		strings.Contains(text, "status=503") ||
		strings.Contains(text, "status=504") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "temporarily")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

var (
	hashEmbedderInit sync.Once
	hashEmbedder     memory.Embedder
)

func hashEmbedderOnce() memory.Embedder {
	hashEmbedderInit.Do(func() {
		hashEmbedder = memory.NewHashEmbedder(128)
	})
	return hashEmbedder
}
