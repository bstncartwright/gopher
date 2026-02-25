package main

import (
	"sort"
	"strings"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func gatewayMessageTargetSelector(defaultActorID sessionrt.ActorID) sessionrt.AgentSelector {
	defaultActorID = sessionrt.ActorID(strings.TrimSpace(string(defaultActorID)))
	return func(session *sessionrt.Session, trigger sessionrt.Event) ([]sessionrt.ActorID, bool) {
		if session == nil {
			return nil, false
		}
		message, ok := selectorMessageFromEvent(trigger)
		if !ok {
			return nil, false
		}
		target := sessionrt.ActorID(strings.TrimSpace(string(message.TargetActorID)))
		if target != "" {
			if participant, exists := session.Participants[target]; exists && participant.Type == sessionrt.ActorAgent {
				return []sessionrt.ActorID{target}, true
			}
		}
		triggerFrom := sessionrt.ActorID(strings.TrimSpace(string(trigger.From)))
		if participant, exists := session.Participants[triggerFrom]; exists && participant.Type == sessionrt.ActorAgent {
			others := make([]sessionrt.ActorID, 0, len(session.Participants))
			for actorID, p := range session.Participants {
				if p.Type != sessionrt.ActorAgent {
					continue
				}
				if actorID == triggerFrom {
					continue
				}
				others = append(others, actorID)
			}
			sort.Slice(others, func(i, j int) bool { return string(others[i]) < string(others[j]) })
			if len(others) > 0 {
				return others, true
			}
		}
		if defaultActorID != "" {
			if participant, exists := session.Participants[defaultActorID]; exists && participant.Type == sessionrt.ActorAgent {
				return []sessionrt.ActorID{defaultActorID}, true
			}
		}
		return sessionrt.DefaultAgentSelector(session, trigger)
	}
}

func selectorMessageFromEvent(event sessionrt.Event) (sessionrt.Message, bool) {
	if event.Type != sessionrt.EventMessage {
		return sessionrt.Message{}, false
	}
	switch payload := event.Payload.(type) {
	case sessionrt.Message:
		return payload, true
	case map[string]any:
		roleRaw, roleOK := payload["role"].(string)
		contentRaw, contentOK := payload["content"].(string)
		if !roleOK || !contentOK {
			return sessionrt.Message{}, false
		}
		out := sessionrt.Message{
			Role:    sessionrt.Role(strings.TrimSpace(roleRaw)),
			Content: contentRaw,
		}
		if raw, exists := payload["target_actor_id"]; exists && raw != nil {
			value, ok := raw.(string)
			if !ok {
				return sessionrt.Message{}, false
			}
			out.TargetActorID = sessionrt.ActorID(strings.TrimSpace(value))
		}
		return out, true
	default:
		return sessionrt.Message{}, false
	}
}
