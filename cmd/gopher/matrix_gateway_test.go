package main

import (
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
