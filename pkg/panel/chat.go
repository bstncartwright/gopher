package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const (
	localChatHumanActorID = sessionrt.ActorID("local:web")
	localChatChannelKey   = "channel"
	localChatChannelValue = "web_chat"
)

type chatPageData struct {
	ChatRoot       string
	AdminRoot      string
	HasChat        bool
	InitialSession string
}

type chatCreateSessionRequest struct {
	Message string `json:"message"`
	Title   string `json:"title"`
}

type chatSendMessageRequest struct {
	Message string `json:"message"`
}

type chatSessionsResponse struct {
	GeneratedAt string               `json:"generated_at"`
	Sessions    []chatSessionSummary `json:"sessions"`
}

type chatSessionResponse struct {
	Session  chatSessionSummary `json:"session"`
	Messages []chatMessageRow   `json:"messages"`
}

type chatSessionSummary struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at"`
	LastSeq   uint64 `json:"last_seq"`
	Working   bool   `json:"working"`
	Preview   string `json:"preview,omitempty"`
}

type chatMessageRow struct {
	Seq       uint64 `json:"seq"`
	Role      string `json:"role"`
	ActorID   string `json:"actor_id"`
	Content   string `json:"content"`
	Timestamp string `json:"timestamp"`
}

func (s *Server) handleChatPage(w http.ResponseWriter, r *http.Request) {
	s.renderTemplate(w, "chat_page.html", chatPageData{
		ChatRoot:       chatRoot,
		AdminRoot:      adminRoot,
		HasChat:        s.chatEnabled(),
		InitialSession: strings.TrimSpace(r.URL.Query().Get("session")),
	})
}

func (s *Server) handleChatJS(w http.ResponseWriter, _ *http.Request) {
	blob, err := fs.ReadFile(s.assets, "chat.js")
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("content-type", "application/javascript; charset=utf-8")
	_, _ = w.Write(blob)
}

func (s *Server) handleChatSessionsAPI(w http.ResponseWriter, r *http.Request) {
	if !s.chatEnabled() {
		http.Error(w, "local chat is unavailable", http.StatusServiceUnavailable)
		return
	}
	summaries, err := s.buildChatSessionSummaries(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("list chat sessions: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, chatSessionsResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Sessions:    summaries,
	})
}

func (s *Server) handleChatCreateSessionAPI(w http.ResponseWriter, r *http.Request) {
	if !s.chatEnabled() {
		http.Error(w, "local chat is unavailable", http.StatusServiceUnavailable)
		return
	}
	var req chatCreateSessionRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "invalid chat session payload", http.StatusBadRequest)
			return
		}
	}
	title := strings.TrimSpace(req.Title)
	message := strings.TrimSpace(req.Message)
	if title == "" {
		title = suggestLocalChatTitle(message)
	}
	created, err := s.chatManager.CreateSession(r.Context(), sessionrt.CreateSessionOptions{
		DisplayName: title,
		Participants: []sessionrt.Participant{
			{ID: s.chatAgentID, Type: sessionrt.ActorAgent},
			{
				ID:   localChatHumanActorID,
				Type: sessionrt.ActorHuman,
				Metadata: map[string]string{
					"name":                 "Local Operator",
					localChatChannelKey:    localChatChannelValue,
					"transport":            "web",
					"session_visibility":   "local",
					"session_origin":       "panel_chat",
					"conversation_surface": "chat",
				},
			},
		},
	})
	if err != nil {
		http.Error(w, fmt.Sprintf("create chat session: %v", err), http.StatusInternalServerError)
		return
	}
	if message != "" {
		if err := s.sendLocalChatMessage(r.Context(), created.ID, message); err != nil {
			http.Error(w, fmt.Sprintf("start chat session: %v", err), http.StatusInternalServerError)
			return
		}
	}
	resp, err := s.buildChatSessionResponse(r.Context(), created.ID)
	if err != nil {
		http.Error(w, fmt.Sprintf("load chat session: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleChatSessionAPI(w http.ResponseWriter, r *http.Request) {
	if !s.chatEnabled() {
		http.Error(w, "local chat is unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := sessionrt.SessionID(strings.TrimSpace(r.PathValue("sessionID")))
	resp, err := s.buildChatSessionResponse(r.Context(), sessionID)
	if err != nil {
		status := http.StatusInternalServerError
		if errorsIsNotFound(err) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) handleChatSendMessageAPI(w http.ResponseWriter, r *http.Request) {
	if !s.chatEnabled() {
		http.Error(w, "local chat is unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := sessionrt.SessionID(strings.TrimSpace(r.PathValue("sessionID")))
	if sessionID == "" {
		http.Error(w, "sessionID is required", http.StatusBadRequest)
		return
	}
	if _, err := s.buildChatSessionResponse(r.Context(), sessionID); err != nil {
		status := http.StatusInternalServerError
		if errorsIsNotFound(err) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	var req chatSendMessageRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid chat message payload", http.StatusBadRequest)
			return
		}
	}
	if err := s.sendLocalChatMessage(r.Context(), sessionID, strings.TrimSpace(req.Message)); err != nil {
		http.Error(w, fmt.Sprintf("send chat message: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleChatSessionStream(w http.ResponseWriter, r *http.Request) {
	if !s.chatEnabled() {
		http.Error(w, "local chat is unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := sessionrt.SessionID(strings.TrimSpace(r.PathValue("sessionID")))
	if _, err := s.buildChatSessionResponse(r.Context(), sessionID); err != nil {
		status := http.StatusInternalServerError
		if errorsIsNotFound(err) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	s.handleSessionStream(w, r)
}

func (s *Server) chatEnabled() bool {
	return s != nil && s.store != nil && s.chatManager != nil && strings.TrimSpace(string(s.chatAgentID)) != ""
}

func (s *Server) sendLocalChatMessage(ctx context.Context, sessionID sessionrt.SessionID, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Errorf("message is required")
	}
	return s.chatManager.SendEvent(ctx, sessionrt.Event{
		SessionID: sessionID,
		From:      localChatHumanActorID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:          sessionrt.RoleUser,
			Content:       message,
			TargetActorID: s.chatAgentID,
		},
	})
}

func (s *Server) buildChatSessionSummaries(ctx context.Context) ([]chatSessionSummary, error) {
	records, err := s.store.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]chatSessionSummary, 0, len(records))
	for _, record := range records {
		events, err := s.store.List(ctx, record.SessionID)
		if err != nil || !isLocalChatSession(events) {
			continue
		}
		out = append(out, buildChatSessionSummary(record, events))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, nil
}

func (s *Server) buildChatSessionResponse(ctx context.Context, sessionID sessionrt.SessionID) (chatSessionResponse, error) {
	if strings.TrimSpace(string(sessionID)) == "" {
		return chatSessionResponse{}, fmt.Errorf("session not found")
	}
	records, err := s.store.ListSessions(ctx)
	if err != nil {
		return chatSessionResponse{}, err
	}
	var record sessionrt.SessionRecord
	found := false
	for _, candidate := range records {
		if candidate.SessionID == sessionID {
			record = candidate
			found = true
			break
		}
	}
	if !found {
		return chatSessionResponse{}, fmt.Errorf("session not found")
	}
	events, err := s.store.List(ctx, sessionID)
	if err != nil {
		return chatSessionResponse{}, err
	}
	if !isLocalChatSession(events) {
		return chatSessionResponse{}, fmt.Errorf("session not found")
	}
	return chatSessionResponse{
		Session:  buildChatSessionSummary(record, events),
		Messages: buildChatMessages(events),
	}, nil
}

func buildChatSessionSummary(record sessionrt.SessionRecord, events []sessionrt.Event) chatSessionSummary {
	title := strings.TrimSpace(record.DisplayName)
	if title == "" {
		title = strings.TrimSpace(string(record.SessionID))
	}
	return chatSessionSummary{
		SessionID: strings.TrimSpace(string(record.SessionID)),
		Title:     title,
		Status:    sessionStatusText(record.Status),
		UpdatedAt: formatTime(record.UpdatedAt),
		LastSeq:   record.LastSeq,
		Working:   record.InFlight,
		Preview:   latestChatPreview(events),
	}
}

func buildChatMessages(events []sessionrt.Event) []chatMessageRow {
	rows := make([]chatMessageRow, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case sessionrt.EventMessage:
			msg, ok := decodeChatMessagePayload(event.Payload)
			if !ok || strings.TrimSpace(msg.Content) == "" {
				continue
			}
			rows = append(rows, chatMessageRow{
				Seq:       event.Seq,
				Role:      string(msg.Role),
				ActorID:   strings.TrimSpace(string(event.From)),
				Content:   strings.TrimSpace(msg.Content),
				Timestamp: formatTime(event.Timestamp),
			})
		case sessionrt.EventError:
			text := decodeChatErrorPayload(event.Payload)
			if text == "" {
				continue
			}
			rows = append(rows, chatMessageRow{
				Seq:       event.Seq,
				Role:      "error",
				ActorID:   strings.TrimSpace(string(event.From)),
				Content:   text,
				Timestamp: formatTime(event.Timestamp),
			})
		case sessionrt.EventControl:
			text := localChatControlMessage(event.Payload)
			if text == "" {
				continue
			}
			rows = append(rows, chatMessageRow{
				Seq:       event.Seq,
				Role:      "system",
				ActorID:   strings.TrimSpace(string(event.From)),
				Content:   text,
				Timestamp: formatTime(event.Timestamp),
			})
		}
	}
	return rows
}

func latestChatPreview(events []sessionrt.Event) string {
	rows := buildChatMessages(events)
	for i := len(rows) - 1; i >= 0; i-- {
		text := strings.TrimSpace(rows[i].Content)
		if text != "" {
			return clipPanelText(text, 120)
		}
	}
	return ""
}

func isLocalChatSession(events []sessionrt.Event) bool {
	for _, participant := range localChatParticipants(events) {
		if participant.ID != localChatHumanActorID || participant.Type != sessionrt.ActorHuman {
			continue
		}
		if participant.Metadata[localChatChannelKey] == localChatChannelValue {
			return true
		}
	}
	for _, event := range events {
		if event.Type == sessionrt.EventMessage && strings.TrimSpace(string(event.From)) == string(localChatHumanActorID) {
			return true
		}
	}
	return false
}

func localChatParticipants(events []sessionrt.Event) []sessionrt.Participant {
	for _, event := range events {
		if event.Type != sessionrt.EventControl {
			continue
		}
		ctrl, ok := decodeChatControlPayload(event.Payload)
		if !ok || strings.TrimSpace(ctrl.Action) != sessionrt.ControlActionSessionCreated {
			continue
		}
		participantsAny, ok := ctrl.Metadata["participants"]
		if !ok || participantsAny == nil {
			return nil
		}
		switch typed := participantsAny.(type) {
		case []sessionrt.Participant:
			out := make([]sessionrt.Participant, len(typed))
			copy(out, typed)
			return out
		default:
			blob, err := json.Marshal(typed)
			if err != nil {
				return nil
			}
			var out []sessionrt.Participant
			if err := json.Unmarshal(blob, &out); err != nil {
				return nil
			}
			return out
		}
	}
	return nil
}

func suggestLocalChatTitle(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return "Local Chat"
	}
	return clipPanelText(message, 48)
}

func decodeChatMessagePayload(payload any) (sessionrt.Message, bool) {
	switch typed := payload.(type) {
	case sessionrt.Message:
		return typed, true
	case *sessionrt.Message:
		if typed == nil {
			return sessionrt.Message{}, false
		}
		return *typed, true
	default:
		blob, err := json.Marshal(payload)
		if err != nil {
			return sessionrt.Message{}, false
		}
		var out sessionrt.Message
		if err := json.Unmarshal(blob, &out); err != nil {
			return sessionrt.Message{}, false
		}
		return out, true
	}
}

func decodeChatControlPayload(payload any) (sessionrt.ControlPayload, bool) {
	switch typed := payload.(type) {
	case sessionrt.ControlPayload:
		return typed, true
	case *sessionrt.ControlPayload:
		if typed == nil {
			return sessionrt.ControlPayload{}, false
		}
		return *typed, true
	default:
		blob, err := json.Marshal(payload)
		if err != nil {
			return sessionrt.ControlPayload{}, false
		}
		var out sessionrt.ControlPayload
		if err := json.Unmarshal(blob, &out); err != nil {
			return sessionrt.ControlPayload{}, false
		}
		return out, true
	}
}

func decodeChatErrorPayload(payload any) string {
	switch typed := payload.(type) {
	case sessionrt.ErrorPayload:
		return strings.TrimSpace(typed.Message)
	case *sessionrt.ErrorPayload:
		if typed == nil {
			return ""
		}
		return strings.TrimSpace(typed.Message)
	default:
		blob, err := json.Marshal(payload)
		if err != nil {
			return strings.TrimSpace(prettyPayload(payload))
		}
		var out sessionrt.ErrorPayload
		if err := json.Unmarshal(blob, &out); err != nil {
			return strings.TrimSpace(prettyPayload(payload))
		}
		return strings.TrimSpace(out.Message)
	}
}

func localChatControlMessage(payload any) string {
	ctrl, ok := decodeChatControlPayload(payload)
	if !ok {
		return ""
	}
	switch strings.TrimSpace(ctrl.Action) {
	case sessionrt.ControlActionSessionCancelled:
		return "Session paused."
	case sessionrt.ControlActionSessionCompleted:
		return "Session completed."
	case sessionrt.ControlActionSessionFailed:
		if reason := strings.TrimSpace(ctrl.Reason); reason != "" {
			return "Session failed: " + reason
		}
		return "Session failed."
	default:
		return ""
	}
}
