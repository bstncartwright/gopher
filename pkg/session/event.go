package session

import (
	"fmt"
	"strings"
)

const (
	ControlActionSessionCreated   = "session.created"
	ControlActionSessionCancelled = "session.cancelled"
	ControlActionSessionFailed    = "session.failed"
	ControlActionSessionCompleted = "session.completed"
)

type ControlPayload struct {
	Action   string         `json:"action"`
	Reason   string         `json:"reason,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}

func normalizeEventPayload(eventType EventType, payload any) (any, error) {
	switch eventType {
	case EventMessage:
		msg, ok := messageFromPayload(payload)
		if !ok {
			return nil, fmt.Errorf("%w: message payload is invalid", ErrInvalidEvent)
		}
		if msg.Role != RoleAgent && msg.Role != RoleUser && msg.Role != RoleSystem {
			return nil, fmt.Errorf("%w: message role %q is invalid", ErrInvalidEvent, msg.Role)
		}
		return msg, nil
	case EventControl:
		ctrl, ok := controlFromPayload(payload)
		if !ok {
			return nil, fmt.Errorf("%w: control payload is invalid", ErrInvalidEvent)
		}
		if strings.TrimSpace(ctrl.Action) == "" {
			return nil, fmt.Errorf("%w: control action is required", ErrInvalidEvent)
		}
		return ctrl, nil
	case EventError:
		errPayload, ok := errorFromPayload(payload)
		if !ok {
			return nil, fmt.Errorf("%w: error payload is invalid", ErrInvalidEvent)
		}
		if strings.TrimSpace(errPayload.Message) == "" {
			return nil, fmt.Errorf("%w: error message is required", ErrInvalidEvent)
		}
		return errPayload, nil
	default:
		return payload, nil
	}
}

func messageFromPayload(payload any) (Message, bool) {
	switch v := payload.(type) {
	case Message:
		return v, true
	case *Message:
		if v == nil {
			return Message{}, false
		}
		return *v, true
	case map[string]any:
		roleAny, ok := v["role"]
		if !ok {
			return Message{}, false
		}
		contentAny, ok := v["content"]
		if !ok {
			return Message{}, false
		}
		role, ok := roleFromAny(roleAny)
		if !ok {
			return Message{}, false
		}
		content, ok := contentAny.(string)
		if !ok {
			return Message{}, false
		}
		return Message{Role: role, Content: content}, true
	default:
		return Message{}, false
	}
}

func roleFromAny(value any) (Role, bool) {
	raw, ok := value.(string)
	if !ok {
		return "", false
	}
	role := Role(strings.TrimSpace(raw))
	switch role {
	case RoleAgent, RoleUser, RoleSystem:
		return role, true
	default:
		return "", false
	}
}

func controlFromPayload(payload any) (ControlPayload, bool) {
	switch v := payload.(type) {
	case ControlPayload:
		return v, true
	case *ControlPayload:
		if v == nil {
			return ControlPayload{}, false
		}
		return *v, true
	case map[string]any:
		actionAny, ok := v["action"]
		if !ok {
			return ControlPayload{}, false
		}
		action, ok := actionAny.(string)
		if !ok {
			return ControlPayload{}, false
		}

		out := ControlPayload{Action: action}
		if reasonAny, ok := v["reason"]; ok {
			if reasonAny == nil {
				out.Reason = ""
			} else {
				reason, ok := reasonAny.(string)
				if !ok {
					return ControlPayload{}, false
				}
				out.Reason = reason
			}
		}
		if metaAny, ok := v["metadata"]; ok && metaAny != nil {
			meta, ok := mapAny(metaAny)
			if !ok {
				return ControlPayload{}, false
			}
			out.Metadata = meta
		}
		return out, true
	default:
		return ControlPayload{}, false
	}
}

func errorFromPayload(payload any) (ErrorPayload, bool) {
	switch v := payload.(type) {
	case ErrorPayload:
		return v, true
	case *ErrorPayload:
		if v == nil {
			return ErrorPayload{}, false
		}
		return *v, true
	case map[string]any:
		msgAny, ok := v["message"]
		if !ok {
			return ErrorPayload{}, false
		}
		msg, ok := msgAny.(string)
		if !ok {
			return ErrorPayload{}, false
		}
		return ErrorPayload{Message: msg}, true
	default:
		return ErrorPayload{}, false
	}
}

func mapAny(value any) (map[string]any, bool) {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out, true
	case map[string]string:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out, true
	default:
		return nil, false
	}
}
