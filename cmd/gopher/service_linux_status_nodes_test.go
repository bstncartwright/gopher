//go:build linux

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/config"
)

func TestReadGatewayNodeStatusLinesListsKnownNodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_gopher/panel/nodes" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"nodes":[{"node_id":"node-2","is_gateway":false,"version":"v1.2.3","capabilities":[{"kind":1,"name":"web_search"}],"last_heartbeat":"2026-01-02T03:04:06Z"},{"node_id":"gateway","is_gateway":true,"version":"v1.2.4","capabilities":[{"kind":2,"name":"router"}],"last_heartbeat":"2026-01-02T03:04:05Z"}]}`)
	}))
	defer server.Close()

	cfg := config.GatewayConfig{
		Panel: config.PanelConfig{
			ListenAddr: strings.TrimPrefix(server.URL, "http://"),
		},
	}
	lines, warning := readGatewayNodeStatusLines(context.Background(), cfg, nil)
	if warning != "" {
		t.Fatalf("warning = %q, want empty", warning)
	}
	if len(lines) != 3 {
		t.Fatalf("line count = %d, want 3", len(lines))
	}
	if lines[0] != "known nodes:    2" {
		t.Fatalf("first line = %q, want known node count", lines[0])
	}
	if !strings.Contains(lines[1], "gateway (gateway)") {
		t.Fatalf("expected gateway line, got %q", lines[1])
	}
	if !strings.Contains(lines[1], "version=v1.2.4") {
		t.Fatalf("expected gateway version, got %q", lines[1])
	}
	if !strings.Contains(lines[1], "capabilities=system:router") {
		t.Fatalf("expected gateway capabilities, got %q", lines[1])
	}
	if !strings.Contains(lines[2], "node-2 (node)") {
		t.Fatalf("expected worker line, got %q", lines[2])
	}
	if !strings.Contains(lines[2], "version=v1.2.3") {
		t.Fatalf("expected worker version, got %q", lines[2])
	}
	if !strings.Contains(lines[2], "capabilities=tool:web_search") {
		t.Fatalf("expected worker capabilities, got %q", lines[2])
	}
}

func TestReadGatewayNodeStatusLinesIncludesUnknownVersionWhenMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_gopher/panel/nodes" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"nodes":[{"node_id":"node-1","is_gateway":false,"capabilities":[{"kind":1,"name":"web_search"}],"last_heartbeat":"2026-01-02T03:04:06Z"}]}`)
	}))
	defer server.Close()

	cfg := config.GatewayConfig{
		Panel: config.PanelConfig{
			ListenAddr: strings.TrimPrefix(server.URL, "http://"),
		},
	}
	lines, warning := readGatewayNodeStatusLines(context.Background(), cfg, nil)
	if warning != "" {
		t.Fatalf("warning = %q, want empty", warning)
	}
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[1], "version=unknown") {
		t.Fatalf("expected unknown version fallback, got %q", lines[1])
	}
}

func TestReadGatewayNodeStatusLinesReturnsWarningWhenPanelUnreachable(t *testing.T) {
	cfg := config.GatewayConfig{
		Panel: config.PanelConfig{
			ListenAddr: "127.0.0.1:1",
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	lines, warning := readGatewayNodeStatusLines(ctx, cfg, nil)
	if len(lines) != 1 || lines[0] != "known nodes:    unknown" {
		t.Fatalf("lines = %#v, want unknown status line", lines)
	}
	if !strings.Contains(warning, "panel endpoint is not reachable") {
		t.Fatalf("warning = %q, want reachability warning", warning)
	}
}
