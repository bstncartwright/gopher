package nats

import (
	"context"
	"testing"
	"time"
)

func TestSubjectMatchesWildcards(t *testing.T) {
	tests := []struct {
		pattern string
		subject string
		want    bool
	}{
		{pattern: "node.*.status", subject: "node.n1.status", want: true},
		{pattern: "node.*.status", subject: "node.n1.capabilities", want: false},
		{pattern: "node.>", subject: "node.n1.capabilities", want: true},
		{pattern: "session.*.events", subject: "session.s1.events", want: true},
		{pattern: "session.*.events", subject: "session.s1.meta.events", want: false},
	}
	for _, tc := range tests {
		if got := subjectMatches(tc.pattern, tc.subject); got != tc.want {
			t.Fatalf("subjectMatches(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

func TestInMemoryBusRequestReply(t *testing.T) {
	bus := NewInMemoryBus()
	_, err := bus.Subscribe("node.n1.control", func(ctx context.Context, message Message) {
		_ = bus.Publish(ctx, Message{Subject: message.Reply, Data: []byte("ok")})
	})
	if err != nil {
		t.Fatalf("Subscribe() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	payload, err := bus.Request(ctx, "node.n1.control", []byte("ping"))
	if err != nil {
		t.Fatalf("Request() error: %v", err)
	}
	if string(payload) != "ok" {
		t.Fatalf("expected reply ok, got %q", string(payload))
	}
}
