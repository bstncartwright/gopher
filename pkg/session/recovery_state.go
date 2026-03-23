package session

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const interruptedResumeReason = "runtime_recovered"

const recoveryPromptHeader = "[resume after interruption]"

func CloneSessionRecord(record SessionRecord) SessionRecord {
	out := record
	out.ResumeActorIDs = cloneActorIDs(record.ResumeActorIDs)
	out.ResumeRecordedAt = cloneTimePtr(record.ResumeRecordedAt)
	out.ResumeEnqueuedAt = cloneTimePtr(record.ResumeEnqueuedAt)
	return out
}

func cloneActorIDs(actorIDs []ActorID) []ActorID {
	if len(actorIDs) == 0 {
		return nil
	}
	out := make([]ActorID, len(actorIDs))
	copy(out, actorIDs)
	return out
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil || value.IsZero() {
		return nil
	}
	ts := value.UTC()
	return &ts
}

func normalizeResumeActorIDs(actorIDs []ActorID) []ActorID {
	if len(actorIDs) == 0 {
		return nil
	}
	seen := map[ActorID]struct{}{}
	out := make([]ActorID, 0, len(actorIDs))
	for _, actorID := range actorIDs {
		normalized := ActorID(strings.TrimSpace(string(actorID)))
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	slices.Sort(out)
	return out
}

func sortedAgentParticipants(session *Session) []ActorID {
	if session == nil {
		return nil
	}
	out := make([]ActorID, 0, len(session.Participants))
	for actorID, participant := range session.Participants {
		if participant.Type != ActorAgent {
			continue
		}
		out = append(out, ActorID(strings.TrimSpace(string(actorID))))
	}
	out = normalizeResumeActorIDs(out)
	return out
}

func buildRecoveryPrompt(triggerSeq uint64, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = interruptedResumeReason
	}
	lines := []string{
		recoveryPromptHeader,
		fmt.Sprintf("resume_reason: %s", reason),
		fmt.Sprintf("resume_trigger_seq: %d", triggerSeq),
		"",
		"Your previous turn was interrupted by runtime sleep or restart.",
		"Use the persisted session history as the source of truth.",
		"Continue unfinished work from the last confirmed step.",
		"Avoid repeating side effects unless you verify they did not complete.",
	}
	return strings.Join(lines, "\n")
}

func isRecoveryPromptEvent(event Event, actorID ActorID, triggerSeq uint64, reason string) bool {
	if event.Type != EventMessage || event.From != SystemActorID {
		return false
	}
	msg, ok := messageFromPayload(event.Payload)
	if !ok {
		return false
	}
	if msg.Role != RoleAgent {
		return false
	}
	if ActorID(strings.TrimSpace(string(msg.TargetActorID))) != ActorID(strings.TrimSpace(string(actorID))) {
		return false
	}
	return strings.TrimSpace(msg.Content) == strings.TrimSpace(buildRecoveryPrompt(triggerSeq, reason))
}
