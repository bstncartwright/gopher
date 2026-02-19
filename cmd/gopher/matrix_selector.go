package main

import (
	"context"
	"sort"
	"strings"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type matrixUntaggedResponderSelectionInput struct {
	Message              string
	CandidateActors      []sessionrt.ActorID
	CandidateUserByActor map[sessionrt.ActorID]string
}

type matrixUntaggedResponderRouter interface {
	SelectResponders(ctx context.Context, input matrixUntaggedResponderSelectionInput) ([]sessionrt.ActorID, error)
}

func matrixMentionAgentSelector(identities agentMatrixIdentitySet, fallback matrixUntaggedResponderRouter) sessionrt.AgentSelector {
	return func(session *sessionrt.Session, trigger sessionrt.Event) ([]sessionrt.ActorID, bool) {
		if session == nil {
			return nil, false
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
		sort.Slice(agentParticipants, func(i, j int) bool {
			return string(agentParticipants[i]) < string(agentParticipants[j])
		})
		if len(agentParticipants) == 0 {
			return nil, false
		}
		if len(agentParticipants) == 1 {
			return []sessionrt.ActorID{agentParticipants[0]}, true
		}

		message, ok := sessionMessageFromEvent(trigger)
		if !ok || message.Role != sessionrt.RoleUser {
			return nil, false
		}
		if actorID, ok := selectedActorFromMessageTarget(message, participantSet); ok {
			return []sessionrt.ActorID{actorID}, true
		}
		mentions := mentionedParticipantActors(message.Content, identities.ActorByUserID, participantSet)
		if len(mentions) > 0 {
			return mentions, true
		}
		if fallback == nil {
			return nil, false
		}
		selected, err := fallback.SelectResponders(context.Background(), matrixUntaggedResponderSelectionInput{
			Message:              message.Content,
			CandidateActors:      agentParticipants,
			CandidateUserByActor: identities.UserByActorID,
		})
		if err != nil {
			return nil, false
		}
		selected = filterParticipantActors(selected, participantSet)
		if len(selected) == 0 {
			return nil, false
		}
		return selected, true
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
		targetActorID := sessionrt.ActorID("")
		if targetRaw, exists := payload["target_actor_id"]; exists && targetRaw != nil {
			targetText, ok := targetRaw.(string)
			if !ok {
				return sessionrt.Message{}, false
			}
			targetActorID = sessionrt.ActorID(strings.TrimSpace(targetText))
		}
		return sessionrt.Message{
			Role:          sessionrt.Role(strings.TrimSpace(roleRaw)),
			Content:       contentRaw,
			TargetActorID: targetActorID,
		}, true
	default:
		return sessionrt.Message{}, false
	}
}

func selectedActorFromMessageTarget(message sessionrt.Message, participantSet map[sessionrt.ActorID]struct{}) (sessionrt.ActorID, bool) {
	target := sessionrt.ActorID(strings.TrimSpace(string(message.TargetActorID)))
	if strings.TrimSpace(string(target)) == "" {
		return "", false
	}
	if _, ok := participantSet[target]; !ok {
		return "", false
	}
	return target, true
}

func filterParticipantActors(actors []sessionrt.ActorID, participantSet map[sessionrt.ActorID]struct{}) []sessionrt.ActorID {
	if len(actors) == 0 {
		return nil
	}
	out := make([]sessionrt.ActorID, 0, len(actors))
	seen := make(map[sessionrt.ActorID]struct{}, len(actors))
	for _, actor := range actors {
		actor = sessionrt.ActorID(strings.TrimSpace(string(actor)))
		if strings.TrimSpace(string(actor)) == "" {
			continue
		}
		if _, ok := participantSet[actor]; !ok {
			continue
		}
		if _, ok := seen[actor]; ok {
			continue
		}
		seen[actor] = struct{}{}
		out = append(out, actor)
	}
	return out
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
