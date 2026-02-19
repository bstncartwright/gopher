package ai

import (
	"context"
	"errors"
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
	} else if event.Type == EventError && event.Error != nil {
		s.result = *event.Error
		s.hasRes = true
		s.done = true
	}

	select {
	case s.events <- event:
	default:
		s.dropped++
	}

	if s.done {
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

	select {
	case <-ctx.Done():
		return AssistantMessage{}, ctx.Err()
	case <-s.wait:
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasRes {
		return AssistantMessage{}, ErrStreamClosed
	}
	return s.result, nil
}
