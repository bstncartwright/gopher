package a2a

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoverFallsBackToLegacyWellKnownPath(t *testing.T) {
	serverURL := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/agent-card.json":
			http.NotFound(w, r)
		case "/.well-known/agent.json":
			_, _ = w.Write([]byte(`{"name":"legacy","url":"` + serverURL + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serverURL = server.URL

	client := NewHTTPClient()
	card, err := client.Discover(context.Background(), Remote{
		BaseURL:                   server.URL,
		CompatLegacyWellKnownPath: true,
	})
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if card.Name != "legacy" {
		t.Fatalf("card.Name = %q, want legacy", card.Name)
	}
}

func TestAgentCardValidateHTTPJSONRejectsNonHTTPJSONInterface(t *testing.T) {
	card := AgentCard{
		Name: "bad",
		Interfaces: []AgentInterface{{
			URL:             "grpc://example.invalid",
			ProtocolBinding: "gRPC",
		}},
	}
	if err := card.ValidateHTTPJSON(); err == nil {
		t.Fatalf("expected ValidateHTTPJSON error")
	}
}

func TestSubscribeTaskParsesSSEUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tasks/task-1:subscribe" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"task\":{\"id\":\"task-1\",\"status\":\"working\",\"message\":{\"parts\":[{\"text\":\"step\"}]}}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"task\":{\"id\":\"task-1\",\"status\":\"completed\",\"message\":{\"parts\":[{\"text\":\"done\"}]}}}\n\n")
	}))
	defer server.Close()

	client := NewHTTPClient()
	var got []string
	err := client.SubscribeTask(context.Background(), server.URL, Remote{}, "task-1", func(task Task) error {
		got = append(got, string(task.NormalizedStatus())+":"+task.LatestText())
		return nil
	})
	if err != nil {
		t.Fatalf("SubscribeTask() error: %v", err)
	}
	if strings.Join(got, ",") != "working:step,completed:done" {
		t.Fatalf("updates = %v", got)
	}
}
