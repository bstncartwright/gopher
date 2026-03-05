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
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	adminRoot           = "/admin"
	legacyPanelRoot     = "/_gopher/panel"
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

type AgentInfo struct {
	AgentID              string
	Name                 string
	Role                 string
	Workspace            string
	ModelPolicy          string
	RequiredCapabilities []string
	EnabledTools         []string
	SkillsPaths          []string
	KnownAgents          []string
	FSRoots              []string
	AllowDomains         []string
	BlockDomains         []string
	CanShell             bool
	ApplyPatchEnabled    bool
	CaptureThinking      bool
	NetworkEnabled       bool
	MaxContextMessages   int
}

type ServerOptions struct {
	ListenAddr      string
	Store           SessionStore
	SessionMetadata SessionMetadataResolver
	NodeSnapshot    func() []scheduler.NodeInfo
	AgentSnapshot   func() []AgentInfo
	ControlDir      string
	CronStorePath   string
}

type Server struct {
	listenAddr      string
	store           SessionStore
	sessionMetadata SessionMetadataResolver
	nodeSnapshot    func() []scheduler.NodeInfo
	agentSnapshot   func() []AgentInfo
	controlDir      string
	cronStorePath   string
	templates       *template.Template
	assets          fs.FS
}

type pageData struct {
	HasSessionStore bool
	ActiveTab       string
	PanelRoot       string
}

type overviewNode struct {
	NodeID        string
	Role          string
	HeartbeatAt   string
	Capabilities  string
	IsGatewayNode bool
}

type nodesData struct {
	Now   string
	Nodes []overviewNode
}

type controlData struct {
	Now          string
	HasControl   bool
	ExecSummary  controlSummary
	WaitingItems []controlWaitingItem
	Delegations  []controlDelegation
}

type controlActionsData struct {
	Now           string
	HasControl    bool
	RecentActions []controlActionRecord
}

type cronData struct {
	Now          string
	HasCronStore bool
	Error        string
	TotalJobs    int
	EnabledJobs  int
	PausedJobs   int
	Jobs         []cronJobRow
}

type cronJobRow struct {
	ID        string
	SessionID string
	Status    string
	CronExpr  string
	Timezone  string
	CreatedBy string
	Message   string
	LastRunAt string
	NextRunAt string
	UpdatedAt string
}

type cronJobDisk struct {
	ID        string     `json:"id"`
	SessionID string     `json:"session_id"`
	Message   string     `json:"message"`
	CronExpr  string     `json:"cron_expr"`
	Timezone  string     `json:"timezone"`
	Enabled   bool       `json:"enabled"`
	CreatedBy string     `json:"created_by"`
	UpdatedAt time.Time  `json:"updated_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	NextRunAt *time.Time `json:"next_run_at,omitempty"`
}

type cronStoreDisk struct {
	Jobs []cronJobDisk `json:"jobs"`
}

type agentsData struct {
	Now    string
	Agents []agentRow
}

type agentRow struct {
	AgentID              string
	Name                 string
	Role                 string
	Workspace            string
	ModelPolicy          string
	RequiredCapabilities string
	EnabledTools         string
	SkillsPaths          string
	KnownAgents          string
	FSRoots              string
	NetworkSummary       string
	CanShell             string
	ApplyPatchEnabled    string
	CaptureThinking      string
	MaxContextMessages   int
}

type controlSummary struct {
	Active    int
	Paused    int
	Completed int
	Failed    int
	Waiting   int
	Delegated int
}

type controlWaitingItem struct {
	SessionID string
	Reason    string
	UpdatedAt string
}

type controlDelegation struct {
	DelegationID    string
	SourceSessionID string
	SourceAgentID   string
	TargetAgentID   string
	Status          string
	UpdatedAt       string
}

type controlActionRecord struct {
	Action    string
	SessionID string
	OK        bool
	UpdatedAt string
	Error     string
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
	Working        bool
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
	ContextHealth   *contextHealthData
}

type contextHealthData struct {
	ModelID                  string
	ModelProvider            string
	ModelDisplay             string
	ModelContextWindow       int
	ReserveTokens            int
	ReserveFloorTokens       int
	EstimatedInputTokens     int
	OverflowRetries          int
	OverflowStage            string
	SummaryStrategy          string
	ToolResultTruncation     int
	RecentMessagesUsedTokens int
	RecentMessagesCapTokens  int
	MemoryUsedTokens         int
	MemoryCapTokens          int
	CompactionUsedTokens     int
	CompactionCapTokens      int
}

type eventRow struct {
	Seq        uint64
	Timestamp  string
	From       string
	Type       string
	TypeLabel  string
	Payload    string
	Collapsed  bool
	Waiting    bool
	BadgeClass string
	RoleClass  string
}

func NewServer(opts ServerOptions) (*Server, error) {
	listenAddr := strings.TrimSpace(opts.ListenAddr)
	if listenAddr == "" {
		slog.Error("panel_server: listen address is required")
		return nil, fmt.Errorf("listen address is required")
	}
	tpl, err := template.New("panel").ParseFS(panelFiles, "templates/*.html")
	if err != nil {
		slog.Error("panel_server: failed to parse templates", "error", err)
		return nil, fmt.Errorf("parse panel templates: %w", err)
	}
	assetsFS, err := fs.Sub(panelFiles, "assets")
	if err != nil {
		slog.Error("panel_server: failed to open assets", "error", err)
		return nil, fmt.Errorf("open panel assets: %w", err)
	}
	nodeSnapshot := opts.NodeSnapshot
	if nodeSnapshot == nil {
		nodeSnapshot = func() []scheduler.NodeInfo { return nil }
	}
	agentSnapshot := opts.AgentSnapshot
	if agentSnapshot == nil {
		agentSnapshot = func() []AgentInfo { return nil }
	}
	controlDir := strings.TrimSpace(opts.ControlDir)
	cronStorePath := strings.TrimSpace(opts.CronStorePath)
	if cronStorePath == "" && controlDir != "" {
		cronStorePath = filepath.Join(filepath.Dir(controlDir), "cron", "jobs.json")
	}
	slog.Info("panel_server: created", "listen_addr", listenAddr, "has_store", opts.Store != nil)
	return &Server{
		listenAddr:      listenAddr,
		store:           opts.Store,
		sessionMetadata: opts.SessionMetadata,
		nodeSnapshot:    nodeSnapshot,
		agentSnapshot:   agentSnapshot,
		controlDir:      controlDir,
		cronStorePath:   cronStorePath,
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
			slog.Warn("panel_server: listen failed, retrying", "addr", s.listenAddr, "error", err, "retry_in", backoff)
			if !sleepWithContext(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}

		mux := s.newMux()
		httpServer := &http.Server{Addr: s.listenAddr, Handler: mux}
		errCh := make(chan error, 1)
		slog.Info("panel_server: listening", "addr", s.listenAddr)
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
			slog.Warn("panel_server: serve error, retrying", "addr", s.listenAddr, "error", err, "retry_in", backoff)
			if !sleepWithContext(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
		}
	}
}

func (s *Server) newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+adminRoot, s.handlePage)
	mux.HandleFunc("GET "+adminRoot+"/health", s.handleHealth)
	mux.HandleFunc("GET "+adminRoot+"/nodes", s.handleNodes)
	mux.HandleFunc("GET "+adminRoot+"/fragments/control", s.handleControl)
	mux.HandleFunc("GET "+adminRoot+"/fragments/nodes-table", s.handleNodesFragment)
	mux.HandleFunc("GET "+adminRoot+"/fragments/control-actions", s.handleControlActions)
	mux.HandleFunc("GET "+adminRoot+"/fragments/cron", s.handleCron)
	mux.HandleFunc("GET "+adminRoot+"/fragments/overview", s.handleOverview)
	mux.HandleFunc("GET "+adminRoot+"/fragments/sessions", s.handleSessions)
	mux.HandleFunc("GET "+adminRoot+"/fragments/session/{sessionID}", s.handleSessionDetail)
	mux.HandleFunc("GET "+adminRoot+"/fragments/agents", s.handleAgents)
	mux.HandleFunc("GET "+adminRoot+"/stream/session/{sessionID}", s.handleSessionStream)
	mux.HandleFunc("GET "+adminRoot+"/assets/panel.css", s.handleCSS)

	mux.HandleFunc("GET "+legacyPanelRoot, s.handleLegacyPage)
	mux.HandleFunc("GET "+legacyPanelRoot+"/tab/{tab}", s.handleLegacyPageTab)
	mux.HandleFunc("GET "+legacyPanelRoot+"/health", s.handleHealth)
	mux.HandleFunc("GET "+legacyPanelRoot+"/nodes", s.handleNodes)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/control", s.handleControl)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/nodes-table", s.handleNodesFragment)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/control-actions", s.handleControlActions)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/cron", s.handleCron)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/overview", s.handleOverview)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/sessions", s.handleSessions)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/session/{sessionID}", s.handleSessionDetail)
	mux.HandleFunc("GET "+legacyPanelRoot+"/fragments/agents", s.handleAgents)
	mux.HandleFunc("GET "+legacyPanelRoot+"/stream/session/{sessionID}", s.handleSessionStream)
	mux.HandleFunc("GET "+legacyPanelRoot+"/assets/panel.css", s.handleCSS)
	return mux
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	tab := ""
	if r != nil {
		tab = normalizePanelTab(r.URL.Query().Get("tab"))
	}
	s.renderTemplate(w, "page.html", pageData{
		HasSessionStore: s.store != nil,
		ActiveTab:       tab,
		PanelRoot:       adminRoot,
	})
}

func (s *Server) handleLegacyPage(w http.ResponseWriter, r *http.Request) {
	s.redirectToCanonicalPage(w, r, "")
}

func (s *Server) handleLegacyPageTab(w http.ResponseWriter, r *http.Request) {
	tab := normalizePanelTab(r.PathValue("tab"))
	s.redirectToCanonicalPage(w, r, tab)
}

func (s *Server) redirectToCanonicalPage(w http.ResponseWriter, r *http.Request, tab string) {
	params := urlValuesClone(r)
	normalized := normalizePanelTab(tab)
	if normalized == "" {
		normalized = normalizePanelTab(params.Get("tab"))
	}
	if normalized != "" && normalized != "control" {
		params.Set("tab", normalized)
	} else {
		params.Del("tab")
	}
	target := adminRoot
	if encoded := params.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"ok":                 true,
		"listen_addr":        s.listenAddr,
		"session_store":      s.store != nil,
		"tracked_node_count": len(s.nodeSnapshot()),
	})
}

func (s *Server) handleNodes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"nodes": s.nodeSnapshot(),
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, _ *http.Request) {
	// Keep overview route for compatibility and render the control board.
	s.handleControl(w, nil)
}

func (s *Server) handleControl(w http.ResponseWriter, _ *http.Request) {
	summary, waiting, delegations, actions := s.loadControlOverview()
	_ = actions
	s.renderTemplate(w, "control.html", controlData{
		Now:          time.Now().UTC().Format(time.RFC3339),
		HasControl:   s.controlDir != "",
		ExecSummary:  summary,
		WaitingItems: waiting,
		Delegations:  delegations,
	})
}

func (s *Server) handleNodesFragment(w http.ResponseWriter, _ *http.Request) {
	s.renderTemplate(w, "nodes.html", nodesData{
		Now:   time.Now().UTC().Format(time.RFC3339),
		Nodes: snapshotOverviewNodes(s.nodeSnapshot()),
	})
}

func (s *Server) handleControlActions(w http.ResponseWriter, _ *http.Request) {
	_, _, _, actions := s.loadControlOverview()
	s.renderTemplate(w, "control_actions.html", controlActionsData{
		Now:           time.Now().UTC().Format(time.RFC3339),
		HasControl:    s.controlDir != "",
		RecentActions: actions,
	})
}

func (s *Server) handleCron(w http.ResponseWriter, _ *http.Request) {
	data := cronData{
		Now:          time.Now().UTC().Format(time.RFC3339),
		HasCronStore: strings.TrimSpace(s.cronStorePath) != "",
	}
	if !data.HasCronStore {
		s.renderTemplate(w, "cron.html", data)
		return
	}

	jobs, err := readCronJobs(s.cronStorePath)
	if err != nil {
		data.Error = fmt.Sprintf("Load cron jobs failed: %v", err)
		s.renderTemplate(w, "cron.html", data)
		return
	}

	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Enabled != jobs[j].Enabled {
			return jobs[i].Enabled
		}
		leftNextZero := jobs[i].NextRunAt == nil || jobs[i].NextRunAt.IsZero()
		rightNextZero := jobs[j].NextRunAt == nil || jobs[j].NextRunAt.IsZero()
		if leftNextZero != rightNextZero {
			return !leftNextZero
		}
		if !leftNextZero && !jobs[i].NextRunAt.Equal(*jobs[j].NextRunAt) {
			return jobs[i].NextRunAt.Before(*jobs[j].NextRunAt)
		}
		return jobs[i].UpdatedAt.After(jobs[j].UpdatedAt)
	})

	rows := make([]cronJobRow, 0, len(jobs))
	for _, job := range jobs {
		data.TotalJobs++
		status := "paused"
		if job.Enabled {
			status = "enabled"
			data.EnabledJobs++
		} else {
			data.PausedJobs++
		}
		rows = append(rows, cronJobRow{
			ID:        strings.TrimSpace(job.ID),
			SessionID: strings.TrimSpace(job.SessionID),
			Status:    status,
			CronExpr:  strings.TrimSpace(job.CronExpr),
			Timezone:  strings.TrimSpace(job.Timezone),
			CreatedBy: strings.TrimSpace(job.CreatedBy),
			Message:   clipPanelText(strings.TrimSpace(job.Message), 180),
			LastRunAt: formatOptionalTime(job.LastRunAt),
			NextRunAt: formatOptionalTime(job.NextRunAt),
			UpdatedAt: formatTime(job.UpdatedAt),
		})
	}
	data.Jobs = rows
	s.renderTemplate(w, "cron.html", data)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	data := sessionsData{HasSessionStore: s.store != nil}
	if s.store == nil {
		data.Error = "Session runtime is unavailable in this mode."
		s.renderTemplate(w, "sessions.html", data)
		return
	}
	includeStale := false
	if r != nil {
		includeStale = parseTruthy(r.URL.Query().Get("include_stale"))
	}
	now := time.Now()

	records, err := s.store.ListSessions(r.Context())
	if err != nil {
		data.Error = fmt.Sprintf("List sessions failed: %v", err)
		s.renderTemplate(w, "sessions.html", data)
		return
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].InFlight != records[j].InFlight {
			return records[i].InFlight
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})

	rows := make([]sessionRow, 0, len(records))
	for _, record := range records {
		if !includeStale && isStaleSessionRecord(record, now) {
			continue
		}
		sessionID := strings.TrimSpace(string(record.SessionID))
		metadata := s.lookupSessionMetadata(record.SessionID)
		title := metadata.ConversationName
		if title == "" {
			title = strings.TrimSpace(record.DisplayName)
		}
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
			Working:        record.InFlight,
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
	data.ContextHealth = extractContextHealth(events)
	if len(events) > 0 {
		data.LastSeq = events[len(events)-1].Seq
	}
	metadata := s.lookupSessionMetadata(sessionrt.SessionID(data.SessionID))
	data.ConversationID = metadata.ConversationID
	if metadata.ConversationName != "" {
		data.Title = metadata.ConversationName
	} else if displayName := sessionrt.DisplayNameFromEvents(events); displayName != "" {
		data.Title = displayName
	} else if metadata.ConversationID != "" {
		data.Title = metadata.ConversationID
	} else {
		data.Title = data.SessionID
	}
	s.renderTemplate(w, "session_detail.html", data)
}

func (s *Server) handleAgents(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.agentSnapshot()
	rows := make([]agentRow, 0, len(snapshot))
	for _, info := range snapshot {
		agentID := strings.TrimSpace(info.AgentID)
		if agentID == "" {
			continue
		}
		name := strings.TrimSpace(info.Name)
		if name == "" {
			name = agentID
		}
		role := strings.TrimSpace(info.Role)
		if role == "" {
			role = "-"
		}
		workspace := strings.TrimSpace(info.Workspace)
		if workspace == "" {
			workspace = "-"
		}
		model := strings.TrimSpace(info.ModelPolicy)
		if model == "" {
			model = "-"
		}
		rows = append(rows, agentRow{
			AgentID:              agentID,
			Name:                 name,
			Role:                 role,
			Workspace:            workspace,
			ModelPolicy:          model,
			RequiredCapabilities: formatStringList(info.RequiredCapabilities),
			EnabledTools:         formatStringList(info.EnabledTools),
			SkillsPaths:          formatStringList(info.SkillsPaths),
			KnownAgents:          formatStringList(info.KnownAgents),
			FSRoots:              formatStringList(info.FSRoots),
			NetworkSummary:       formatNetworkSummary(info.NetworkEnabled, info.AllowDomains, info.BlockDomains),
			CanShell:             boolStateText(info.CanShell),
			ApplyPatchEnabled:    boolStateText(info.ApplyPatchEnabled),
			CaptureThinking:      boolStateText(info.CaptureThinking),
			MaxContextMessages:   info.MaxContextMessages,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].AgentID < rows[j].AgentID
	})
	s.renderTemplate(w, "agents.html", agentsData{
		Now:    time.Now().UTC().Format(time.RFC3339),
		Agents: rows,
	})
}

func snapshotOverviewNodes(nodes []scheduler.NodeInfo) []overviewNode {
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
	return rows
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
		slog.Error("panel_server: template render failed", "template", name, "error", err)
		http.Error(w, fmt.Sprintf("template error: %v", err), http.StatusInternalServerError)
	}
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

type pendingToolCallRow struct {
	RowIndex    int
	ToolName    string
	CallPayload map[string]any
}

func toEventRows(events []sessionrt.Event) []eventRow {
	rows := make([]eventRow, 0, len(events))
	pendingCalls := make([]pendingToolCallRow, 0, 4)

	for _, event := range events {
		if shouldHideEventInPanel(event.Type) {
			continue
		}

		switch event.Type {
		case sessionrt.EventToolCall:
			callPayload := clonePayloadMap(event.Payload)
			toolName := toolNameFromPayloadMap(callPayload)
			row := newEventRow(event)
			row.Payload = prettyPayload(mergedToolExecutionPayload(toolName, callPayload, nil, true))
			row.Waiting = true
			rows = append(rows, row)
			pendingCalls = append(pendingCalls, pendingToolCallRow{
				RowIndex:    len(rows) - 1,
				ToolName:    toolName,
				CallPayload: callPayload,
			})
		case sessionrt.EventToolResult:
			resultPayload := clonePayloadMap(event.Payload)
			toolName := toolNameFromPayloadMap(resultPayload)
			pendingIndex := findPendingToolCallIndex(pendingCalls, toolName)
			if pendingIndex >= 0 {
				pending := pendingCalls[pendingIndex]
				pendingCalls = append(pendingCalls[:pendingIndex], pendingCalls[pendingIndex+1:]...)
				resolvedName := coalesceTrimmed(toolName, pending.ToolName)
				row := &rows[pending.RowIndex]
				row.Payload = prettyPayload(mergedToolExecutionPayload(resolvedName, pending.CallPayload, resultPayload, false))
				row.Waiting = false
				row.BadgeClass = eventBadgeClass(sessionrt.EventToolResult)
				if strings.TrimSpace(row.TypeLabel) == string(sessionrt.EventToolCall) {
					if label := toolTypeLabelFromName(resolvedName); label != "" {
						row.TypeLabel = label
					}
				}
				continue
			}
			rows = append(rows, newEventRow(event))
		default:
			rows = append(rows, newEventRow(event))
		}
	}
	return rows
}

func newEventRow(event sessionrt.Event) eventRow {
	eventType := strings.TrimSpace(string(event.Type))
	row := eventRow{
		Seq:        event.Seq,
		Timestamp:  formatTime(event.Timestamp),
		From:       strings.TrimSpace(string(event.From)),
		Type:       eventType,
		TypeLabel:  eventTypeLabel(event, eventType),
		Payload:    prettyPayload(event.Payload),
		Collapsed:  event.Type == sessionrt.EventAgentDelta || event.Type == sessionrt.EventAgentThinkingDelta,
		BadgeClass: eventBadgeClass(event.Type),
	}
	if event.Type == sessionrt.EventMessage {
		if role, ok := messageRoleFromPayload(event.Payload); ok {
			row.BadgeClass = badgeClassForMessageRole(role)
			row.RoleClass = rowClassForMessageRole(role)
		}
	}
	return row
}

func shouldHideEventInPanel(eventType sessionrt.EventType) bool {
	switch eventType {
	case sessionrt.EventAgentDelta, sessionrt.EventAgentThinkingDelta:
		return true
	default:
		return false
	}
}

func eventTypeLabel(event sessionrt.Event, fallback string) string {
	if event.Type == sessionrt.EventMessage {
		if role, ok := messageRoleFromPayload(event.Payload); ok {
			return strings.ToUpper(string(role))
		}
	}
	if event.Type == sessionrt.EventToolCall {
		if name, ok := builtInToolNameFromPayload(event.Payload); ok {
			return name
		}
	}
	if fallback != "" {
		return fallback
	}
	return strings.TrimSpace(string(event.Type))
}

func builtInToolNameFromPayload(payload any) (string, bool) {
	name := toolNameFromPayloadMap(clonePayloadMap(payload))
	if name == "" || !isKnownBuiltInToolName(name) {
		return "", false
	}
	return name, true
}

func toolTypeLabelFromName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || !isKnownBuiltInToolName(name) {
		return ""
	}
	return name
}

func isKnownBuiltInToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read", "write", "edit", "apply_patch", "exec", "process", "delegate", "heartbeat", "cron", "web_search", "search_mcp", "search":
		return true
	default:
		return false
	}
}

func findPendingToolCallIndex(pending []pendingToolCallRow, toolName string) int {
	toolName = strings.TrimSpace(toolName)
	if toolName != "" {
		for idx := range pending {
			if strings.EqualFold(strings.TrimSpace(pending[idx].ToolName), toolName) {
				return idx
			}
		}
	}
	if len(pending) > 0 {
		return 0
	}
	return -1
}

func mergedToolExecutionPayload(toolName string, callPayload map[string]any, resultPayload map[string]any, waiting bool) map[string]any {
	out := map[string]any{
		"waiting": waiting,
	}
	if toolName = strings.TrimSpace(toolName); toolName != "" {
		out["name"] = toolName
	}
	if args := toolArgsFromPayloadMap(callPayload); args != nil {
		out["args"] = args
	}
	if len(callPayload) > 0 {
		out["tool_call"] = callPayload
	}
	if len(resultPayload) > 0 {
		out["tool_result"] = resultPayload
	}
	return out
}

func toolNameFromPayloadMap(payload map[string]any) string {
	for _, key := range []string{"tool_name", "name", "tool", "id"} {
		raw, ok := payload[key]
		if !ok {
			continue
		}
		name, ok := raw.(string)
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name != "" {
			return name
		}
	}
	return ""
}

func toolArgsFromPayloadMap(payload map[string]any) any {
	for _, key := range []string{"arguments", "args", "input", "params", "payload"} {
		value, ok := payload[key]
		if ok {
			return value
		}
	}
	return nil
}

func clonePayloadMap(payload any) map[string]any {
	src, ok := payload.(map[string]any)
	if !ok || src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func coalesceTrimmed(primary, fallback string) string {
	if value := strings.TrimSpace(primary); value != "" {
		return value
	}
	return strings.TrimSpace(fallback)
}

func messageRoleFromPayload(payload any) (sessionrt.Role, bool) {
	switch value := payload.(type) {
	case sessionrt.Message:
		return normalizeMessageRole(value.Role)
	case *sessionrt.Message:
		if value == nil {
			return "", false
		}
		return normalizeMessageRole(value.Role)
	case map[string]any:
		rawRole, ok := value["role"].(string)
		if !ok {
			return "", false
		}
		return normalizeMessageRole(sessionrt.Role(rawRole))
	default:
		return "", false
	}
}

func normalizeMessageRole(role sessionrt.Role) (sessionrt.Role, bool) {
	switch sessionrt.Role(strings.ToLower(strings.TrimSpace(string(role)))) {
	case sessionrt.RoleUser:
		return sessionrt.RoleUser, true
	case sessionrt.RoleAgent:
		return sessionrt.RoleAgent, true
	case sessionrt.RoleSystem:
		return sessionrt.RoleSystem, true
	default:
		return "", false
	}
}

func badgeClassForMessageRole(role sessionrt.Role) string {
	switch role {
	case sessionrt.RoleUser:
		return "badge-message-user"
	case sessionrt.RoleAgent:
		return "badge-message-agent"
	case sessionrt.RoleSystem:
		return "badge-message-system"
	default:
		return "badge-message"
	}
}

func rowClassForMessageRole(role sessionrt.Role) string {
	switch role {
	case sessionrt.RoleUser:
		return "event-message-user"
	case sessionrt.RoleAgent:
		return "event-message-agent"
	case sessionrt.RoleSystem:
		return "event-message-system"
	default:
		return ""
	}
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

func extractContextHealth(events []sessionrt.Event) *contextHealthData {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if event.Type != sessionrt.EventStatePatch {
			continue
		}
		payload := clonePayloadMap(event.Payload)
		if len(payload) == 0 {
			continue
		}
		modelID := stringPayloadValue(payload, "model_id")
		modelProvider := stringPayloadValue(payload, "model_provider")
		return &contextHealthData{
			ModelID:                  modelID,
			ModelProvider:            modelProvider,
			ModelDisplay:             formatModelDisplay(modelID, modelProvider),
			ModelContextWindow:       intPayloadValue(payload, "model_context_window"),
			ReserveTokens:            intPayloadValue(payload, "reserve_tokens"),
			ReserveFloorTokens:       intPayloadValue(payload, "reserve_floor_tokens"),
			EstimatedInputTokens:     intPayloadValue(payload, "estimated_input_tokens"),
			OverflowRetries:          intPayloadValue(payload, "overflow_retries"),
			OverflowStage:            stringPayloadValue(payload, "overflow_stage"),
			SummaryStrategy:          stringPayloadValue(payload, "summary_strategy"),
			ToolResultTruncation:     intPayloadValue(payload, "tool_result_truncation_count"),
			RecentMessagesUsedTokens: intPayloadValue(payload, "recent_messages_used_tokens"),
			RecentMessagesCapTokens:  intPayloadValue(payload, "recent_messages_cap_tokens"),
			MemoryUsedTokens:         intPayloadValue(payload, "retrieved_memory_used_tokens"),
			MemoryCapTokens:          intPayloadValue(payload, "retrieved_memory_cap_tokens"),
			CompactionUsedTokens:     intPayloadValue(payload, "compaction_used_tokens"),
			CompactionCapTokens:      intPayloadValue(payload, "compaction_cap_tokens"),
		}
	}
	return nil
}

func intPayloadValue(payload map[string]any, key string) int {
	raw, ok := payload[key]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		value, err := v.Int64()
		if err != nil {
			return 0
		}
		return int(value)
	default:
		return 0
	}
}

func stringPayloadValue(payload map[string]any, key string) string {
	raw, ok := payload[key]
	if !ok {
		return ""
	}
	if text, ok := raw.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func formatModelDisplay(modelID, modelProvider string) string {
	modelID = strings.TrimSpace(modelID)
	modelProvider = strings.TrimSpace(modelProvider)
	if modelID == "" {
		return modelProvider
	}
	if modelProvider == "" {
		return modelID
	}
	return modelID + " (" + modelProvider + ")"
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

func isStaleSessionRecord(record sessionrt.SessionRecord, now time.Time) bool {
	lastActivity := record.UpdatedAt
	if lastActivity.IsZero() {
		lastActivity = record.CreatedAt
	}
	return sessionrt.IsStaleByDailyReset(lastActivity, now, sessionrt.DefaultDailyResetPolicy())
}

func parseTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (s *Server) loadControlOverview() (controlSummary, []controlWaitingItem, []controlDelegation, []controlActionRecord) {
	if s == nil || strings.TrimSpace(s.controlDir) == "" {
		return controlSummary{}, nil, nil, nil
	}
	indexPath := filepath.Join(s.controlDir, "session_index.json")
	indexBlob, err := os.ReadFile(indexPath)
	if err != nil {
		return controlSummary{}, nil, nil, nil
	}
	index := map[string]any{}
	if err := json.Unmarshal(indexBlob, &index); err != nil {
		return controlSummary{}, nil, nil, nil
	}
	summary := controlSummary{}
	if rawSummary, ok := index["summary"].(map[string]any); ok {
		summary.Active = int(asInt64(rawSummary["active"]))
		summary.Paused = int(asInt64(rawSummary["paused"]))
		summary.Completed = int(asInt64(rawSummary["completed"]))
		summary.Failed = int(asInt64(rawSummary["failed"]))
		summary.Waiting = int(asInt64(rawSummary["waiting"]))
		summary.Delegated = int(asInt64(rawSummary["delegated"]))
	}
	waiting := make([]controlWaitingItem, 0)
	if rawSessions, ok := index["sessions"].([]any); ok {
		for _, raw := range rawSessions {
			session, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			waitingOnHuman, _ := session["waiting_on_human"].(bool)
			if !waitingOnHuman {
				continue
			}
			waiting = append(waiting, controlWaitingItem{
				SessionID: strings.TrimSpace(asString(session["session_id"])),
				Reason:    strings.TrimSpace(asString(session["waiting_reason"])),
				UpdatedAt: strings.TrimSpace(asString(session["updated_at"])),
			})
		}
	}
	delegations := readControlDelegations(filepath.Join(s.controlDir, "delegations.jsonl"), 10)
	actions := readControlActions(filepath.Join(s.controlDir, "actions", "applied.jsonl"), 10)
	return summary, waiting, delegations, actions
}

func readControlDelegations(path string, limit int) []controlDelegation {
	records := make([]controlDelegation, 0)
	blob, err := os.ReadFile(path)
	if err != nil {
		return records
	}
	lines := strings.Split(strings.TrimSpace(string(blob)), "\n")
	for i := len(lines) - 1; i >= 0 && len(records) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		item := map[string]any{}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		records = append(records, controlDelegation{
			DelegationID:    asString(item["delegation_id"]),
			SourceSessionID: asString(item["source_session_id"]),
			SourceAgentID:   asString(item["source_agent_id"]),
			TargetAgentID:   asString(item["target_agent_id"]),
			Status:          asString(item["status"]),
			UpdatedAt:       asString(item["ts"]),
		})
	}
	return records
}

func readControlActions(path string, limit int) []controlActionRecord {
	records := make([]controlActionRecord, 0)
	blob, err := os.ReadFile(path)
	if err != nil {
		return records
	}
	lines := strings.Split(strings.TrimSpace(string(blob)), "\n")
	for i := len(lines) - 1; i >= 0 && len(records) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		item := map[string]any{}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		ok, _ := item["ok"].(bool)
		records = append(records, controlActionRecord{
			Action:    strings.TrimSpace(asString(item["action"])),
			SessionID: strings.TrimSpace(asString(item["session_id"])),
			OK:        ok,
			UpdatedAt: strings.TrimSpace(asString(item["ts"])),
			Error:     strings.TrimSpace(asString(item["error"])),
		})
	}
	return records
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}

func asInt64(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func readCronJobs(path string) ([]cronJobDisk, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(blob))) == 0 {
		return nil, nil
	}
	doc := cronStoreDisk{}
	if err := json.Unmarshal(blob, &doc); err != nil {
		return nil, err
	}
	return doc.Jobs, nil
}

func formatOptionalTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "-"
	}
	return formatTime(*value)
}

func clipPanelText(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	if max == 1 {
		return "…"
	}
	return value[:max-1] + "…"
}

func formatStringList(values []string) string {
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		candidate := strings.TrimSpace(value)
		if candidate == "" {
			continue
		}
		trimmed = append(trimmed, candidate)
	}
	if len(trimmed) == 0 {
		return "-"
	}
	return strings.Join(trimmed, ", ")
}

func boolStateText(value bool) string {
	if value {
		return "enabled"
	}
	return "disabled"
}

func formatNetworkSummary(enabled bool, allow []string, block []string) string {
	if !enabled {
		return "disabled"
	}
	allowText := formatStringList(allow)
	blockText := formatStringList(block)
	if allowText == "-" && blockText == "-" {
		return "enabled"
	}
	return fmt.Sprintf("allow: %s · block: %s", allowText, blockText)
}

func normalizePanelTab(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "control":
		return "control"
	case "sessions":
		return "sessions"
	case "nodes":
		return "nodes"
	case "actions", "control-actions":
		return "actions"
	case "cron":
		return "cron"
	case "agents":
		return "agents"
	default:
		return ""
	}
}

func urlValuesClone(r *http.Request) url.Values {
	if r == nil || r.URL == nil {
		return url.Values{}
	}
	out := url.Values{}
	for key, values := range r.URL.Query() {
		copied := make([]string, len(values))
		copy(copied, values)
		out[key] = copied
	}
	return out
}
