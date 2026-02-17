package nats

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

type Message struct {
	Subject string
	Reply   string
	Data    []byte
}

type Handler func(context.Context, Message)

type Subscription interface {
	Unsubscribe()
}

type Fabric interface {
	Publish(ctx context.Context, message Message) error
	Subscribe(subject string, handler Handler) (Subscription, error)
	Request(ctx context.Context, subject string, data []byte) ([]byte, error)
}

type InMemoryBus struct {
	mu sync.RWMutex

	nextID uint64
	subs   map[uint64]*inMemorySubscription
}

type inMemorySubscription struct {
	id      uint64
	pattern string
	handler Handler
	bus     *InMemoryBus
}

func NewInMemoryBus() *InMemoryBus {
	return &InMemoryBus{subs: make(map[uint64]*inMemorySubscription)}
}

func (b *InMemoryBus) Publish(ctx context.Context, message Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if strings.TrimSpace(message.Subject) == "" {
		return fmt.Errorf("subject is required")
	}

	b.mu.RLock()
	matched := make([]*inMemorySubscription, 0, len(b.subs))
	for _, sub := range b.subs {
		if subjectMatches(sub.pattern, message.Subject) {
			matched = append(matched, sub)
		}
	}
	b.mu.RUnlock()

	for _, sub := range matched {
		handler := sub.handler
		msgCopy := Message{Subject: message.Subject, Reply: message.Reply, Data: append([]byte(nil), message.Data...)}
		go handler(ctx, msgCopy)
	}

	return nil
}

func (b *InMemoryBus) Subscribe(subject string, handler Handler) (Subscription, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, fmt.Errorf("subject is required")
	}
	if handler == nil {
		return nil, fmt.Errorf("handler is required")
	}
	id := atomic.AddUint64(&b.nextID, 1)
	sub := &inMemorySubscription{
		id:      id,
		pattern: strings.TrimSpace(subject),
		handler: handler,
		bus:     b,
	}
	b.mu.Lock()
	b.subs[id] = sub
	b.mu.Unlock()
	return sub, nil
}

func (b *InMemoryBus) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	inboxID := atomic.AddUint64(&b.nextID, 1)
	inbox := fmt.Sprintf("_INBOX.%d", inboxID)
	response := make(chan []byte, 1)
	responseSub, err := b.Subscribe(inbox, func(_ context.Context, message Message) {
		select {
		case response <- append([]byte(nil), message.Data...):
		default:
		}
	})
	if err != nil {
		return nil, err
	}
	defer responseSub.Unsubscribe()

	if err := b.Publish(ctx, Message{Subject: subject, Reply: inbox, Data: data}); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case payload := <-response:
		return payload, nil
	}
}

func (s *inMemorySubscription) Unsubscribe() {
	if s == nil || s.bus == nil {
		return
	}
	s.bus.mu.Lock()
	delete(s.bus.subs, s.id)
	s.bus.mu.Unlock()
}

func subjectMatches(pattern, subject string) bool {
	pattern = strings.TrimSpace(pattern)
	subject = strings.TrimSpace(subject)
	if pattern == "" || subject == "" {
		return false
	}
	if pattern == subject {
		return true
	}

	p := strings.Split(pattern, ".")
	s := strings.Split(subject, ".")
	pi := 0
	si := 0

	for {
		if pi >= len(p) && si >= len(s) {
			return true
		}
		if pi >= len(p) {
			return false
		}

		token := p[pi]
		switch token {
		case ">":
			return true
		case "*":
			if si >= len(s) {
				return false
			}
			pi++
			si++
		default:
			if si >= len(s) || token != s[si] {
				return false
			}
			pi++
			si++
		}
	}
}
