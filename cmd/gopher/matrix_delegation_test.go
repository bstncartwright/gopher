package main

import (
	"testing"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func TestBuildDelegationKickoffMessagePrefixesTargetMention(t *testing.T) {
	got := buildDelegationKickoffMessage("@writer:example.com", "Please review this.")
	if got != "@writer:example.com Please review this." {
		t.Fatalf("kickoff = %q", got)
	}
}

func TestFirstHumanMatrixParticipantFindsMatrixActor(t *testing.T) {
	session := &sessionrt.Session{
		Participants: map[sessionrt.ActorID]sessionrt.Participant{
			"writer":                {ID: "writer", Type: sessionrt.ActorAgent},
			"matrix:@user:example":  {ID: "matrix:@user:example", Type: sessionrt.ActorHuman},
			"matrix:@other:example": {ID: "matrix:@other:example", Type: sessionrt.ActorHuman},
			"system:coordinator":    {ID: "system:coordinator", Type: sessionrt.ActorSystem},
		},
	}
	actorID, userID, err := firstHumanMatrixParticipant(session)
	if err != nil {
		t.Fatalf("firstHumanMatrixParticipant() error: %v", err)
	}
	if actorID != "matrix:@other:example" {
		t.Fatalf("actor id = %q, want matrix:@other:example", actorID)
	}
	if userID != "@other:example" {
		t.Fatalf("user id = %q, want @other:example", userID)
	}
}
