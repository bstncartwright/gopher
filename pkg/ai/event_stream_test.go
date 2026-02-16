package ai

import (
	"context"
	"testing"
	"time"
)

func TestAssistantMessageEventStreamDone(t *testing.T) {
	stream := CreateAssistantMessageEventStream()
	model := Model{ID: "m", API: APIOpenAICompletions, Provider: ProviderOpenAI}
	msg := NewAssistantMessage(model)
	msg.StopReason = StopReasonStop
	stream.Push(AssistantMessageEvent{Type: EventStart, Partial: &msg})
	stream.Push(AssistantMessageEvent{Type: EventDone, Reason: StopReasonStop, Message: &msg})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.StopReason != StopReasonStop {
		t.Fatalf("expected stop reason %q, got %q", StopReasonStop, result.StopReason)
	}
}

func TestAssistantMessageEventStreamError(t *testing.T) {
	stream := CreateAssistantMessageEventStream()
	model := Model{ID: "m", API: APIOpenAICompletions, Provider: ProviderOpenAI}
	msg := NewAssistantMessage(model)
	msg.StopReason = StopReasonError
	msg.ErrorMessage = "boom"
	stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopReasonError, Error: &msg})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result() error: %v", err)
	}
	if result.ErrorMessage != "boom" {
		t.Fatalf("expected error message %q, got %q", "boom", result.ErrorMessage)
	}
}
