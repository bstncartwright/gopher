package ai

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

var ErrStreamClosed = errors.New("assistant message event stream closed")

type AssistantMessageEventStream struct {
	mu      sync.Mutex
	events  chan AssistantMessageEvent
	done    bool
	dropped int
	result  AssistantMessage
	hasRes  bool
	wait    chan struct{}
}

func NewAssistantMessageEventStream(buffer int) *AssistantMessageEventStream {
	if buffer <= 0 {
		buffer = 256
	}
	return &AssistantMessageEventStream{
		events: make(chan AssistantMessageEvent, buffer),
		wait:   make(chan struct{}),
	}
}

func CreateAssistantMessageEventStream() *AssistantMessageEventStream {
	return NewAssistantMessageEventStream(256)
}

func (s *AssistantMessageEventStream) Events() <-chan AssistantMessageEvent {
	return s.events
}

func (s *AssistantMessageEventStream) Push(event AssistantMessageEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done {
		return
	}

	if event.Type == EventDone && event.Message != nil {
		s.result = *event.Message
		s.hasRes = true
		s.done = true
		slog.Debug("ai_event_stream: received terminal done event",
			"reason", event.Reason,
			"content_blocks", len(event.Message.Content),
			"error_message_length", len(event.Message.ErrorMessage),
		)
	} else if event.Type == EventError && event.Error != nil {
		s.result = *event.Error
		s.hasRes = true
		s.done = true
		slog.Debug("ai_event_stream: received terminal error event",
			"reason", event.Reason,
			"content_blocks", len(event.Error.Content),
			"error_message_length", len(event.Error.ErrorMessage),
		)
	}

	select {
	case s.events <- event:
	default:
		s.dropped++
	}

	if s.done {
		slog.Debug("ai_event_stream: closing event stream",
			"has_result", s.hasRes,
			"dropped_events", s.dropped,
		)
		select {
		case <-s.wait:
		default:
			close(s.wait)
			close(s.events)
		}
	}
}

func (s *AssistantMessageEventStream) End(result *AssistantMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.done {
		return
	}
	s.done = true
	if result != nil {
		s.result = *result
		s.hasRes = true
	}
	slog.Debug("ai_event_stream: end called",
		"has_result", s.hasRes,
		"content_blocks", len(s.result.Content),
		"stop_reason", s.result.StopReason,
		"error_message_length", len(s.result.ErrorMessage),
	)

	select {
	case <-s.wait:
	default:
		close(s.wait)
		close(s.events)
	}
}

func (s *AssistantMessageEventStream) Dropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *AssistantMessageEventStream) Result(ctx context.Context) (AssistantMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	slog.Debug("ai_event_stream: awaiting result")

	select {
	case <-ctx.Done():
		slog.Warn("ai_event_stream: result wait cancelled", "error", ctx.Err())
		return AssistantMessage{}, ctx.Err()
	case <-s.wait:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasRes {
		slog.Error("ai_event_stream: result wait finished without terminal result", "dropped_events", s.dropped)
		return AssistantMessage{}, ErrStreamClosed
	}
	slog.Debug("ai_event_stream: returning result",
		"stop_reason", s.result.StopReason,
		"content_blocks", len(s.result.Content),
		"error_message_length", len(s.result.ErrorMessage),
		"dropped_events", s.dropped,
	)
	return s.result, nil
}
