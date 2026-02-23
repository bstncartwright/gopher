package agentcore

import (
	"fmt"
	"time"

	"github.com/bstncartwright/gopher/pkg/ai"
)

type eventEmitter struct {
	agentID   string
	sessionID string
	logger    EventLogger
	onEmit    func(Event) error
	events    []Event
}

func newEventEmitter(agentID, sessionID string, logger EventLogger, onEmit func(Event) error) *eventEmitter {
	return &eventEmitter{
		agentID:   agentID,
		sessionID: sessionID,
		logger:    logger,
		onEmit:    onEmit,
		events:    make([]Event, 0, 16),
	}
}

func (e *eventEmitter) Emit(eventType EventType, payload any) error {
	event := Event{
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		SessionID: e.sessionID,
		AgentID:   e.agentID,
		Type:      eventType,
		Payload:   payload,
	}
	if e.logger != nil {
		if err := e.logger.Append(event); err != nil {
			return fmt.Errorf("append event log: %w", err)
		}
	}
	if e.onEmit != nil {
		if err := e.onEmit(event); err != nil {
			return err
		}
	}
	e.events = append(e.events, event)
	return nil
}

func (e *eventEmitter) Events() []Event {
	out := make([]Event, len(e.events))
	copy(out, e.events)
	return out
}

func extractText(blocks []ai.ContentBlock) string {
	text := ""
	for _, block := range blocks {
		if block.Type == ai.ContentTypeText {
			text += block.Text
		}
	}
	return text
}
