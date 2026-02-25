package gateway

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type fakeTraceSender struct {
	sent           []transport.OutboundMessage
	failOnCall     int
	callCount      int
	publishSuccess int
	publishFailure int
}

func (s *fakeTraceSender) SendMessage(_ context.Context, message transport.OutboundMessage) error {
	s.callCount++
	if s.failOnCall > 0 && s.callCount == s.failOnCall {
		return fmt.Errorf("send failed")
	}
	s.sent = append(s.sent, message)
	return nil
}

func (s *fakeTraceSender) RecordTracePublishSuccess() { s.publishSuccess++ }
func (s *fakeTraceSender) RecordTracePublishFailure() { s.publishFailure++ }

type fakeTraceSenderWithResult struct {
	fakeTraceSender
	eventIDs []string
}

func (s *fakeTraceSenderWithResult) SendMessageWithResult(_ context.Context, message transport.OutboundMessage) (transport.OutboundSendResult, error) {
	if err := s.SendMessage(context.Background(), message); err != nil {
		return transport.OutboundSendResult{}, err
	}
	out := transport.OutboundSendResult{}
	if len(s.eventIDs) > 0 {
		out.EventID = s.eventIDs[0]
		s.eventIDs = s.eventIDs[1:]
	}
	return out, nil
}

func TestRenderTraceEventCardsSupportsCoreEventTypes(t *testing.T) {
	now := time.Date(2026, time.February, 20, 13, 0, 0, 0, time.UTC)
	cases := []sessionrt.Event{
		{
			Seq:       1,
			From:      "agent:milo",
			Type:      sessionrt.EventMessage,
			Timestamp: now,
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleAgent,
				Content: "hello",
			},
		},
		{
			Seq:       2,
			From:      "agent:milo",
			Type:      sessionrt.EventToolCall,
			Timestamp: now,
			Payload: map[string]any{
				"name": "exec",
				"args": map[string]any{"command": "echo hi"},
			},
		},
		{
			Seq:       3,
			From:      "agent:milo",
			Type:      sessionrt.EventToolResult,
			Timestamp: now,
			Payload: map[string]any{
				"name":   "exec",
				"status": "ok",
				"result": map[string]any{"stdout": "hi"},
			},
		},
		{
			Seq:       4,
			From:      "agent:milo",
			Type:      sessionrt.EventAgentDelta,
			Timestamp: now,
			Payload:   map[string]any{"delta": "hel"},
		},
		{
			Seq:       5,
			From:      "system",
			Type:      sessionrt.EventControl,
			Timestamp: now,
			Payload:   sessionrt.ControlPayload{Action: "session.created", Reason: "test"},
		},
		{
			Seq:       6,
			From:      "agent:milo",
			Type:      sessionrt.EventError,
			Timestamp: now,
			Payload:   sessionrt.ErrorPayload{Message: "boom"},
		},
	}

	for _, event := range cases {
		messages := renderTraceEventCards(event, 3500)
		if len(messages) == 0 {
			t.Fatalf("expected messages for event type %q", event.Type)
		}
		if !strings.Contains(messages[0], fmt.Sprintf("[#%d]", event.Seq)) {
			t.Fatalf("missing sequence header in %q", messages[0])
		}
	}
	if !strings.HasPrefix(renderTraceEventCards(cases[0], 3500)[0], "🤖 ") {
		t.Fatalf("expected emoji prefix in agent message header")
	}
}

func TestTraceEventPublisherSuppressesDeltaEvents(t *testing.T) {
	sender := &fakeTraceSender{}
	publisher := NewTraceEventPublisher(sender)
	event := sessionrt.Event{
		Seq:  99,
		From: "agent:milo",
		Type: sessionrt.EventAgentDelta,
		Payload: map[string]any{
			"delta": "hello",
		},
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", event); err != nil {
		t.Fatalf("PublishEvent() error: %v", err)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("sent messages = %d, want 0", len(sender.sent))
	}
	if sender.publishSuccess != 0 || sender.publishFailure != 0 {
		t.Fatalf("success/failure = %d/%d, want 0/0", sender.publishSuccess, sender.publishFailure)
	}
}

func TestTraceEventPublisherIncludesFirstProgressDeltaWhenEnabled(t *testing.T) {
	sender := &fakeTraceSender{}
	publisher := NewTraceEventPublisherWithOptions(sender, TraceEventPublisherOptions{
		IncludeProgressDeltas: true,
	})
	delta := sessionrt.Event{
		SessionID: "sess-1",
		Seq:       1,
		From:      "agent:milo",
		Type:      sessionrt.EventAgentDelta,
		Payload: map[string]any{
			"delta": "Analyzing repository and collecting context",
		},
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", delta); err != nil {
		t.Fatalf("PublishEvent(delta) error: %v", err)
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", delta); err != nil {
		t.Fatalf("PublishEvent(delta second) error: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sender.sent))
	}
	if !strings.Contains(sender.sent[0].Text, "progress:") {
		t.Fatalf("expected progress delta body in %q", sender.sent[0].Text)
	}

	userTrigger := sessionrt.Event{
		SessionID: "sess-1",
		Seq:       2,
		From:      "user:@alice:local",
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "next task",
		},
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", userTrigger); err != nil {
		t.Fatalf("PublishEvent(user trigger) error: %v", err)
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", sessionrt.Event{
		SessionID: "sess-1",
		Seq:       3,
		From:      "agent:milo",
		Type:      sessionrt.EventAgentDelta,
		Payload: map[string]any{
			"delta": "Running command",
		},
	}); err != nil {
		t.Fatalf("PublishEvent(delta after trigger) error: %v", err)
	}
	if len(sender.sent) != 3 {
		t.Fatalf("sent messages = %d, want 3", len(sender.sent))
	}
}

func TestRenderTraceEventCardsRedactsSensitiveKeys(t *testing.T) {
	event := sessionrt.Event{
		Seq:  7,
		From: "agent:milo",
		Type: sessionrt.EventToolCall,
		Payload: map[string]any{
			"name": "web_search",
			"args": map[string]any{
				"query":         "hello",
				"authorization": "Bearer abc",
				"api_key":       "sk-secret",
				"nested": map[string]any{
					"token": "tok",
				},
			},
		},
	}
	messages := renderTraceEventCards(event, 3500)
	if len(messages) != 1 {
		t.Fatalf("message count = %d, want 1", len(messages))
	}
	joined := messages[0]
	if strings.Contains(strings.ToLower(joined), "bearer abc") {
		t.Fatalf("expected authorization to be redacted: %q", joined)
	}
	if strings.Contains(strings.ToLower(joined), "sk-secret") {
		t.Fatalf("expected api_key to be redacted: %q", joined)
	}
	if strings.Contains(strings.ToLower(joined), "\"tok\"") {
		t.Fatalf("expected nested token to be redacted: %q", joined)
	}
	if !strings.Contains(joined, "[redacted]") {
		t.Fatalf("expected redaction marker in %q", joined)
	}
}

func TestRenderTraceEventCardsSplitsDeterministically(t *testing.T) {
	event := sessionrt.Event{
		Seq:  8,
		From: "agent:milo",
		Type: sessionrt.EventAgentDelta,
		Payload: map[string]any{
			"delta": strings.Repeat("x", 5000),
		},
	}
	first := renderTraceEventCards(event, 300)
	second := renderTraceEventCards(event, 300)
	if len(first) <= 1 {
		t.Fatalf("expected split output, got %d part(s)", len(first))
	}
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Fatalf("expected deterministic split output")
	}
	for _, message := range first {
		if len([]rune(message)) > 300 {
			t.Fatalf("trace message rune length %d exceeds 300", len([]rune(message)))
		}
	}
}

func TestTraceEventPublisherPublishesAndTracksMetrics(t *testing.T) {
	sender := &fakeTraceSender{}
	publisher := NewTraceEventPublisherWithMaxChars(sender, 400)
	event := sessionrt.Event{
		Seq:  9,
		From: "agent:milo",
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: "hello",
		},
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", event); err != nil {
		t.Fatalf("PublishEvent() error: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent messages = %d, want 1", len(sender.sent))
	}
	if sender.publishSuccess != 1 || sender.publishFailure != 0 {
		t.Fatalf("success/failure = %d/%d, want 1/0", sender.publishSuccess, sender.publishFailure)
	}
}

func TestTraceEventPublisherTracksFailure(t *testing.T) {
	sender := &fakeTraceSender{failOnCall: 1}
	publisher := NewTraceEventPublisher(sender)
	event := sessionrt.Event{
		Seq:  10,
		From: "agent:milo",
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: "hello",
		},
	}
	err := publisher.PublishEvent(context.Background(), "!trace:one", event)
	if err == nil {
		t.Fatalf("expected publish error")
	}
	if sender.publishSuccess != 0 || sender.publishFailure != 1 {
		t.Fatalf("success/failure = %d/%d, want 0/1", sender.publishSuccess, sender.publishFailure)
	}
}

func TestTraceEventPublisherThreadsEventsUnderUserTrigger(t *testing.T) {
	sender := &fakeTraceSenderWithResult{
		eventIDs: []string{"$root", "$tool"},
	}
	publisher := NewTraceEventPublisher(sender)
	trigger := sessionrt.Event{
		SessionID: "sess-1",
		Seq:       11,
		From:      "user:@alice:local",
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "help me",
		},
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", trigger); err != nil {
		t.Fatalf("PublishEvent(trigger) error: %v", err)
	}
	toolCall := sessionrt.Event{
		SessionID: "sess-1",
		Seq:       12,
		From:      "agent:milo",
		Type:      sessionrt.EventToolCall,
		Payload: map[string]any{
			"name": "exec",
			"args": map[string]any{"cmd": "echo hi"},
		},
	}
	if err := publisher.PublishEvent(context.Background(), "!trace:one", toolCall); err != nil {
		t.Fatalf("PublishEvent(tool) error: %v", err)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("sent messages = %d, want 2", len(sender.sent))
	}
	if sender.sent[0].ThreadRootEventID != "" {
		t.Fatalf("trigger thread root = %q, want empty", sender.sent[0].ThreadRootEventID)
	}
	if sender.sent[1].ThreadRootEventID != "$root" {
		t.Fatalf("tool thread root = %q, want $root", sender.sent[1].ThreadRootEventID)
	}
}
