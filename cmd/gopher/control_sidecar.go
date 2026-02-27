package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type controlActionApplier struct {
	manager sessionrt.SessionManager
	dataDir string
	logger  *log.Logger
}

type controlSessionWatcher struct {
	store interface {
		List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error)
		ListSessions(ctx context.Context) ([]sessionrt.SessionRecord, error)
	}
	dataDir string
	logger  *log.Logger
}

type controlAction struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

func newControlActionApplier(manager sessionrt.SessionManager, dataDir string, logger *log.Logger) *controlActionApplier {
	return &controlActionApplier{
		manager: manager,
		dataDir: filepath.Clean(strings.TrimSpace(dataDir)),
		logger:  logger,
	}
}

func (a *controlActionApplier) Start(ctx context.Context) {
	if a == nil || a.manager == nil || a.dataDir == "" {
		return
	}
	go a.loop(ctx)
}

func (a *controlActionApplier) loop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.processPending(ctx)
		}
	}
}

func (a *controlActionApplier) processPending(ctx context.Context) {
	pendingDir := filepath.Join(a.dataDir, "control", "actions", "pending")
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		if !os.IsNotExist(err) && a.logger != nil {
			a.logger.Printf("control action applier read pending dir err=%v", err)
		}
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	appliedKeys := a.loadAppliedKeys()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(pendingDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		action := controlAction{}
		if err := json.Unmarshal(raw, &action); err != nil {
			a.recordApplied(map[string]any{
				"ts":      time.Now().UTC().Format(time.RFC3339Nano),
				"ok":      false,
				"error":   fmt.Sprintf("decode action: %v", err),
				"file":    entry.Name(),
				"action":  "invalid",
				"raw_len": len(raw),
			})
			_ = os.Remove(path)
			continue
		}
		key := strings.TrimSpace(action.ID)
		if key == "" {
			sum := sha256.Sum256(raw)
			key = hex.EncodeToString(sum[:])
		}
		if _, exists := appliedKeys[key]; exists {
			_ = os.Remove(path)
			continue
		}
		result := map[string]any{
			"ts":          time.Now().UTC().Format(time.RFC3339Nano),
			"idempotency": key,
			"action":      strings.TrimSpace(action.Type),
			"session_id":  strings.TrimSpace(action.SessionID),
			"file":        entry.Name(),
			"ok":          true,
		}
		if err := a.applyAction(ctx, action, result); err != nil {
			result["ok"] = false
			result["error"] = err.Error()
		}
		a.recordApplied(result)
		appliedKeys[key] = struct{}{}
		_ = os.Remove(path)
	}
}

func (a *controlActionApplier) applyAction(ctx context.Context, action controlAction, result map[string]any) error {
	actionType := strings.TrimSpace(action.Type)
	sessionID := sessionrt.SessionID(strings.TrimSpace(action.SessionID))
	switch actionType {
	case "pause_session":
		if sessionID == "" {
			return fmt.Errorf("session_id is required")
		}
		if err := a.manager.CancelSession(ctx, sessionID); err != nil {
			return err
		}
		return nil
	case "resume_session":
		if sessionID == "" {
			return fmt.Errorf("session_id is required")
		}
		source, err := a.manager.GetSession(ctx, sessionID)
		if err != nil {
			return err
		}
		participants := make([]sessionrt.Participant, 0, len(source.Participants))
		for _, participant := range source.Participants {
			participants = append(participants, sessionrt.Participant{
				ID:       participant.ID,
				Type:     participant.Type,
				Metadata: participant.Metadata,
			})
		}
		created, err := a.manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
			Participants: participants,
		})
		if err != nil {
			return err
		}
		result["replacement_session_id"] = strings.TrimSpace(string(created.ID))
		return nil
	default:
		return fmt.Errorf("unsupported action type %q", actionType)
	}
}

func (a *controlActionApplier) loadAppliedKeys() map[string]struct{} {
	keys := map[string]struct{}{}
	path := filepath.Join(a.dataDir, "control", "actions", "applied.jsonl")
	blob, err := os.ReadFile(path)
	if err != nil {
		return keys
	}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		key, _ := record["idempotency"].(string)
		key = strings.TrimSpace(key)
		if key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys
}

func (a *controlActionApplier) recordApplied(record map[string]any) {
	path := filepath.Join(a.dataDir, "control", "actions", "applied.jsonl")
	appendJSONLRecord(path, record)
}

func newControlSessionWatcher(
	store interface {
		List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error)
		ListSessions(ctx context.Context) ([]sessionrt.SessionRecord, error)
	},
	dataDir string,
	logger *log.Logger,
) *controlSessionWatcher {
	return &controlSessionWatcher{
		store:   store,
		dataDir: filepath.Clean(strings.TrimSpace(dataDir)),
		logger:  logger,
	}
}

func (w *controlSessionWatcher) Start(ctx context.Context) {
	if w == nil || w.store == nil || w.dataDir == "" {
		return
	}
	go w.loop(ctx)
}

func (w *controlSessionWatcher) loop(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.rebuildIndex(ctx); err != nil && w.logger != nil {
				w.logger.Printf("control session watcher rebuild err=%v", err)
			}
		}
	}
}

func (w *controlSessionWatcher) rebuildIndex(ctx context.Context) error {
	records, err := w.store.ListSessions(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	sort.Slice(records, func(i, j int) bool {
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	delegations := readDelegationMap(filepath.Join(w.dataDir, "control", "delegations.jsonl"))

	sessions := make([]map[string]any, 0, len(records))
	summary := map[string]any{
		"active":    0,
		"paused":    0,
		"completed": 0,
		"failed":    0,
		"waiting":   0,
		"delegated": 0,
	}
	for _, record := range records {
		if controlSessionRecordIsStale(record, now) {
			continue
		}
		events, _ := w.store.List(ctx, record.SessionID)
		waiting, waitingText := detectWaitingOnHuman(events)
		statusText := strings.ToLower(strings.TrimSpace(controlSessionStatusText(record.Status)))
		if _, ok := summary[statusText]; ok {
			summary[statusText] = summary[statusText].(int) + 1
		}
		if waiting {
			summary["waiting"] = summary["waiting"].(int) + 1
		}
		delegation, delegated := delegations[strings.TrimSpace(string(record.SessionID))]
		if delegated {
			summary["delegated"] = summary["delegated"].(int) + 1
		}
		item := map[string]any{
			"session_id":        strings.TrimSpace(string(record.SessionID)),
			"status":            statusText,
			"updated_at":        record.UpdatedAt.UTC().Format(time.RFC3339Nano),
			"last_seq":          record.LastSeq,
			"waiting_on_human":  waiting,
			"waiting_reason":    waitingText,
			"is_delegated":      delegated,
			"delegation_parent": "",
			"target_agent_id":   "",
		}
		if delegated {
			item["delegation_parent"] = delegation["source_session_id"]
			item["target_agent_id"] = delegation["target_agent_id"]
		}
		sessions = append(sessions, item)
	}
	doc := map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"summary":      summary,
		"sessions":     sessions,
	}
	path := filepath.Join(w.dataDir, "control", "session_index.json")
	return writeJSONAtomically(path, doc)
}

func readDelegationMap(path string) map[string]map[string]any {
	out := map[string]map[string]any{}
	blob, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(blob), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		id, _ := record["delegation_id"].(string)
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		out[id] = record
	}
	return out
}

func detectWaitingOnHuman(events []sessionrt.Event) (bool, string) {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessionrt.EventMessage {
			continue
		}
		text := extractMessageText(event.Payload)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if strings.Contains(text, "[[WAITING_ON_HUMAN|") {
			return true, text
		}
	}
	return false, ""
}

func extractMessageText(payload any) string {
	switch v := payload.(type) {
	case sessionrt.Message:
		return v.Content
	case map[string]any:
		text, _ := v["content"].(string)
		return text
	default:
		return ""
	}
}

func controlSessionStatusText(status sessionrt.SessionStatus) string {
	switch status {
	case sessionrt.SessionActive:
		return "active"
	case sessionrt.SessionPaused:
		return "paused"
	case sessionrt.SessionCompleted:
		return "completed"
	case sessionrt.SessionFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func controlSessionRecordIsStale(record sessionrt.SessionRecord, now time.Time) bool {
	lastActivity := record.UpdatedAt
	if lastActivity.IsZero() {
		lastActivity = record.CreatedAt
	}
	return sessionrt.IsStaleByDailyReset(lastActivity, now, sessionrt.DefaultDailyResetPolicy())
}

func appendJSONLRecord(path string, record map[string]any) {
	blob, err := json.Marshal(record)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(blob, '\n'))
}

func writeJSONAtomically(path string, payload any) error {
	blob, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, blob, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}
