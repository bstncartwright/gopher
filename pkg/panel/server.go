package panel

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const (
	defaultRetryBackoff = 500 * time.Millisecond
	maxRetryBackoff     = 30 * time.Second
	sseKeepaliveEvery   = 15 * time.Second
)

//go:embed templates/*.html assets/*
var panelFiles embed.FS

type SessionStore interface {
	List(ctx context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error)
	Stream(ctx context.Context, sessionID sessionrt.SessionID) (<-chan sessionrt.Event, error)
	ListSessions(ctx context.Context) ([]sessionrt.SessionRecord, error)
}

type SessionMetadata struct {
	ConversationID   string
	ConversationName string
}

type SessionMetadataResolver func(sessionID sessionrt.SessionID) (SessionMetadata, bool)

type ServerOptions struct {
	ListenAddr      string
	Logger          *log.Logger
	Store           SessionStore
	SessionMetadata SessionMetadataResolver
	NodeSnapshot    func() []scheduler.NodeInfo
}

type Server struct {
	listenAddr      string
	logger          *log.Logger
	store           SessionStore
	sessionMetadata SessionMetadataResolver
	nodeSnapshot    func() []scheduler.NodeInfo
	templates       *template.Template
	assets          fs.FS
}

type pageData struct {
	HasSessionStore bool
}

type overviewData struct {
	Now   string
	Nodes []overviewNode
}

type overviewNode struct {
	NodeID        string
	Role          string
	HeartbeatAt   string
	Capabilities  string
	IsGatewayNode bool
}

type sessionsData struct {
	HasSessionStore bool
	Error           string
	Sessions        []sessionRow
}

type sessionRow struct {
	SessionID      string
	Title          string
	ConversationID string
	Status         string
	UpdatedAt      string
	LastSeq        uint64
}

type sessionDetailData struct {
	HasSessionStore bool
	SessionID       string
	Title           string
	ConversationID  string
	Error           string
	LastSeq         uint64
	Events          []eventRow
}

type eventRow struct {
	Seq        uint64
	Timestamp  string
	From       string
	Type       string
	Payload    string
	Collapsed  bool
	BadgeClass string
}

func NewServer(opts ServerOptions) (*Server, error) {
	listenAddr := strings.TrimSpace(opts.ListenAddr)
	if listenAddr == "" {
		return nil, fmt.Errorf("listen address is required")
	}
	tpl, err := template.New("panel").ParseFS(panelFiles, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse panel templates: %w", err)
	}
	assetsFS, err := fs.Sub(panelFiles, "assets")
	if err != nil {
		return nil, fmt.Errorf("open panel assets: %w", err)
	}
	nodeSnapshot := opts.NodeSnapshot
	if nodeSnapshot == nil {
		nodeSnapshot = func() []scheduler.NodeInfo { return nil }
	}
	return &Server{
		listenAddr:      listenAddr,
		logger:          opts.Logger,
		store:           opts.Store,
		sessionMetadata: opts.SessionMetadata,
		nodeSnapshot:    nodeSnapshot,
		templates:       tpl,
		assets:          assetsFS,
	}, nil
}

func (s *Server) RunWithRetry(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	backoff := defaultRetryBackoff

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		listener, err := net.Listen("tcp", s.listenAddr)
		if err != nil {
			s.logf("panel listen failed addr=%s err=%v retry_in=%s", s.listenAddr, err, backoff)
			if !sleepWithContext(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}

		mux := s.newMux()
		httpServer := &http.Server{Addr: s.listenAddr, Handler: mux}
		errCh := make(chan error, 1)
		s.logf("panel listening addr=%s", s.listenAddr)
		backoff = defaultRetryBackoff

		go func() {
			errCh <- httpServer.Serve(listener)
		}()

		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = httpServer.Shutdown(shutdownCtx)
			cancel()
			err := <-errCh
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		case err := <-errCh:
			if err == nil || errors.Is(err, http.ErrServerClosed) {
				if !sleepWithContext(ctx, backoff) {
					return nil
				}
				backoff = nextBackoff(backoff)
				continue
			}
			s.logf("panel serve error addr=%s err=%v retry_in=%s", s.listenAddr, err, backoff)
			if !sleepWithContext(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
		}
	}
}

func (s *Server) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_gopher/panel", s.handlePage)
	mux.HandleFunc("GET /_gopher/panel/health", s.handleHealth)
	mux.HandleFunc("GET /_gopher/panel/fragments/overview", s.handleOverview)
	mux.HandleFunc("GET /_gopher/panel/fragments/sessions", s.handleSessions)
	mux.HandleFunc("GET /_gopher/panel/fragments/session/{sessionID}", s.handleSessionDetail)
	mux.HandleFunc("GET /_gopher/panel/stream/session/{sessionID}", s.handleSessionStream)
	mux.HandleFunc("GET /_gopher/panel/assets/panel.css", s.handleCSS)
	return mux
}

func (s *Server) handlePage(w http.ResponseWriter, _ *http.Request) {
	s.renderTemplate(w, "page.html", pageData{HasSessionStore: s.store != nil})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"ok":                 true,
		"listen_addr":        s.listenAddr,
		"session_store":      s.store != nil,
		"tracked_node_count": len(s.nodeSnapshot()),
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, _ *http.Request) {
	nodes := s.nodeSnapshot()
	rows := make([]overviewNode, 0, len(nodes))
	for _, node := range nodes {
		rows = append(rows, overviewNode{
			NodeID:        strings.TrimSpace(node.NodeID),
			Role:          nodeRoleText(node.IsGateway),
			HeartbeatAt:   formatTime(node.LastHeartbeat),
			Capabilities:  formatCapabilities(node.Capabilities),
			IsGatewayNode: node.IsGateway,
		})
	}
	s.renderTemplate(w, "overview.html", overviewData{
		Now:   time.Now().UTC().Format(time.RFC3339),
		Nodes: rows,
	})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	data := sessionsData{HasSessionStore: s.store != nil}
	if s.store == nil {
		data.Error = "Session runtime is unavailable in this mode (matrix disabled)."
		s.renderTemplate(w, "sessions.html", data)
		return
	}

	records, err := s.store.ListSessions(r.Context())
	if err != nil {
		data.Error = fmt.Sprintf("List sessions failed: %v", err)
		s.renderTemplate(w, "sessions.html", data)
		return
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})

	rows := make([]sessionRow, 0, len(records))
	for _, record := range records {
		sessionID := strings.TrimSpace(string(record.SessionID))
		metadata := s.lookupSessionMetadata(record.SessionID)
		title := metadata.ConversationName
		if title == "" {
			title = metadata.ConversationID
		}
		if title == "" {
			title = sessionID
		}
		rows = append(rows, sessionRow{
			SessionID:      sessionID,
			Title:          title,
			ConversationID: metadata.ConversationID,
			Status:         sessionStatusText(record.Status),
			UpdatedAt:      formatTime(record.UpdatedAt),
			LastSeq:        record.LastSeq,
		})
	}
	data.Sessions = rows
	s.renderTemplate(w, "sessions.html", data)
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	data := sessionDetailData{
		HasSessionStore: s.store != nil,
		SessionID:       strings.TrimSpace(r.PathValue("sessionID")),
	}
	if s.store == nil {
		data.Error = "Session runtime is unavailable in this mode."
		s.renderTemplate(w, "session_detail.html", data)
		return
	}
	if data.SessionID == "" {
		data.Error = "Choose a session from the left to inspect its timeline."
		s.renderTemplate(w, "session_detail.html", data)
		return
	}

	events, err := s.store.List(r.Context(), sessionrt.SessionID(data.SessionID))
	if err != nil {
		data.Error = fmt.Sprintf("Load session failed: %v", err)
		s.renderTemplate(w, "session_detail.html", data)
		return
	}
	data.Events = toEventRows(events)
	if len(events) > 0 {
		data.LastSeq = events[len(events)-1].Seq
	}
	metadata := s.lookupSessionMetadata(sessionrt.SessionID(data.SessionID))
	data.ConversationID = metadata.ConversationID
	if metadata.ConversationName != "" {
		data.Title = metadata.ConversationName
	} else if metadata.ConversationID != "" {
		data.Title = metadata.ConversationID
	} else {
		data.Title = data.SessionID
	}
	s.renderTemplate(w, "session_detail.html", data)
}

func (s *Server) handleSessionStream(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "session runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if sessionID == "" {
		http.Error(w, "sessionID is required", http.StatusBadRequest)
		return
	}
	afterSeq, err := parseAfterSeq(r.URL.Query().Get("after_seq"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("content-type", "text/event-stream")
	w.Header().Set("cache-control", "no-cache")
	w.Header().Set("connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	events, err := s.store.List(r.Context(), sessionrt.SessionID(sessionID))
	if err != nil {
		http.Error(w, fmt.Sprintf("load session: %v", err), http.StatusNotFound)
		return
	}
	lastSeq := afterSeq
	for _, event := range events {
		if event.Seq <= afterSeq {
			continue
		}
		if err := writeSessionSSEEvent(w, sessionID, event); err != nil {
			return
		}
		if event.Seq > lastSeq {
			lastSeq = event.Seq
		}
	}
	flusher.Flush()

	stream, err := s.store.Stream(r.Context(), sessionrt.SessionID(sessionID))
	if err != nil {
		http.Error(w, fmt.Sprintf("subscribe session: %v", err), http.StatusNotFound)
		return
	}

	keepalive := time.NewTicker(sseKeepaliveEvery)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-stream:
			if !ok {
				return
			}
			if event.Seq <= lastSeq {
				continue
			}
			if err := writeSessionSSEEvent(w, sessionID, event); err != nil {
				return
			}
			lastSeq = event.Seq
			flusher.Flush()
		}
	}
}

func (s *Server) handleCSS(w http.ResponseWriter, _ *http.Request) {
	blob, err := fs.ReadFile(s.assets, "panel.css")
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("content-type", "text/css; charset=utf-8")
	_, _ = w.Write(blob)
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, fmt.Sprintf("template error: %v", err), http.StatusInternalServerError)
	}
}

func (s *Server) logf(format string, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Printf(format, args...)
}

func (s *Server) lookupSessionMetadata(sessionID sessionrt.SessionID) SessionMetadata {
	if s.sessionMetadata == nil {
		return SessionMetadata{}
	}
	metadata, ok := s.sessionMetadata(sessionID)
	if !ok {
		return SessionMetadata{}
	}
	metadata.ConversationID = strings.TrimSpace(metadata.ConversationID)
	metadata.ConversationName = strings.TrimSpace(metadata.ConversationName)
	return metadata
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func parseAfterSeq(raw string) (uint64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	seq, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("after_seq must be an unsigned integer")
	}
	return seq, nil
}

func writeSessionSSEEvent(w io.Writer, sessionID string, event sessionrt.Event) error {
	blob, err := json.Marshal(map[string]any{
		"session_id": sessionID,
		"seq":        event.Seq,
		"type":       event.Type,
	})
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: session-event\ndata: %s\n\n", string(blob)); err != nil {
		return err
	}
	return nil
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > maxRetryBackoff {
		return maxRetryBackoff
	}
	return next
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func toEventRows(events []sessionrt.Event) []eventRow {
	rows := make([]eventRow, 0, len(events))
	for _, event := range events {
		eventType := strings.TrimSpace(string(event.Type))
		rows = append(rows, eventRow{
			Seq:        event.Seq,
			Timestamp:  formatTime(event.Timestamp),
			From:       strings.TrimSpace(string(event.From)),
			Type:       eventType,
			Payload:    prettyPayload(event.Payload),
			Collapsed:  event.Type == sessionrt.EventAgentDelta || event.Type == sessionrt.EventAgentThinkingDelta,
			BadgeClass: eventBadgeClass(event.Type),
		})
	}
	return rows
}

func prettyPayload(value any) string {
	if value == nil {
		return "{}"
	}
	blob, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(blob)
}

func eventBadgeClass(eventType sessionrt.EventType) string {
	switch eventType {
	case sessionrt.EventMessage:
		return "badge-message"
	case sessionrt.EventToolCall:
		return "badge-tool-call"
	case sessionrt.EventToolResult:
		return "badge-tool-result"
	case sessionrt.EventError:
		return "badge-error"
	case sessionrt.EventAgentDelta, sessionrt.EventAgentThinkingDelta:
		return "badge-delta"
	case sessionrt.EventControl:
		return "badge-control"
	default:
		return "badge-default"
	}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func nodeRoleText(isGateway bool) string {
	if isGateway {
		return "gateway"
	}
	return "node"
}

func formatCapabilities(capabilities []scheduler.Capability) string {
	if len(capabilities) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		parts = append(parts, fmt.Sprintf("%s:%s", capabilityKindText(capability.Kind), strings.TrimSpace(capability.Name)))
	}
	return strings.Join(parts, ", ")
}

func capabilityKindText(kind scheduler.CapabilityKind) string {
	switch kind {
	case scheduler.CapabilityAgent:
		return "agent"
	case scheduler.CapabilityTool:
		return "tool"
	case scheduler.CapabilitySystem:
		return "system"
	default:
		return "unknown"
	}
}

func sessionStatusText(status sessionrt.SessionStatus) string {
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
