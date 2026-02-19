package session

import (
	"context"
	"fmt"
)

func Replay(events []Event) (*Session, error) {
	if len(events) == 0 {
		return nil, fmt.Errorf("%w: replay history is empty", ErrInvalidSession)
	}

	sessionID := events[0].SessionID
	if sessionID == "" {
		return nil, fmt.Errorf("%w: replay history missing session ID", ErrInvalidSession)
	}

	session := &Session{
		ID:           sessionID,
		Participants: map[ActorID]Participant{},
		Status:       SessionActive,
		CreatedAt:    events[0].Timestamp,
	}

	expectedSeq := uint64(1)
	for _, event := range events {
		if event.SessionID != sessionID {
			return nil, fmt.Errorf("%w: mixed session IDs in replay history", ErrInvalidSession)
		}
		if event.Seq != expectedSeq {
			return nil, fmt.Errorf("%w: sequence gap at expected %d got %d", ErrInvalidSession, expectedSeq, event.Seq)
		}
		expectedSeq++

		switch event.Type {
		case EventControl:
			control, ok := controlFromPayload(event.Payload)
			if !ok {
				continue
			}
			switch control.Action {
			case ControlActionSessionCreated:
				session.CreatedAt = event.Timestamp
				if participantsAny, ok := control.Metadata["participants"]; ok {
					participants, ok := participantsFromAny(participantsAny)
					if ok {
						session.Participants = make(map[ActorID]Participant, len(participants))
						for _, participant := range participants {
							session.Participants[participant.ID] = cloneParticipant(participant)
						}
					}
				}
				session.Status = SessionActive
			case ControlActionSessionCancelled:
				session.Status = SessionPaused
			case ControlActionSessionCompleted:
				session.Status = SessionCompleted
			case ControlActionSessionFailed:
				session.Status = SessionFailed
			}
		}
	}

	return session, nil
}

func ReplayFromStore(ctx context.Context, store EventStore, sessionID SessionID) (*Session, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: event store is required", ErrInvalidSession)
	}
	events, err := store.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	replayed, err := Replay(events)
	if err != nil {
		return nil, err
	}
	if replayed.ID != sessionID {
		return nil, fmt.Errorf("%w: replayed session ID mismatch", ErrInvalidSession)
	}
	return replayed, nil
}

func participantsFromAny(value any) ([]Participant, bool) {
	switch v := value.(type) {
	case []Participant:
		out := make([]Participant, 0, len(v))
		for _, participant := range v {
			out = append(out, cloneParticipant(participant))
		}
		return out, true
	case []any:
		out := make([]Participant, 0, len(v))
		for _, item := range v {
			participant, ok := participantFromAny(item)
			if !ok {
				return nil, false
			}
			out = append(out, participant)
		}
		return out, true
	default:
		return nil, false
	}
}

func participantFromAny(value any) (Participant, bool) {
	switch v := value.(type) {
	case Participant:
		return cloneParticipant(v), true
	case map[string]any:
		idAny, ok := v["id"]
		if !ok {
			return Participant{}, false
		}
		id, ok := idAny.(string)
		if !ok || id == "" {
			return Participant{}, false
		}

		typeAny, ok := v["type"]
		if !ok {
			return Participant{}, false
		}
		actorType, ok := actorTypeFromAny(typeAny)
		if !ok {
			return Participant{}, false
		}

		metadata := map[string]string{}
		if metaAny, ok := v["metadata"]; ok && metaAny != nil {
			switch meta := metaAny.(type) {
			case map[string]string:
				metadata = cloneStringMap(meta)
			case map[string]any:
				metadata = make(map[string]string, len(meta))
				for key, item := range meta {
					text, ok := item.(string)
					if !ok {
						return Participant{}, false
					}
					metadata[key] = text
				}
			default:
				return Participant{}, false
			}
		}

		return Participant{
			ID:       ActorID(id),
			Type:     actorType,
			Metadata: metadata,
		}, true
	default:
		return Participant{}, false
	}
}

func actorTypeFromAny(value any) (ActorType, bool) {
	switch v := value.(type) {
	case ActorType:
		if !validActorType(v) {
			return 0, false
		}
		return v, true
	case int:
		t := ActorType(v)
		if !validActorType(t) {
			return 0, false
		}
		return t, true
	case float64:
		t := ActorType(int(v))
		if !validActorType(t) {
			return 0, false
		}
		return t, true
	default:
		return 0, false
	}
}
