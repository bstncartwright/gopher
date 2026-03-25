package agentcore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	minimaxBaseURL              = "https://api.minimax.io"
	minimaxT2AEndpoint          = "/v1/t2a_v2"
	minimaxImageEndpoint        = "/v1/image_generation"
	minimaxVideoEndpoint        = "/v1/video_generation"
	minimaxMusicEndpoint        = "/v1/music_generation"
	minimaxFileRetrieveEndpoint = "/v1/files/retrieve"
)

type minimaxClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

func newMinimaxClient() *minimaxClient {
	return &minimaxClient{
		apiKey:     os.Getenv("MINIMAX_API_KEY"),
		baseURL:    minimaxBaseURL,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *minimaxClient) doRequest(ctx context.Context, method, endpoint string, body any) (map[string]any, error) {
	return c.doRequestWithQuery(ctx, method, endpoint, body, nil)
}

func (c *minimaxClient) doRequestWithQuery(ctx context.Context, method, endpoint string, body any, queryParams map[string]string) (map[string]any, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("MINIMAX_API_KEY environment variable is not set")
	}

	var bodyReader io.Reader
	if body != nil {
		blob, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(blob)
	}

	urlStr := strings.TrimSuffix(c.baseURL, "/") + endpoint
	if len(queryParams) > 0 {
		q := url.Values{}
		for k, v := range queryParams {
			q.Set(k, v)
		}
		urlStr += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if baseResp, ok := result["base_resp"].(map[string]any); ok {
		if statusCode, ok := baseResp["status_code"].(float64); ok && statusCode != 0 {
			statusMsg, _ := baseResp["status_msg"].(string)
			return nil, fmt.Errorf("minimax API error: status_code=%d, status_msg=%s", int(statusCode), statusMsg)
		}
	}

	return result, nil
}

type minimaxBaseResp struct {
	StatusCode int    `json:"status_code"`
	StatusMsg  string `json:"status_msg"`
}
