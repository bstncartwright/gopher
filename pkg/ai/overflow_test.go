package ai

import "testing"

func TestIsContextOverflowErrorPattern(t *testing.T) {
	message := AssistantMessage{StopReason: StopReasonError, ErrorMessage: "Your input exceeds the context window of this model"}
	if !IsContextOverflow(message, 0) {
		t.Fatalf("expected overflow detection for known error pattern")
	}
}

func TestIsContextOverflowSilentOverflow(t *testing.T) {
	message := AssistantMessage{
		StopReason: StopReasonStop,
		Usage: Usage{
			Input:     1000,
			CacheRead: 500,
		},
	}
	if !IsContextOverflow(message, 1200) {
		t.Fatalf("expected overflow detection for silent overflow")
	}
}
