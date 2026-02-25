package session

import (
	"context"
	"errors"
)

type MultiEventPublisher struct {
	publishers []EventPublisher
}

var _ EventPublisher = (*MultiEventPublisher)(nil)

func NewMultiEventPublisher(publishers ...EventPublisher) *MultiEventPublisher {
	filtered := make([]EventPublisher, 0, len(publishers))
	for _, publisher := range publishers {
		if publisher == nil {
			continue
		}
		filtered = append(filtered, publisher)
	}
	return &MultiEventPublisher{publishers: filtered}
}

func (m *MultiEventPublisher) PublishEvent(ctx context.Context, event Event) error {
	if m == nil || len(m.publishers) == 0 {
		return nil
	}
	var errs []error
	for _, publisher := range m.publishers {
		if err := publisher.PublishEvent(ctx, event); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
