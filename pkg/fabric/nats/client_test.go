package nats

import (
	"context"
	"testing"
)

func TestClientValidationWithoutConnection(t *testing.T) {
	client := &Client{}
	if err := client.Publish(context.Background(), Message{Subject: "a"}); err == nil {
		t.Fatalf("expected publish without connection to fail")
	}
	if _, err := client.Subscribe("a", func(context.Context, Message) {}); err == nil {
		t.Fatalf("expected subscribe without connection to fail")
	}
	if _, err := client.Request(context.Background(), "a", nil); err == nil {
		t.Fatalf("expected request without connection to fail")
	}
}

func TestClientValidatesSubject(t *testing.T) {
	client := &Client{}
	if _, err := client.Subscribe("", func(context.Context, Message) {}); err == nil {
		t.Fatalf("expected empty subject to fail")
	}
}
