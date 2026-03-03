package session

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func normalizeDisplayName(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func defaultSessionDisplayName(createdAt time.Time, participants map[ActorID]Participant) string {
	labels := participantDisplayLabels(participants)
	base := "Session"
	switch len(labels) {
	case 0:
	case 1:
		base = fmt.Sprintf("Session with %s", labels[0])
	case 2:
		base = fmt.Sprintf("Session with %s and %s", labels[0], labels[1])
	default:
		base = fmt.Sprintf("Session with %s and %d others", labels[0], len(labels)-1)
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	return fmt.Sprintf("%s (%s UTC)", base, createdAt.UTC().Format("2006-01-02 15:04"))
}

func participantDisplayLabels(participants map[ActorID]Participant) []string {
	if len(participants) == 0 {
		return nil
	}

	keys := make([]string, 0, len(participants))
	for id := range participants {
		keys = append(keys, string(id))
	}
	sort.Strings(keys)

	labels := make([]string, 0, len(keys))
	for _, rawID := range keys {
		participant := participants[ActorID(rawID)]
		label := normalizeDisplayName(participant.Metadata["name"])
		if label == "" {
			label = humanizeActorID(participant.ID)
		}
		if label == "" {
			continue
		}
		labels = append(labels, label)
	}
	return labels
}

func humanizeActorID(actorID ActorID) string {
	raw := normalizeDisplayName(string(actorID))
	if raw == "" {
		return ""
	}
	if idx := strings.Index(raw, ":"); idx >= 0 && idx < len(raw)-1 {
		raw = raw[idx+1:]
	}
	raw = strings.NewReplacer("_", " ", "-", " ").Replace(raw)
	return normalizeDisplayName(raw)
}

func displayNameFromSessionCreatedEvent(event Event) (string, bool) {
	if event.Type != EventControl {
		return "", false
	}
	control, ok := controlFromPayload(event.Payload)
	if !ok || strings.TrimSpace(control.Action) != ControlActionSessionCreated {
		return "", false
	}
	return displayNameFromControlMetadata(control)
}

func displayNameFromControlMetadata(control ControlPayload) (string, bool) {
	if control.Metadata == nil {
		return "", false
	}
	raw, ok := control.Metadata["display_name"]
	if !ok || raw == nil {
		return "", false
	}
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	name := normalizeDisplayName(text)
	if name == "" {
		return "", false
	}
	return name, true
}

func DisplayNameFromEvents(events []Event) string {
	for _, event := range events {
		if name, ok := displayNameFromSessionCreatedEvent(event); ok {
			return name
		}
	}
	return ""
}
