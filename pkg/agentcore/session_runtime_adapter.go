package agentcore

import (
	"context"
	"sync"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type SessionRuntimeAdapter struct {
	agent *Agent

	mu       sync.Mutex
	sessions map[sessionrt.SessionID]*Session
}

var sessionRuntimeTurnTimeout = 20 * time.Second

func NewSessionRuntimeAdapter(agent *Agent) *SessionRuntimeAdapter {
	return &SessionRuntimeAdapter{
		agent:    agent,
		sessions: make(map[sessionrt.SessionID]*Session),
	}
}

func (a *SessionRuntimeAdapter) Step(ctx context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	userMsg, ok := latestUserMessage(input.History)
	if !ok {
		return sessionrt.AgentOutput{}, nil
	}

	a.mu.Lock()
	sessionData, exists := a.sessions[input.SessionID]
	if !exists {
		sessionData = a.agent.NewSession()
		sessionData.ID = string(input.SessionID)
		a.sessions[input.SessionID] = sessionData
	}
	a.mu.Unlock()

	turnCtx, cancel := withTurnTimeout(ctx)
	defer cancel()

	result, err := a.agent.RunTurn(turnCtx, sessionData, TurnInput{UserMessage: userMsg.Content})
	if err != nil {
		return sessionrt.AgentOutput{}, err
	}

	out := make([]sessionrt.Event, 0, len(result.Events))
	for _, event := range result.Events {
		switch event.Type {
		case EventTypeAgentMsg:
			text, _ := stringPayloadField(event.Payload, "text")
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventMessage,
				Payload: sessionrt.Message{Role: sessionrt.RoleAgent, Content: text},
			})
		case EventTypeToolCall:
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventToolCall,
				Payload: clonePayloadMap(event.Payload),
			})
		case EventTypeToolResult:
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventToolResult,
				Payload: clonePayloadMap(event.Payload),
			})
		case EventTypeError:
			message, _ := stringPayloadField(event.Payload, "message")
			out = append(out, sessionrt.Event{
				Type:    sessionrt.EventError,
				Payload: sessionrt.ErrorPayload{Message: message},
			})
		}
	}

	return sessionrt.AgentOutput{Events: out}, nil
}

func withTurnTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionRuntimeTurnTimeout <= 0 {
		return ctx, func() {}
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, sessionRuntimeTurnTimeout)
}

func latestUserMessage(events []sessionrt.Event) (sessionrt.Message, bool) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessionrt.EventMessage {
			continue
		}
		switch payload := event.Payload.(type) {
		case sessionrt.Message:
			if payload.Role == sessionrt.RoleUser {
				return payload, true
			}
		case map[string]any:
			roleRaw, ok := payload["role"].(string)
			if !ok || roleRaw != string(sessionrt.RoleUser) {
				continue
			}
			content, ok := payload["content"].(string)
			if !ok {
				continue
			}
			return sessionrt.Message{Role: sessionrt.RoleUser, Content: content}, true
		}
	}
	return sessionrt.Message{}, false
}

func stringPayloadField(payload any, key string) (string, bool) {
	value, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	text, ok := value[key].(string)
	if !ok {
		return "", false
	}
	return text, true
}

func clonePayloadMap(payload any) map[string]any {
	src, ok := payload.(map[string]any)
	if !ok || src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
