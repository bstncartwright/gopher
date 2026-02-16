package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var defaultHTTPClient = &http.Client{Timeout: 0}

type sseEvent struct {
	Event string
	Data  string
}

func readSSE(ctx context.Context, body io.ReadCloser, out chan<- sseEvent) error {
	defer close(out)
	defer body.Close()
	reader := bufio.NewReader(body)

	var (
		eventType string
		dataLines []string
	)

	dispatch := func() {
		if len(dataLines) == 0 {
			return
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		if data == "" {
			dataLines = dataLines[:0]
			eventType = ""
			return
		}
		out <- sseEvent{Event: eventType, Data: data}
		dataLines = dataLines[:0]
		eventType = ""
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				dispatch()
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			dispatch()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func decodeJSON(raw string) map[string]any {
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func resolveRequestContext(opts *StreamOptions) context.Context {
	if opts != nil && opts.RequestContext != nil {
		return opts.RequestContext
	}
	return context.Background()
}

func parseRetryAfter(headers http.Header) time.Duration {
	retry := headers.Get("Retry-After")
	if retry == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(retry); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if t, err := http.ParseTime(retry); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}

func withJSONContentType(headers map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range headers {
		out[k] = v
	}
	if _, ok := out["Content-Type"]; !ok {
		out["Content-Type"] = "application/json"
	}
	if _, ok := out["Accept"]; !ok {
		out["Accept"] = "text/event-stream"
	}
	return out
}
