package main

import (
	"testing"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestMatrixMentionAgentSelectorSingleAgentSession(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@writer:example.com": "writer",
		},
	})
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"writer": {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	actorID, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "hello",
		},
	})
	if !ok {
		t.Fatalf("expected selection for single-agent session")
	}
	if actorID != "writer" {
		t.Fatalf("actor id = %q, want writer", actorID)
	}
}

func TestMatrixMentionAgentSelectorMultiAgentRequiresSingleMention(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	})
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
			"matrix:@user:example.com": {
				ID:   "matrix:@user:example.com",
				Type: sessionrt.ActorHuman,
			},
		},
	}
	actorID, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "@writer:example.com please handle this.",
		},
	})
	if !ok {
		t.Fatalf("expected selection for single mention")
	}
	if actorID != "writer" {
		t.Fatalf("actor id = %q, want writer", actorID)
	}

	if _, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "no explicit mention",
		},
	}); ok {
		t.Fatalf("did not expect selection without mention")
	}

	if _, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "@writer:example.com and @planner:example.com both review",
		},
	}); ok {
		t.Fatalf("did not expect selection when multiple agent mentions are present")
	}
}

func TestMatrixMentionAgentSelectorUsesTargetActorIDFirst(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	})
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	actorID, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:          sessionrt.RoleUser,
			Content:       "no mention needed",
			TargetActorID: "writer",
		},
	})
	if !ok {
		t.Fatalf("expected selection for target_actor_id")
	}
	if actorID != "writer" {
		t.Fatalf("actor id = %q, want writer", actorID)
	}
}

func TestMatrixMentionAgentSelectorIgnoresInvalidTargetActorID(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	})
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	if _, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:          sessionrt.RoleUser,
			Content:       "no mention",
			TargetActorID: "agent:missing",
		},
	}); ok {
		t.Fatalf("did not expect selection for invalid target_actor_id")
	}
}
