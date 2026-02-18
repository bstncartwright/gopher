package main

import (
	"testing"

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
