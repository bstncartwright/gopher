package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestBuildAgentMatrixIdentitySetUsesAgentIDsAsLocalparts(t *testing.T) {
	runtime := &gatewayAgentRuntime{
		DefaultActorID: "writer",
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"planner": nil,
			"writer":  nil,
		},
	}

	identities, err := buildAgentMatrixIdentitySet(runtime, "@gopher:example.com")
	if err != nil {
		t.Fatalf("buildAgentMatrixIdentitySet() error: %v", err)
	}

	if identities.DefaultUserID != "@writer:example.com" {
		t.Fatalf("default user = %q, want @writer:example.com", identities.DefaultUserID)
	}
	if identities.UserByActorID["planner"] != "@planner:example.com" {
		t.Fatalf("planner user = %q, want @planner:example.com", identities.UserByActorID["planner"])
	}
	if identities.ActorByUserID["@writer:example.com"] != "writer" {
		t.Fatalf("writer actor mapping missing")
	}
}

func TestBuildAgentMatrixIdentitySetRejectsMissingTemplateDomain(t *testing.T) {
	runtime := &gatewayAgentRuntime{
		DefaultActorID: "writer",
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"writer": nil,
		},
	}

	if _, err := buildAgentMatrixIdentitySet(runtime, ""); err == nil {
		t.Fatalf("expected error for missing template user id")
	}
}

func TestCollectHeartbeatSchedulesIncludesOnlyEnabledAgents(t *testing.T) {
	runtime := &gatewayAgentRuntime{
		Agents: map[sessionrt.ActorID]*agentcore.Agent{
			"writer": {
				Config: agentcore.AgentConfig{
					UserTimezone: "America/New_York",
				},
				Heartbeat: agentcore.AgentHeartbeat{
					Enabled:     true,
					Every:       15 * time.Minute,
					Prompt:      "hb",
					AckMaxChars: 120,
				},
			},
			"planner": {
				Heartbeat: agentcore.AgentHeartbeat{
					Enabled: false,
					Every:   10 * time.Minute,
				},
			},
		},
	}

	schedules := collectHeartbeatSchedules(runtime)
	if len(schedules) != 1 {
		t.Fatalf("schedule count = %d, want 1", len(schedules))
	}
	if schedules[0].AgentID != "writer" {
		t.Fatalf("agent id = %q, want writer", schedules[0].AgentID)
	}
	if schedules[0].Every != 15*time.Minute {
		t.Fatalf("every = %s, want 15m", schedules[0].Every)
	}
	if schedules[0].Prompt != "hb" {
		t.Fatalf("prompt = %q, want hb", schedules[0].Prompt)
	}
	if schedules[0].AckMaxChars != 120 {
		t.Fatalf("ack max = %d, want 120", schedules[0].AckMaxChars)
	}
	if schedules[0].Timezone != "America/New_York" {
		t.Fatalf("timezone = %q, want America/New_York", schedules[0].Timezone)
	}
}

func TestResolveGatewayDataDirUsesWorkspaceWhenAlreadyDotGopher(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, ".gopher")
	if err := os.MkdirAll(filepath.Join(workspace, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir workspace sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "sessions", "conversation_bindings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write bindings: %v", err)
	}
	got := resolveGatewayDataDir(workspace)
	if got != workspace {
		t.Fatalf("resolveGatewayDataDir() = %q, want %q", got, workspace)
	}
}

func TestResolveGatewayDataDirFallsBackToLegacyNestedData(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, ".gopher")
	legacy := filepath.Join(workspace, ".gopher", "sessions")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir legacy sessions: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "conversation_bindings.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write legacy bindings: %v", err)
	}
	got := resolveGatewayDataDir(workspace)
	want := filepath.Join(workspace, ".gopher")
	if got != want {
		t.Fatalf("resolveGatewayDataDir() = %q, want %q", got, want)
	}
}

func TestSelectCatchupReplayEventsReturnsOnlyNewerEventsChronologically(t *testing.T) {
	events := []matrixTimelineEvent{
		{EventID: "$5"},
		{EventID: "$4"},
		{EventID: "$3"},
		{EventID: "$2"},
		{EventID: "$1"},
	}
	got := selectCatchupReplayEvents(events, "$3")
	if len(got) != 2 {
		t.Fatalf("replay len = %d, want 2", len(got))
	}
	if got[0].EventID != "$4" || got[1].EventID != "$5" {
		t.Fatalf("replay order = [%s,%s], want [$4,$5]", got[0].EventID, got[1].EventID)
	}
}

func TestTraceRoomNameFromSessionIDUsesPrefixAndTruncation(t *testing.T) {
	name := traceRoomNameFromSessionID("sess-1234567890abcdef")
	if name != "trace-sess-1234567" {
		t.Fatalf("trace room name = %q, want trace-sess-1234567", name)
	}
}
