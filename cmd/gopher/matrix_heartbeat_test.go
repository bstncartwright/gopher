package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type noopSessionManager struct{}

func (noopSessionManager) CreateSession(context.Context, sessionrt.CreateSessionOptions) (*sessionrt.Session, error) {
	return nil, nil
}

func (noopSessionManager) GetSession(context.Context, sessionrt.SessionID) (*sessionrt.Session, error) {
	return nil, nil
}

func (noopSessionManager) SendEvent(context.Context, sessionrt.Event) error {
	return nil
}

func (noopSessionManager) Subscribe(context.Context, sessionrt.SessionID) (<-chan sessionrt.Event, error) {
	ch := make(chan sessionrt.Event)
	close(ch)
	return ch, nil
}

func (noopSessionManager) CancelSession(context.Context, sessionrt.SessionID) error {
	return nil
}

func TestGatewayHeartbeatToolServiceSetAndDisable(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.json")
	writeConfigFile(t, configPath, map[string]any{
		"agent_id":             "writer",
		"name":                 "writer",
		"role":                 "assistant",
		"model_policy":         "zai:glm-5",
		"enabled_tools":        []any{"group:collaboration"},
		"max_context_messages": 40,
	})

	agent := &agentcore.Agent{
		ID:        "writer",
		Workspace: workspace,
		Config: agentcore.AgentConfig{
			AgentID: "writer",
			Name:    "writer",
		},
	}
	runner, err := gateway.NewHeartbeatRunner(gateway.HeartbeatRunnerOptions{
		Manager:  noopSessionManager{},
		Pipeline: &gateway.DMPipeline{},
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}
	service := newGatewayHeartbeatToolService(map[sessionrt.ActorID]*agentcore.Agent{
		"writer": agent,
	}, runner)

	prompt := "heartbeat check"
	ackMaxChars := 120
	timezone := "America/New_York"
	state, err := service.SetHeartbeat(context.Background(), agentcore.HeartbeatSetRequest{
		AgentID:      "writer",
		Every:        "15m",
		Prompt:       &prompt,
		AckMaxChars:  &ackMaxChars,
		UserTimezone: &timezone,
	})
	if err != nil {
		t.Fatalf("SetHeartbeat() error: %v", err)
	}
	if !state.Enabled {
		t.Fatalf("expected heartbeat to be enabled")
	}
	if state.Every != "15m0s" {
		t.Fatalf("every = %q, want 15m0s", state.Every)
	}
	if state.Prompt != "heartbeat check" {
		t.Fatalf("prompt = %q, want heartbeat check", state.Prompt)
	}
	if state.AckMaxChars != 120 {
		t.Fatalf("ack max chars = %d, want 120", state.AckMaxChars)
	}
	if state.UserTimezone != "America/New_York" {
		t.Fatalf("timezone = %q, want America/New_York", state.UserTimezone)
	}
	if !agent.Heartbeat.Enabled || agent.Heartbeat.Every != 15*time.Minute {
		t.Fatalf("agent heartbeat not updated: %#v", agent.Heartbeat)
	}

	doc := readConfigFile(t, configPath)
	heartbeatValue, ok := doc["heartbeat"].(map[string]any)
	if !ok {
		t.Fatalf("expected heartbeat object in config")
	}
	if heartbeatValue["every"] != "15m" {
		t.Fatalf("heartbeat.every = %#v, want 15m", heartbeatValue["every"])
	}

	disabled, err := service.DisableHeartbeat(context.Background(), "writer")
	if err != nil {
		t.Fatalf("DisableHeartbeat() error: %v", err)
	}
	if disabled.Enabled {
		t.Fatalf("expected heartbeat to be disabled")
	}
	doc = readConfigFile(t, configPath)
	if _, ok := doc["heartbeat"]; ok {
		t.Fatalf("did not expect heartbeat key in config after disable")
	}
}

func TestGatewayHeartbeatToolServiceRejectsInvalidTimezone(t *testing.T) {
	workspace := t.TempDir()
	configPath := filepath.Join(workspace, "config.json")
	writeConfigFile(t, configPath, map[string]any{
		"agent_id": "writer",
	})

	agent := &agentcore.Agent{
		ID:        "writer",
		Workspace: workspace,
		Config: agentcore.AgentConfig{
			AgentID: "writer",
		},
	}
	runner, err := gateway.NewHeartbeatRunner(gateway.HeartbeatRunnerOptions{
		Manager:  noopSessionManager{},
		Pipeline: &gateway.DMPipeline{},
	})
	if err != nil {
		t.Fatalf("NewHeartbeatRunner() error: %v", err)
	}
	service := newGatewayHeartbeatToolService(map[sessionrt.ActorID]*agentcore.Agent{
		"writer": agent,
	}, runner)

	timezone := "Not/A_Timezone"
	_, err = service.SetHeartbeat(context.Background(), agentcore.HeartbeatSetRequest{
		AgentID:      "writer",
		Every:        "15m",
		UserTimezone: &timezone,
	})
	if err == nil {
		t.Fatalf("expected invalid timezone error")
	}
}

func writeConfigFile(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readConfigFile(t *testing.T, path string) map[string]any {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return doc
}
