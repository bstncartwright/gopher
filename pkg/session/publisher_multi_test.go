package session

import (
	"context"
	"errors"
	"testing"
)

type recordingPublisher struct {
	count int
	err   error
}

func (p *recordingPublisher) PublishEvent(_ context.Context, _ Event) error {
	p.count++
	return p.err
}

func TestMultiEventPublisherFanout(t *testing.T) {
	a := &recordingPublisher{}
	b := &recordingPublisher{}
	m := NewMultiEventPublisher(a, b)

	err := m.PublishEvent(context.Background(), Event{SessionID: "s1", Type: EventMessage})
	if err != nil {
		t.Fatalf("PublishEvent() error: %v", err)
	}
	if a.count != 1 || b.count != 1 {
		t.Fatalf("expected both publishers to be called once, got a=%d b=%d", a.count, b.count)
	}
}

func TestMultiEventPublisherJoinsErrors(t *testing.T) {
	a := &recordingPublisher{err: errors.New("a failed")}
	b := &recordingPublisher{err: errors.New("b failed")}
	m := NewMultiEventPublisher(a, b)

	err := m.PublishEvent(context.Background(), Event{SessionID: "s1", Type: EventMessage})
	if err == nil {
		t.Fatalf("expected joined error")
	}
}
