package session

import "testing"

func TestMessageFromPayloadParsesTargetActorID(t *testing.T) {
	msg, ok := messageFromPayload(map[string]any{
		"role":            "user",
		"content":         "ping",
		"target_actor_id": "writer",
	})
	if !ok {
		t.Fatalf("expected payload decode success")
	}
	if msg.Role != RoleUser {
		t.Fatalf("role = %q, want %q", msg.Role, RoleUser)
	}
	if msg.Content != "ping" {
		t.Fatalf("content = %q, want ping", msg.Content)
	}
	if msg.TargetActorID != "writer" {
		t.Fatalf("target_actor_id = %q, want writer", msg.TargetActorID)
	}
}

func TestMessageFromPayloadRejectsInvalidTargetActorIDType(t *testing.T) {
	if _, ok := messageFromPayload(map[string]any{
		"role":            "user",
		"content":         "ping",
		"target_actor_id": 123,
	}); ok {
		t.Fatalf("expected decode failure for non-string target_actor_id")
	}
}
