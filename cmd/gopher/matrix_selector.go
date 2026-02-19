package main

import (
	"sort"
	"strings"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

func matrixMentionAgentSelector(identities agentMatrixIdentitySet) sessionrt.AgentSelector {
	return func(session *sessionrt.Session, trigger sessionrt.Event) (sessionrt.ActorID, bool) {
		if session == nil {
			return "", false
		}
		agentParticipants := make([]sessionrt.ActorID, 0, len(session.Participants))
		participantSet := make(map[sessionrt.ActorID]struct{}, len(session.Participants))
		for actorID, participant := range session.Participants {
			if participant.Type != sessionrt.ActorAgent {
				continue
			}
			if strings.TrimSpace(string(actorID)) == "" {
				continue
			}
			agentParticipants = append(agentParticipants, actorID)
			participantSet[actorID] = struct{}{}
		}
		if len(agentParticipants) == 0 {
			return "", false
		}
		if len(agentParticipants) == 1 {
			return agentParticipants[0], true
		}

		message, ok := sessionMessageFromEvent(trigger)
		if !ok || message.Role != sessionrt.RoleUser {
			return "", false
		}
		mentions := mentionedParticipantActors(message.Content, identities.ActorByUserID, participantSet)
		if len(mentions) != 1 {
			return "", false
		}
		return mentions[0], true
	}
}

func sessionMessageFromEvent(event sessionrt.Event) (sessionrt.Message, bool) {
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
		return sessionrt.Message{
			Role:    sessionrt.Role(strings.TrimSpace(roleRaw)),
			Content: contentRaw,
		}, true
	default:
		return sessionrt.Message{}, false
	}
}

func mentionedParticipantActors(text string, actorByUserID map[string]sessionrt.ActorID, participantSet map[sessionrt.ActorID]struct{}) []sessionrt.ActorID {
	mentions := parseMatrixMentionTokens(text)
	seen := map[sessionrt.ActorID]struct{}{}
	for _, mention := range mentions {
		actorID, ok := actorByUserID[strings.ToLower(strings.TrimSpace(mention))]
		if !ok {
			continue
		}
		if _, allowed := participantSet[actorID]; !allowed {
			continue
		}
		seen[actorID] = struct{}{}
	}
	out := make([]sessionrt.ActorID, 0, len(seen))
	for actorID := range seen {
		out = append(out, actorID)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i]) < string(out[j])
	})
	return out
}

func parseMatrixMentionTokens(text string) []string {
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		token := strings.TrimSpace(field)
		token = strings.Trim(token, ".,!?;()[]{}<>\"'`")
		if !strings.HasPrefix(token, "@") {
			continue
		}
		if !strings.Contains(token, ":") {
			continue
		}
		out = append(out, strings.ToLower(strings.TrimSpace(token)))
	}
	return out
}
