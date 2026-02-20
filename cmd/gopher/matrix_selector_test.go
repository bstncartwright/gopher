package main

import (
	"context"
	"testing"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type stubUntaggedRouter struct {
	selected []sessionrt.ActorID
	called   int
}

func (s *stubUntaggedRouter) SelectResponders(_ context.Context, _ matrixUntaggedResponderSelectionInput) ([]sessionrt.ActorID, error) {
	s.called++
	out := make([]sessionrt.ActorID, len(s.selected))
	copy(out, s.selected)
	return out, nil
}

func TestMatrixMentionAgentSelectorSingleAgentSession(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@writer:example.com": "writer",
		},
	}, nil)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"writer": {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	actorIDs, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "hello",
		},
	})
	if !ok {
		t.Fatalf("expected selection for single-agent session")
	}
	if len(actorIDs) != 1 || actorIDs[0] != "writer" {
		t.Fatalf("actor ids = %v, want [writer]", actorIDs)
	}
}

func TestMatrixMentionAgentSelectorUsesExplicitMentions(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	}, nil)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}

	actorIDs, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "@writer:example.com please handle this.",
		},
	})
	if !ok {
		t.Fatalf("expected selection for single mention")
	}
	if len(actorIDs) != 1 || actorIDs[0] != "writer" {
		t.Fatalf("actor ids = %v, want [writer]", actorIDs)
	}

	actorIDs, ok = selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "@writer:example.com and @planner:example.com both review",
		},
	})
	if !ok {
		t.Fatalf("expected selection for multi-mention")
	}
	if len(actorIDs) != 2 || actorIDs[0] != "planner" || actorIDs[1] != "writer" {
		t.Fatalf("actor ids = %v, want [planner writer]", actorIDs)
	}
}

func TestMatrixMentionAgentSelectorUsesFallbackForUntaggedMessage(t *testing.T) {
	fallback := &stubUntaggedRouter{selected: []sessionrt.ActorID{"writer"}}
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
		UserByActorID: map[sessionrt.ActorID]string{
			"planner": "@planner:example.com",
			"writer":  "@writer:example.com",
		},
	}, fallback)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}

	actorIDs, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "untagged request",
		},
	})
	if !ok {
		t.Fatalf("expected selection from fallback")
	}
	if len(actorIDs) != 1 || actorIDs[0] != "writer" {
		t.Fatalf("actor ids = %v, want [writer]", actorIDs)
	}
	if fallback.called != 1 {
		t.Fatalf("fallback called %d times, want 1", fallback.called)
	}
}

func TestMatrixMentionAgentSelectorFallbackFiltersInvalidActors(t *testing.T) {
	fallback := &stubUntaggedRouter{selected: []sessionrt.ActorID{"writer", "missing", "writer", "planner"}}
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{}, fallback)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	actorIDs, ok := selector(session, sessionrt.Event{
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "no tags",
		},
	})
	if !ok {
		t.Fatalf("expected filtered selection")
	}
	if len(actorIDs) != 2 || actorIDs[0] != "writer" || actorIDs[1] != "planner" {
		t.Fatalf("actor ids = %v, want [writer planner]", actorIDs)
	}
}

func TestMatrixMentionAgentSelectorUsesTargetActorIDFirst(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	}, nil)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	actorIDs, ok := selector(session, sessionrt.Event{
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
	if len(actorIDs) != 1 || actorIDs[0] != "writer" {
		t.Fatalf("actor ids = %v, want [writer]", actorIDs)
	}
}

func TestMatrixMentionAgentSelectorIgnoresInvalidTargetActorID(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	}, nil)
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

func TestMatrixMentionAgentSelectorRoutesAgentSenderToOtherParticipants(t *testing.T) {
	fallback := &stubUntaggedRouter{selected: []sessionrt.ActorID{"planner"}}
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	}, fallback)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	actorIDs, ok := selector(session, sessionrt.Event{
		From: "writer",
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "can someone review this?",
		},
	})
	if !ok {
		t.Fatalf("expected selection for agent-authored message")
	}
	if len(actorIDs) != 1 || actorIDs[0] != "planner" {
		t.Fatalf("actor ids = %v, want [planner]", actorIDs)
	}
	if fallback.called != 0 {
		t.Fatalf("fallback called %d times, want 0", fallback.called)
	}
}

func TestMatrixMentionAgentSelectorAgentSenderMentionDoesNotSelectSelf(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{
		ActorByUserID: map[string]sessionrt.ActorID{
			"@planner:example.com": "planner",
			"@writer:example.com":  "writer",
		},
	}, nil)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	actorIDs, ok := selector(session, sessionrt.Event{
		From: "writer",
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "@writer:example.com this is mine",
		},
	})
	if !ok {
		t.Fatalf("expected selection for agent-authored message")
	}
	if len(actorIDs) != 1 || actorIDs[0] != "planner" {
		t.Fatalf("actor ids = %v, want [planner]", actorIDs)
	}
}

func TestMatrixMentionAgentSelectorAgentSenderSelfTargetIgnored(t *testing.T) {
	selector := matrixMentionAgentSelector(agentMatrixIdentitySet{}, nil)
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"planner": {ID: "planner", Type: sessionrt.ActorAgent},
			"writer":  {ID: "writer", Type: sessionrt.ActorAgent},
		},
	}
	if _, ok := selector(session, sessionrt.Event{
		From: "writer",
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:          sessionrt.RoleUser,
			Content:       "echo?",
			TargetActorID: "writer",
		},
	}); ok {
		t.Fatalf("did not expect selection when sender targets itself")
	}
}
