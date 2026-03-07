package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const (
	sectionOverview      = "overview"
	sectionWork          = "work"
	sectionAutomations   = "automations"
	sectionFleet         = "fleet"
	fleetViewNodes       = "nodes"
	fleetViewAgents      = "agents"
	fleetViewRemotes     = "remotes"
	workInitialPageLimit = 40
	nodeStaleAfter       = 90 * time.Second
)

type shellNavItem struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

type shellMetric struct {
	Label string
	Value int
	Tone  string
}

type pageViewData struct {
	CurrentSection  string
	PanelRoot       string
	ContentTemplate string
	PageTitle       string
	PageSubtitle    string
	NavItems        []shellNavItem
	StatusStrip     []shellMetric
	Overview        overviewPageData
	Work            workPageData
	Automations     automationsPageData
	Fleet           fleetPageData
}

type overviewPageData struct {
	AttentionGroups []overviewAttentionGroup
	ActivityItems   []overviewActivityItem
}

type overviewAttentionGroup struct {
	Title     string
	Tone      string
	EmptyText string
	Items     []overviewAttentionItem
}

type overviewAttentionItem struct {
	Title   string
	Summary string
	Meta    string
	Href    string
}

type overviewActivityItem struct {
	Title     string
	Summary   string
	Timestamp string
	Href      string
	Tone      string
	SortAt    time.Time
}

type workPageData struct {
	HasSessionStore bool
	InitialSession  string
	InitialFilter   string
	InitialView     string
	InitialNoise    string
	InitialEventSeq string
}

type automationsPageData struct {
	Summary       automationSummary
	AttentionJobs []automationJobCard
	ScheduledJobs []automationJobCard
	PausedJobs    []automationJobCard
	HasCronStore  bool
	Error         string
}

type automationSummary struct {
	Total   int
	Enabled int
	Paused  int
	Failed  int
}

type automationJobCard struct {
	ID          string
	SessionID   string
	Schedule    string
	RunStatus   string
	NextRunAt   string
	LastRunAt   string
	UpdatedAt   string
	Timezone    string
	Message     string
	MessageFull string
	CreatedBy   string
	CronExpr    string
	Enabled     bool
	Tone        string
}

type fleetPageData struct {
	View        string
	ViewTabs    []fleetViewTab
	NodeCards   []fleetNodeCard
	AgentCards  []fleetAgentCard
	RemoteCards []fleetRemoteCard
}

type fleetViewTab struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

type fleetNodeCard struct {
	ID            string
	Role          string
	Capabilities  string
	Agents        string
	Version       string
	HeartbeatAt   string
	HeartbeatText string
	Tone          string
	Status        string
}

type fleetAgentCard struct {
	ID                 string
	Name               string
	Role               string
	Workspace          string
	Model              string
	EnabledTools       string
	RequiredCaps       string
	Network            string
	Thinking           string
	Shell              string
	Patch              string
	MaxContextMessages int
}

type fleetRemoteCard struct {
	ID          string
	DisplayName string
	Description string
	Endpoint    string
	Health      string
	LastRefresh string
	LastError   string
}

type workSessionsResponse struct {
	GeneratedAt string               `json:"generated_at"`
	Sessions    []workSessionSummary `json:"sessions"`
}

type workSessionSummary struct {
	SessionID      string    `json:"session_id"`
	Title          string    `json:"title"`
	ConversationID string    `json:"conversation_id,omitempty"`
	Status         string    `json:"status"`
	Working        bool      `json:"working"`
	WaitingOnHuman bool      `json:"waiting_on_human"`
	WaitingReason  string    `json:"waiting_reason,omitempty"`
	HasAnomaly     bool      `json:"has_anomaly"`
	LatestDigest   string    `json:"latest_digest"`
	UpdatedAt      string    `json:"updated_at"`
	LastSeq        uint64    `json:"last_seq"`
	PriorityLabel  string    `json:"priority_label"`
	UpdatedAtTime  time.Time `json:"-"`
	Priority       int       `json:"-"`
}

type workSessionDetailResponse struct {
	Session       workSessionSummary       `json:"session"`
	Story         workSessionStory         `json:"story"`
	Counts        map[string]int           `json:"counts"`
	LatestAnomaly string                   `json:"latest_anomaly,omitempty"`
	ContextHealth *workContextHealth       `json:"context_health,omitempty"`
	Timeline      workTimelinePageResponse `json:"timeline"`
}

type workSessionStory struct {
	Goal               string `json:"goal,omitempty"`
	CurrentState       string `json:"current_state,omitempty"`
	CurrentStateDetail string `json:"current_state_detail,omitempty"`
	LatestConclusion   string `json:"latest_conclusion,omitempty"`
	LastMeaningfulStep string `json:"last_meaningful_step,omitempty"`
	LatestAnomaly      string `json:"latest_anomaly,omitempty"`
}

type workTimelinePageResponse struct {
	SessionID string              `json:"session_id"`
	FirstSeq  uint64              `json:"first_seq"`
	LastSeq   uint64              `json:"last_seq"`
	HasOlder  bool                `json:"has_older"`
	Events    []workTimelineEvent `json:"events"`
}

type workEventDetailResponse struct {
	Event         *workTimelineEvent `json:"event,omitempty"`
	ContextHealth *workContextHealth `json:"context_health,omitempty"`
	OutOfWindow   bool               `json:"out_of_window"`
}

type workTimelineEvent struct {
	Seq          uint64   `json:"seq"`
	Timestamp    string   `json:"timestamp"`
	From         string   `json:"from"`
	Type         string   `json:"type"`
	TypeLabel    string   `json:"type_label"`
	Category     string   `json:"category"`
	Digest       string   `json:"digest"`
	Emoji        string   `json:"emoji"`
	Title        string   `json:"title"`
	Subtitle     string   `json:"subtitle,omitempty"`
	Tone         string   `json:"tone"`
	KeyFacts     []string `json:"key_facts"`
	RawJSON      string   `json:"raw_json"`
	Waiting      bool     `json:"waiting"`
	Anomaly      bool     `json:"anomaly"`
	IsMeaningful bool     `json:"is_meaningful"`
	BundleKind   string   `json:"bundle_kind,omitempty"`
	BundleID     string   `json:"bundle_id,omitempty"`
	BundleTitle  string   `json:"bundle_title,omitempty"`
	ResultStatus string   `json:"result_status,omitempty"`
	GroupKey     string   `json:"group_key,omitempty"`
	GroupLabel   string   `json:"group_label,omitempty"`
}

type workContextHealth struct {
	ModelDisplay         string `json:"model_display"`
	ModelContextWindow   int    `json:"model_context_window"`
	ReserveTokens        int    `json:"reserve_tokens"`
	EstimatedInputTokens int    `json:"estimated_input_tokens"`
	OverflowRetries      int    `json:"overflow_retries"`
	OverflowStage        string `json:"overflow_stage,omitempty"`
	SummaryStrategy      string `json:"summary_strategy,omitempty"`
	ToolTruncation       int    `json:"tool_truncation"`
	RecentMessages       string `json:"recent_messages"`
	Memory               string `json:"memory"`
	Compaction           string `json:"compaction"`
}

type pendingTimelineTool struct {
	Index       int
	ToolName    string
	CallPayload map[string]any
	CallEvent   sessionrt.Event
}

func (s *Server) handleAdminEntry(w http.ResponseWriter, r *http.Request) {
	if s.redirectLegacyQueryIfNeeded(w, r) {
		return
	}
	data := s.newPageViewData(r.Context(), sectionOverview)
	data.PageTitle = "Overview"
	data.PageSubtitle = "Prioritize failures, blocked work, unhealthy automations, and runtime drift from one landing page."
	data.ContentTemplate = "overview_page.html"
	data.Overview = s.buildOverviewPageData(r.Context())
	s.renderTemplate(w, "page.html", data)
}

func (s *Server) handleWorkPage(w http.ResponseWriter, r *http.Request) {
	data := s.newPageViewData(r.Context(), sectionWork)
	data.PageTitle = "Work"
	data.PageSubtitle = "Inspect live sessions with an action-oriented queue, narrated work log, and event inspector."
	data.ContentTemplate = "work_page.html"
	data.Work = workPageData{
		HasSessionStore: s.store != nil,
		InitialSession:  strings.TrimSpace(r.PathValue("sessionID")),
		InitialFilter:   normalizeWorkFilter(r.URL.Query().Get("filter")),
		InitialView:     normalizeWorkView(r.URL.Query().Get("view")),
		InitialNoise:    normalizeWorkNoise(r.URL.Query().Get("noise")),
		InitialEventSeq: strings.TrimSpace(r.URL.Query().Get("event")),
	}
	s.renderTemplate(w, "page.html", data)
}

func (s *Server) handleAutomationsPage(w http.ResponseWriter, r *http.Request) {
	data := s.newPageViewData(r.Context(), sectionAutomations)
	data.PageTitle = "Automations"
	data.PageSubtitle = "Track scheduled jobs by urgency and next run, without falling into raw-table scanning."
	data.ContentTemplate = "automations_page.html"
	data.Automations = s.buildAutomationsPageData()
	s.renderTemplate(w, "page.html", data)
}

func (s *Server) handleFleetPage(w http.ResponseWriter, r *http.Request) {
	view := normalizeFleetView(r.URL.Query().Get("view"))
	data := s.newPageViewData(r.Context(), sectionFleet)
	data.PageTitle = "Fleet"
	data.PageSubtitle = "Review node freshness, agent configuration, and remote targets without competing with active work."
	data.ContentTemplate = "fleet_page.html"
	data.Fleet = s.buildFleetPageData(view)
	s.renderTemplate(w, "page.html", data)
}

func (s *Server) handleLegacyPanelEntry(w http.ResponseWriter, r *http.Request) {
	target := s.routeForLegacyTab(strings.TrimSpace(r.URL.Query().Get("tab")), strings.TrimSpace(r.URL.Query().Get("session")))
	redirect := target
	if raw := s.legacyQueryStringForTarget(target, r.URL.Query()); raw != "" {
		redirect += "?" + raw
	}
	http.Redirect(w, r, redirect, http.StatusPermanentRedirect)
}

func (s *Server) handleLegacyPanelTab(w http.ResponseWriter, r *http.Request) {
	target := s.routeForLegacyTab(strings.TrimSpace(r.PathValue("tab")), strings.TrimSpace(r.URL.Query().Get("session")))
	redirect := target
	if raw := s.legacyQueryStringForTarget(target, r.URL.Query()); raw != "" {
		redirect += "?" + raw
	}
	http.Redirect(w, r, redirect, http.StatusPermanentRedirect)
}

func (s *Server) handleWorkSessionsAPI(w http.ResponseWriter, r *http.Request) {
	summaries, err := s.buildWorkSessionSummaries(r.Context(), false)
	if err != nil {
		http.Error(w, fmt.Sprintf("list sessions: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, workSessionsResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Sessions:    summaries,
	})
}

func (s *Server) handleWorkSessionAPI(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "session runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if sessionID == "" {
		http.Error(w, "sessionID is required", http.StatusBadRequest)
		return
	}
	pageSize, err := parseLimitParam(r.URL.Query().Get("limit"), workInitialPageLimit, timelineStreamLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.buildWorkSessionDetail(r.Context(), sessionID, pageSize)
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

func (s *Server) handleWorkSessionEventsAPI(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "session runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if sessionID == "" {
		http.Error(w, "sessionID is required", http.StatusBadRequest)
		return
	}
	beforeSeq, err := parseSeqParam(r.URL.Query().Get("before_seq"), "before_seq")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	afterSeq, err := parseSeqParam(r.URL.Query().Get("after_seq"), "after_seq")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if beforeSeq > 0 && afterSeq > 0 {
		http.Error(w, "before_seq and after_seq cannot be combined", http.StatusBadRequest)
		return
	}
	limit, err := parseLimitParam(r.URL.Query().Get("limit"), timelinePageLimit, timelineStreamLimit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := s.buildWorkTimelinePage(r.Context(), sessionID, beforeSeq, afterSeq, limit)
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

func (s *Server) handleWorkEventDetailAPI(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "session runtime unavailable", http.StatusServiceUnavailable)
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("sessionID"))
	if sessionID == "" {
		http.Error(w, "sessionID is required", http.StatusBadRequest)
		return
	}
	seq, err := strconv.ParseUint(strings.TrimSpace(r.PathValue("seq")), 10, 64)
	if err != nil || seq == 0 {
		http.Error(w, "seq must be an unsigned integer", http.StatusBadRequest)
		return
	}
	resp, err := s.buildWorkEventDetail(r.Context(), sessionID, seq)
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

func (s *Server) handleWorkJS(w http.ResponseWriter, _ *http.Request) {
	blob, err := fs.ReadFile(s.assets, "work.js")
	if err != nil {
		http.Error(w, "asset not found", http.StatusNotFound)
		return
	}
	w.Header().Set("content-type", "application/javascript; charset=utf-8")
	_, _ = w.Write(blob)
}

func (s *Server) newPageViewData(ctx context.Context, current string) pageViewData {
	shell := s.buildShellMetrics(ctx)
	nav := []shellNavItem{
		{Key: sectionOverview, Label: "Overview", Href: adminRoot, Active: current == sectionOverview},
		{Key: sectionWork, Label: "Work", Href: adminRoot + "/work", Active: current == sectionWork},
		{Key: sectionAutomations, Label: "Automations", Href: adminRoot + "/automations", Active: current == sectionAutomations},
		{Key: sectionFleet, Label: "Fleet", Href: adminRoot + "/fleet", Active: current == sectionFleet},
	}
	return pageViewData{
		CurrentSection: current,
		PanelRoot:      adminRoot,
		NavItems:       nav,
		StatusStrip: []shellMetric{
			{Label: "Active", Value: shell.Active, Tone: "good"},
			{Label: "Failed", Value: shell.Failed, Tone: "danger"},
			{Label: "Waiting", Value: shell.Waiting, Tone: "warn"},
			{Label: "Delegated", Value: shell.Delegated, Tone: "neutral"},
			{Label: "Automations", Value: shell.AutomationsEnabled, Tone: "good"},
			{Label: "Paused", Value: shell.AutomationsPaused, Tone: "muted"},
			{Label: "Nodes", Value: shell.NodeCount, Tone: "neutral"},
		},
	}
}

type shellSnapshot struct {
	Active             int
	Paused             int
	Failed             int
	Waiting            int
	Delegated          int
	AutomationsEnabled int
	AutomationsPaused  int
	NodeCount          int
}

func (s *Server) buildShellMetrics(ctx context.Context) shellSnapshot {
	snapshot := shellSnapshot{NodeCount: len(s.nodeSnapshot())}
	if s.store != nil {
		if records, err := s.store.ListSessions(ctx); err == nil {
			for _, record := range records {
				switch sessionStatusText(record.Status) {
				case "active":
					snapshot.Active++
				case "paused":
					snapshot.Paused++
				case "failed":
					snapshot.Failed++
				}
			}
		}
	}
	if summary, waiting, delegations, _ := s.loadControlOverview(); summary != (controlSummary{}) || len(waiting) > 0 || len(delegations) > 0 {
		if summary.Active > snapshot.Active {
			snapshot.Active = summary.Active
		}
		if summary.Paused > snapshot.Paused {
			snapshot.Paused = summary.Paused
		}
		if summary.Failed > snapshot.Failed {
			snapshot.Failed = summary.Failed
		}
		snapshot.Waiting = maxInt(summary.Waiting, len(waiting))
		for _, delegation := range delegations {
			if normalizeStatusTone(delegation.Status) == "active" {
				snapshot.Delegated++
			}
		}
	}
	if jobs, err := readCronJobs(s.cronStorePath); err == nil {
		for _, job := range jobs {
			if job.Enabled {
				snapshot.AutomationsEnabled++
			} else {
				snapshot.AutomationsPaused++
			}
		}
	}
	return snapshot
}

func (s *Server) buildOverviewPageData(ctx context.Context) overviewPageData {
	attention := make([]overviewAttentionGroup, 0, 5)
	failedSessions := make([]overviewAttentionItem, 0)
	waitingItems := make([]overviewAttentionItem, 0)
	failedJobs := make([]overviewAttentionItem, 0)
	nodeIssues := make([]overviewAttentionItem, 0)
	delegations := make([]overviewAttentionItem, 0)

	sessionSummaries, _ := s.buildWorkSessionSummaries(ctx, false)
	for _, session := range sessionSummaries {
		switch {
		case session.Status == "failed":
			failedSessions = append(failedSessions, overviewAttentionItem{
				Title:   session.Title,
				Summary: fallbackText(session.LatestDigest, "Session failed without a recent digest."),
				Meta:    fmt.Sprintf("Session %s", session.SessionID),
				Href:    adminRoot + "/work/" + session.SessionID,
			})
		case session.WaitingOnHuman:
			waitingItems = append(waitingItems, overviewAttentionItem{
				Title:   session.Title,
				Summary: fallbackText(session.WaitingReason, "Waiting on human input."),
				Meta:    fmt.Sprintf("Session %s", session.SessionID),
				Href:    adminRoot + "/work/" + session.SessionID,
			})
		}
	}

	_, waiting, rawDelegations, actions := s.loadControlOverview()
	if len(waitingItems) == 0 {
		for _, item := range waiting {
			waitingItems = append(waitingItems, overviewAttentionItem{
				Title:   item.SessionID,
				Summary: fallbackText(item.Reason, "Waiting on human input."),
				Meta:    item.UpdatedAt,
				Href:    adminRoot + "/work/" + item.SessionID,
			})
		}
	}
	for _, delegation := range rawDelegations {
		if normalizeStatusTone(delegation.Status) != "active" {
			continue
		}
		delegations = append(delegations, overviewAttentionItem{
			Title:   delegation.DelegationID,
			Summary: fmt.Sprintf("%s -> %s", fallbackText(delegation.SourceAgentID, delegation.SourceSessionID), fallbackText(delegation.TargetAgentID, "target")),
			Meta:    fallbackText(delegation.UpdatedAt, delegation.Status),
			Href:    adminRoot + "/work/" + delegation.SourceSessionID,
		})
	}

	if jobs, err := readCronJobs(s.cronStorePath); err == nil {
		for _, job := range jobs {
			status := normalizeStatusTone(job.LastRunStatus)
			if status == "failed" || status == "danger" || (!job.Enabled && status != "") {
				failedJobs = append(failedJobs, overviewAttentionItem{
					Title:   job.ID,
					Summary: clipPanelText(strings.TrimSpace(job.Message), 140),
					Meta:    fallbackText(formatOptionalTime(job.NextRunAt), fallbackText(formatOptionalTime(job.LastRunAt), "No run timestamps")),
					Href:    adminRoot + "/automations#job-" + job.ID,
				})
			}
		}
	}

	now := time.Now().UTC()
	for _, node := range s.nodeSnapshot() {
		if nodeIsStale(node, now) {
			nodeIssues = append(nodeIssues, overviewAttentionItem{
				Title:   node.NodeID,
				Summary: "Node heartbeat is stale.",
				Meta:    relativeAgeText(node.LastHeartbeat, now),
				Href:    adminRoot + "/fleet?view=nodes#node-" + node.NodeID,
			})
		}
	}

	attention = append(attention,
		overviewAttentionGroup{Title: "Failed Sessions", Tone: "danger", EmptyText: "No failed sessions.", Items: failedSessions},
		overviewAttentionGroup{Title: "Waiting On Human", Tone: "warn", EmptyText: "No sessions are waiting on human input.", Items: waitingItems},
		overviewAttentionGroup{Title: "Failed Automations", Tone: "danger", EmptyText: "No automation failures need attention.", Items: failedJobs},
		overviewAttentionGroup{Title: "Unhealthy Nodes", Tone: "warn", EmptyText: "No stale or unhealthy nodes.", Items: nodeIssues},
		overviewAttentionGroup{Title: "Active Delegations", Tone: "neutral", EmptyText: "No active delegations.", Items: delegations},
	)

	activity := s.buildOverviewActivity(sessionSummaries, actions)
	return overviewPageData{
		AttentionGroups: attention,
		ActivityItems:   activity,
	}
}

func (s *Server) buildOverviewActivity(sessions []workSessionSummary, actions []controlActionRecord) []overviewActivityItem {
	items := make([]overviewActivityItem, 0, 12)
	for _, session := range sessions {
		items = append(items, overviewActivityItem{
			Title:     session.Title,
			Summary:   fallbackText(session.LatestDigest, "No recent digest."),
			Timestamp: session.UpdatedAt,
			Href:      adminRoot + "/work/" + session.SessionID,
			Tone:      session.Status,
			SortAt:    session.UpdatedAtTime,
		})
	}
	for _, action := range actions {
		parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(action.UpdatedAt))
		summary := "Applied successfully."
		tone := "good"
		if !action.OK {
			tone = "danger"
			summary = fallbackText(action.Error, "The control action failed.")
		}
		items = append(items, overviewActivityItem{
			Title:     strings.Title(fallbackText(action.Action, "Control action")),
			Summary:   summary,
			Timestamp: action.UpdatedAt,
			Href:      adminRoot + "/work/" + action.SessionID,
			Tone:      tone,
			SortAt:    parsed,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].SortAt.After(items[j].SortAt)
	})
	if len(items) > 8 {
		items = items[:8]
	}
	return items
}

func (s *Server) buildAutomationsPageData() automationsPageData {
	data := automationsPageData{
		HasCronStore: strings.TrimSpace(s.cronStorePath) != "",
	}
	if !data.HasCronStore {
		return data
	}
	jobs, err := readCronJobs(s.cronStorePath)
	if err != nil {
		data.Error = fmt.Sprintf("Load cron jobs failed: %v", err)
		return data
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Enabled != jobs[j].Enabled {
			return jobs[i].Enabled
		}
		leftNext := optionalTimeValue(jobs[i].NextRunAt)
		rightNext := optionalTimeValue(jobs[j].NextRunAt)
		if !leftNext.Equal(rightNext) {
			return leftNext.Before(rightNext)
		}
		return jobs[i].UpdatedAt.After(jobs[j].UpdatedAt)
	})
	for _, job := range jobs {
		card := automationJobCard{
			ID:          strings.TrimSpace(job.ID),
			SessionID:   strings.TrimSpace(job.SessionID),
			Schedule:    map[bool]string{true: "Enabled", false: "Paused"}[job.Enabled],
			RunStatus:   fallbackText(strings.TrimSpace(job.LastRunStatus), "completed"),
			NextRunAt:   formatOptionalTime(job.NextRunAt),
			LastRunAt:   formatOptionalTime(job.LastRunAt),
			UpdatedAt:   formatTime(job.UpdatedAt),
			Timezone:    fallbackText(strings.TrimSpace(job.Timezone), "-"),
			Message:     clipPanelText(strings.TrimSpace(job.Message), 160),
			MessageFull: strings.TrimSpace(job.Message),
			CreatedBy:   fallbackText(strings.TrimSpace(job.CreatedBy), "-"),
			CronExpr:    fallbackText(strings.TrimSpace(job.CronExpr), "-"),
			Enabled:     job.Enabled,
			Tone:        normalizeStatusTone(job.LastRunStatus),
		}
		data.Summary.Total++
		if job.Enabled {
			data.Summary.Enabled++
		} else {
			data.Summary.Paused++
		}
		if card.Tone == "failed" || card.Tone == "danger" {
			data.Summary.Failed++
			data.AttentionJobs = append(data.AttentionJobs, card)
			continue
		}
		if !job.Enabled {
			data.PausedJobs = append(data.PausedJobs, card)
			continue
		}
		data.ScheduledJobs = append(data.ScheduledJobs, card)
	}
	return data
}

func (s *Server) buildFleetPageData(view string) fleetPageData {
	viewTabs := []fleetViewTab{
		{Key: fleetViewNodes, Label: "Nodes", Href: adminRoot + "/fleet?view=nodes", Active: view == fleetViewNodes},
		{Key: fleetViewAgents, Label: "Agents", Href: adminRoot + "/fleet?view=agents", Active: view == fleetViewAgents},
	}
	if len(s.remoteSnapshot()) > 0 {
		viewTabs = append(viewTabs, fleetViewTab{Key: fleetViewRemotes, Label: "Remotes", Href: adminRoot + "/fleet?view=remotes", Active: view == fleetViewRemotes})
	}
	now := time.Now().UTC()
	nodeCards := make([]fleetNodeCard, 0, len(s.nodeSnapshot()))
	for _, node := range s.nodeSnapshot() {
		tone := "good"
		status := "Healthy"
		if nodeIsStale(node, now) {
			tone = "warn"
			status = "Stale"
		}
		nodeCards = append(nodeCards, fleetNodeCard{
			ID:            strings.TrimSpace(node.NodeID),
			Role:          nodeRoleText(node.IsGateway),
			Capabilities:  formatCapabilities(node.Capabilities),
			Agents:        formatStringList(node.Agents),
			Version:       fallbackText(strings.TrimSpace(node.Version), "-"),
			HeartbeatAt:   formatTime(node.LastHeartbeat),
			HeartbeatText: relativeAgeText(node.LastHeartbeat, now),
			Tone:          tone,
			Status:        status,
		})
	}
	sort.Slice(nodeCards, func(i, j int) bool { return nodeCards[i].ID < nodeCards[j].ID })

	agentCards := make([]fleetAgentCard, 0, len(s.agentSnapshot()))
	for _, info := range s.agentSnapshot() {
		agentID := strings.TrimSpace(info.AgentID)
		if agentID == "" {
			continue
		}
		agentCards = append(agentCards, fleetAgentCard{
			ID:                 agentID,
			Name:               fallbackText(strings.TrimSpace(info.Name), agentID),
			Role:               fallbackText(strings.TrimSpace(info.Role), "-"),
			Workspace:          fallbackText(strings.TrimSpace(info.Workspace), "-"),
			Model:              fallbackText(strings.TrimSpace(info.ModelPolicy), "-"),
			EnabledTools:       formatStringList(info.EnabledTools),
			RequiredCaps:       formatStringList(info.RequiredCapabilities),
			Network:            formatNetworkSummary(info.NetworkEnabled, info.AllowDomains, info.BlockDomains),
			Thinking:           boolStateText(info.CaptureThinking),
			Shell:              boolStateText(info.CanShell),
			Patch:              boolStateText(info.ApplyPatchEnabled),
			MaxContextMessages: info.MaxContextMessages,
		})
	}
	sort.Slice(agentCards, func(i, j int) bool { return agentCards[i].ID < agentCards[j].ID })

	remoteCards := make([]fleetRemoteCard, 0, len(s.remoteSnapshot()))
	for _, info := range s.remoteSnapshot() {
		targetID := strings.TrimSpace(info.TargetID)
		if targetID == "" {
			continue
		}
		health := "Degraded"
		if info.Healthy {
			health = "Healthy"
		}
		remoteCards = append(remoteCards, fleetRemoteCard{
			ID:          targetID,
			DisplayName: fallbackText(strings.TrimSpace(info.DisplayName), targetID),
			Description: formatOptionalText(info.Description),
			Endpoint:    formatOptionalText(info.Endpoint),
			Health:      health,
			LastRefresh: formatOptionalText(info.LastRefresh),
			LastError:   formatOptionalText(info.LastError),
		})
	}
	sort.Slice(remoteCards, func(i, j int) bool { return remoteCards[i].ID < remoteCards[j].ID })

	return fleetPageData{
		View:        view,
		ViewTabs:    viewTabs,
		NodeCards:   nodeCards,
		AgentCards:  agentCards,
		RemoteCards: remoteCards,
	}
}

func (s *Server) buildWorkSessionSummaries(ctx context.Context, includeStale bool) ([]workSessionSummary, error) {
	if s.store == nil {
		return nil, nil
	}
	records, err := s.store.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	waitingMap := map[string]string{}
	_, waiting, _, _ := s.loadControlOverview()
	for _, item := range waiting {
		waitingMap[strings.TrimSpace(item.SessionID)] = strings.TrimSpace(item.Reason)
	}

	summaries := make([]workSessionSummary, 0, len(records))
	for _, record := range records {
		if !includeStale && isStaleSessionRecord(record, now) {
			continue
		}
		summary := s.buildWorkSessionSummary(ctx, record, waitingMap[string(record.SessionID)])
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].Priority != summaries[j].Priority {
			return summaries[i].Priority < summaries[j].Priority
		}
		if summaries[i].HasAnomaly != summaries[j].HasAnomaly {
			return summaries[i].HasAnomaly
		}
		return summaries[i].UpdatedAtTime.After(summaries[j].UpdatedAtTime)
	})
	return summaries, nil
}

func (s *Server) buildWorkSessionSummary(ctx context.Context, record sessionrt.SessionRecord, waitingReason string) workSessionSummary {
	title, conversationID := s.resolveSessionTitle(ctx, record.SessionID)
	updatedAt := record.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = record.CreatedAt
	}
	page, _ := s.listSessionEventsBefore(ctx, record.SessionID, 0, 25)
	events := buildTimelineEvents(page.Events)
	latestDigest := "No recent events."
	hasAnomaly := false
	if len(events) > 0 {
		last := events[len(events)-1]
		latestDigest = last.Digest
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Anomaly {
				hasAnomaly = true
				latestDigest = events[i].Digest
				break
			}
		}
	}
	status := sessionStatusText(record.Status)
	priorityLabel, priority := sessionPriority(status, record.InFlight, strings.TrimSpace(waitingReason) != "", hasAnomaly)
	return workSessionSummary{
		SessionID:      string(record.SessionID),
		Title:          title,
		ConversationID: conversationID,
		Status:         status,
		Working:        record.InFlight,
		WaitingOnHuman: strings.TrimSpace(waitingReason) != "",
		WaitingReason:  strings.TrimSpace(waitingReason),
		HasAnomaly:     hasAnomaly,
		LatestDigest:   latestDigest,
		UpdatedAt:      formatTime(updatedAt),
		LastSeq:        record.LastSeq,
		PriorityLabel:  priorityLabel,
		UpdatedAtTime:  updatedAt,
		Priority:       priority,
	}
}

func (s *Server) resolveSessionTitle(ctx context.Context, sessionID sessionrt.SessionID) (string, string) {
	metadata := s.lookupSessionMetadata(sessionID)
	title := metadata.ConversationName
	if title == "" {
		title = s.lookupSessionDisplayName(ctx, sessionID)
	}
	if title == "" {
		title = strings.TrimSpace(metadata.ConversationID)
	}
	if title == "" {
		title = strings.TrimSpace(string(sessionID))
	}
	return title, strings.TrimSpace(metadata.ConversationID)
}

func (s *Server) buildWorkSessionDetail(ctx context.Context, sessionID string, limit int) (workSessionDetailResponse, error) {
	record, err := s.findSessionRecord(ctx, sessionrt.SessionID(sessionID))
	if err != nil {
		return workSessionDetailResponse{}, err
	}
	waitingMap := map[string]string{}
	_, waiting, _, _ := s.loadControlOverview()
	for _, item := range waiting {
		waitingMap[strings.TrimSpace(item.SessionID)] = strings.TrimSpace(item.Reason)
	}
	summary := s.buildWorkSessionSummary(ctx, record, waitingMap[sessionID])
	page, err := s.listSessionEventsBefore(ctx, sessionrt.SessionID(sessionID), 0, limit)
	if err != nil {
		return workSessionDetailResponse{}, err
	}
	timeline := buildTimelineEvents(page.Events)
	counts := map[string]int{}
	latestAnomaly := ""
	for _, event := range timeline {
		counts[event.Category]++
		if event.Anomaly {
			latestAnomaly = event.Digest
		}
	}
	contextEvents, err := s.listSessionEventsBefore(ctx, sessionrt.SessionID(sessionID), 0, timelineStreamLimit)
	if err != nil {
		contextEvents = sessionEventPage{}
	}
	resp := workSessionDetailResponse{
		Session:       summary,
		Story:         buildWorkSessionStory(summary, timeline, latestAnomaly),
		Counts:        counts,
		LatestAnomaly: latestAnomaly,
		ContextHealth: toWorkContextHealth(extractContextHealth(contextEvents.Events)),
		Timeline: workTimelinePageResponse{
			SessionID: sessionID,
			HasOlder:  page.HasMoreBefore,
			Events:    timeline,
		},
	}
	if len(timeline) > 0 {
		resp.Timeline.FirstSeq = timeline[0].Seq
		resp.Timeline.LastSeq = timeline[len(timeline)-1].Seq
	}
	return resp, nil
}

func (s *Server) buildWorkTimelinePage(ctx context.Context, sessionID string, beforeSeq uint64, afterSeq uint64, limit int) (workTimelinePageResponse, error) {
	page := workTimelinePageResponse{SessionID: sessionID}
	var eventsPage sessionEventPage
	var err error
	switch {
	case afterSeq > 0:
		eventsPage, err = s.listSessionEventsAfter(ctx, sessionrt.SessionID(sessionID), afterSeq, limit)
	default:
		eventsPage, err = s.listSessionEventsBefore(ctx, sessionrt.SessionID(sessionID), beforeSeq, limit)
		page.HasOlder = eventsPage.HasMoreBefore
	}
	if err != nil {
		return page, err
	}
	page.Events = buildTimelineEvents(eventsPage.Events)
	if len(page.Events) > 0 {
		page.FirstSeq = page.Events[0].Seq
		page.LastSeq = page.Events[len(page.Events)-1].Seq
	}
	return page, nil
}

func (s *Server) buildWorkEventDetail(ctx context.Context, sessionID string, seq uint64) (workEventDetailResponse, error) {
	if s.store == nil {
		return workEventDetailResponse{}, sessionrt.ErrSessionNotFound
	}
	events, err := s.store.List(ctx, sessionrt.SessionID(sessionID))
	if err != nil {
		return workEventDetailResponse{}, err
	}
	timeline := buildTimelineEvents(events)
	resp := workEventDetailResponse{
		ContextHealth: toWorkContextHealth(extractContextHealth(events)),
	}
	for _, event := range timeline {
		if event.Seq == seq {
			copyEvent := event
			resp.Event = &copyEvent
			return resp, nil
		}
	}
	return workEventDetailResponse{}, sessionrt.ErrSessionNotFound
}

func (s *Server) findSessionRecord(ctx context.Context, sessionID sessionrt.SessionID) (sessionrt.SessionRecord, error) {
	if s.store == nil {
		return sessionrt.SessionRecord{}, sessionrt.ErrSessionNotFound
	}
	records, err := s.store.ListSessions(ctx)
	if err != nil {
		return sessionrt.SessionRecord{}, err
	}
	for _, record := range records {
		if record.SessionID == sessionID {
			return record, nil
		}
	}
	return sessionrt.SessionRecord{}, sessionrt.ErrSessionNotFound
}

func (s *Server) redirectLegacyQueryIfNeeded(w http.ResponseWriter, r *http.Request) bool {
	tab := strings.TrimSpace(r.URL.Query().Get("tab"))
	if tab == "" {
		return false
	}
	target := s.routeForLegacyTab(tab, strings.TrimSpace(r.URL.Query().Get("session")))
	redirect := target
	if raw := s.legacyQueryStringForTarget(target, r.URL.Query()); raw != "" {
		redirect += "?" + raw
	}
	http.Redirect(w, r, redirect, http.StatusPermanentRedirect)
	return true
}

func (s *Server) routeForLegacyTab(tab string, sessionID string) string {
	switch normalizePanelTab(tab) {
	case "sessions":
		if strings.TrimSpace(sessionID) != "" {
			return adminRoot + "/work/" + strings.TrimSpace(sessionID)
		}
		return adminRoot + "/work"
	case "cron":
		return adminRoot + "/automations"
	case "nodes":
		return adminRoot + "/fleet?view=nodes"
	case "agents":
		return adminRoot + "/fleet?view=agents"
	case "actions":
		return adminRoot
	default:
		return adminRoot
	}
}

func (s *Server) legacyQueryStringForTarget(target string, values map[string][]string) string {
	params := urlValuesCloneFromMap(values)
	params.Del("tab")
	params.Del("session")
	if strings.Contains(target, "/fleet?view=") {
		params.Del("view")
	}
	return params.Encode()
}

func buildTimelineEvents(events []sessionrt.Event) []workTimelineEvent {
	rows := make([]workTimelineEvent, 0, len(events))
	pending := make([]pendingTimelineTool, 0, 4)

	for _, event := range events {
		if shouldHideEventInPanel(event.Type) {
			continue
		}
		payload := normalizePanelPayload(event.Payload)
		switch event.Type {
		case sessionrt.EventToolCall:
			callMap := looseMap(payload)
			toolName := toolNameFromPayloadMap(callMap)
			merged := normalizePanelPayload(mergedToolExecutionPayload(toolName, callMap, nil, true))
			row := newTimelineEvent(event, merged)
			rows = append(rows, row)
			pending = append(pending, pendingTimelineTool{
				Index:       len(rows) - 1,
				ToolName:    toolName,
				CallPayload: callMap,
				CallEvent:   event,
			})
		case sessionrt.EventToolResult:
			resultMap := looseMap(payload)
			toolName := toolNameFromPayloadMap(resultMap)
			if pendingIndex := findPendingToolCallIndex(pendingToOldPending(pending), toolName); pendingIndex >= 0 {
				item := pending[pendingIndex]
				pending = append(pending[:pendingIndex], pending[pendingIndex+1:]...)
				resolvedName := coalesceTrimmed(toolName, item.ToolName)
				merged := normalizePanelPayload(mergedToolExecutionPayload(resolvedName, item.CallPayload, resultMap, false))
				row := newTimelineEvent(item.CallEvent, merged)
				if label := toolTypeLabelFromName(resolvedName); label != "" {
					row.TypeLabel = label
				}
				rows[item.Index] = row
				continue
			}
			rows = append(rows, newTimelineEvent(event, payload))
		default:
			rows = append(rows, newTimelineEvent(event, payload))
		}
	}
	return applyTimelineBundleHints(rows)
}

func pendingToOldPending(items []pendingTimelineTool) []pendingToolCallRow {
	out := make([]pendingToolCallRow, 0, len(items))
	for _, item := range items {
		out = append(out, pendingToolCallRow{
			RowIndex:    item.Index,
			ToolName:    item.ToolName,
			CallPayload: item.CallPayload,
		})
	}
	return out
}

func newTimelineEvent(event sessionrt.Event, payload any) workTimelineEvent {
	eventType := strings.TrimSpace(string(event.Type))
	waiting := truthyLooseValue(pickLooseValue(payload, "waiting", "pending", "in_progress"))
	category := timelineEventCategory(eventType, payload)
	anomaly := timelineEventAnomaly(eventType, payload, "")
	presentation := describeTimelineEvent(eventType, payload, category, waiting, anomaly)
	digest := summarizeTimelineEvent(eventType, payload, presentation)
	groupKey, groupLabel := timelineNoiseDescriptor(eventType, payload, presentation)
	return workTimelineEvent{
		Seq:          event.Seq,
		Timestamp:    formatTime(event.Timestamp),
		From:         fallbackText(strings.TrimSpace(string(event.From)), "system"),
		Type:         eventType,
		TypeLabel:    eventTypeLabel(event, eventType),
		Category:     category,
		Digest:       digest,
		Emoji:        presentation.Emoji,
		Title:        presentation.Title,
		Subtitle:     presentation.Subtitle,
		Tone:         presentation.Tone,
		KeyFacts:     timelineKeyFacts(eventType, payload, digest, presentation),
		RawJSON:      prettyPayload(payload),
		Waiting:      waiting,
		Anomaly:      anomaly,
		IsMeaningful: presentation.IsMeaningful,
		ResultStatus: presentation.ResultStatus,
		GroupKey:     groupKey,
		GroupLabel:   groupLabel,
	}
}

func normalizePanelPayload(value any) any {
	if value == nil {
		return map[string]any{}
	}
	blob, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if err := json.Unmarshal(blob, &out); err != nil {
		return value
	}
	if out == nil {
		return map[string]any{}
	}
	return out
}

func looseMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if src, ok := value.(map[string]any); ok && src != nil {
		out := make(map[string]any, len(src))
		for key, item := range src {
			out[key] = item
		}
		return out
	}
	return clonePayloadMap(normalizePanelPayload(value))
}

type timelinePresentation struct {
	Emoji        string
	Title        string
	Subtitle     string
	Tone         string
	ResultStatus string
	IsMeaningful bool
}

func summarizeTimelineEvent(eventType string, payload any, presentation timelinePresentation) string {
	title := clipPanelText(strings.TrimSpace(presentation.Title), 140)
	subtitle := clipPanelText(strings.TrimSpace(presentation.Subtitle), 220)
	if title == "" {
		title = clipPanelText(summarizeLooseValue(payload), 140)
	}
	if subtitle == "" {
		return title
	}
	return title + ": " + subtitle
}

func timelineEventCategory(eventType string, payload any) string {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "message":
		switch strings.ToLower(strings.TrimSpace(pickLooseString(payload, "role", "speaker"))) {
		case "user":
			return "user"
		case "agent":
			return "agent"
		case "system":
			return "control"
		default:
			return "other"
		}
	case "agent_start", "agent_stop", "agent_thinking_delta":
		return "agent"
	case "tool_call", "tool_result":
		return "tools"
	case "control", "state_patch":
		return "control"
	case "error":
		return "errors"
	default:
		return "other"
	}
}

func timelineEventAnomaly(eventType string, payload any, digest string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "error":
		return true
	case "state_patch":
		if retries := strings.TrimSpace(pickLooseString(payload, "overflow_retries")); retries != "" && retries != "0" {
			return true
		}
		if truncations := strings.TrimSpace(pickLooseString(payload, "tool_result_truncation_count")); truncations != "" && truncations != "0" {
			return true
		}
		if stage := strings.TrimSpace(pickLooseString(payload, "overflow_stage")); stage != "" {
			return true
		}
	case "tool_call", "tool_result":
		if timelineToolResultStatus(payload) == "failure" {
			return true
		}
	}
	normalized := strings.ToLower(strings.TrimSpace(digest))
	return strings.Contains(normalized, "error") || strings.Contains(normalized, "failed") || strings.Contains(normalized, "exception") || strings.Contains(normalized, "overflow")
}

func timelineNoiseDescriptor(eventType string, payload any, presentation timelinePresentation) (string, string) {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "tool_call", "tool_result":
		toolName := pickLooseString(payload, "tool_name", "name", "tool", "id")
		if toolName == "" {
			return "", ""
		}
		args := pickLooseValue(payload, "arguments", "args", "input", "params", "payload")
		action := pickLooseString(args, "action", "command", "name")
		key := "tool:" + strings.ToLower(strings.TrimSpace(toolName))
		label := toolName
		if action != "" {
			key += ":" + strings.ToLower(strings.TrimSpace(action))
			label += "/" + action
		}
		return key, label
	case "control":
		action := pickLooseString(payload, "action", "reason", "state", "status")
		if action == "" {
			return "", ""
		}
		return "control:" + strings.ToLower(strings.TrimSpace(action)), "control/" + action
	case "state_patch":
		return "state_patch", "context updates"
	case "agent_thinking_delta":
		if !presentation.IsMeaningful {
			return "agent:thinking", "thinking"
		}
		return "", ""
	default:
		return "", ""
	}
}

func timelineKeyFacts(eventType string, payload any, digest string, presentation timelinePresentation) []string {
	normalized := strings.ToLower(strings.TrimSpace(eventType))
	parts := make([]string, 0, 8)
	switch normalized {
	case "message":
		role := fallbackText(pickLooseString(payload, "role", "speaker"), "message")
		parts = append(parts, "Role: "+role)
		if content := extractLooseText(pickLooseValue(payload, "content", "text", "message")); content != "" {
			parts = append(parts, "Content: "+clipPanelText(content, 120))
		}
		if target := pickLooseString(payload, "target_actor_id"); target != "" {
			parts = append(parts, "Target: "+target)
		}
	case "agent_thinking_delta":
		if delta := extractLooseText(pickLooseValue(payload, "delta", "content", "text", "message")); delta != "" {
			parts = append(parts, "Thinking: "+clipPanelText(delta, 140))
		}
	case "tool_call", "tool_result":
		name := fallbackText(pickLooseString(payload, "tool_name", "name", "tool", "id"), "tool")
		parts = append(parts, "Tool: "+name)
		args := pickLooseValue(payload, "arguments", "args", "input", "params", "payload")
		if action := pickLooseString(args, "action", "command", "name"); action != "" {
			parts = append(parts, "Action: "+clipPanelText(action, 90))
		}
		if path := pickLooseString(args, "path", "file"); path != "" {
			parts = append(parts, "Path: "+clipPanelText(path, 120))
		}
		if command := pickLooseString(args, "cmd", "command"); command != "" {
			parts = append(parts, "Command: "+clipPanelText(command, 120))
		}
		if query := pickLooseString(args, "query", "search_query", "q", "text"); query != "" {
			parts = append(parts, "Query: "+clipPanelText(query, 120))
		}
		if url := pickLooseString(args, "url"); url != "" {
			parts = append(parts, "URL: "+clipPanelText(url, 120))
		}
		if status := timelineToolResultStatusLabel(payload); status != "" {
			parts = append(parts, "Status: "+status)
		}
		if outcome := timelineToolOutcomeSummary(payload); outcome != "" {
			prefix := "Outcome: "
			if presentation.ResultStatus == "failure" {
				prefix = "Error: "
			}
			parts = append(parts, prefix+clipPanelText(outcome, 140))
		}
	case "error":
		message := pickLooseString(payload, "error", "message", "detail", "reason")
		if message == "" {
			message = digest
		}
		parts = append(parts, "Error: "+clipPanelText(message, 120))
	case "control":
		action := fallbackText(pickLooseString(payload, "action", "command", "status", "state"), "event")
		parts = append(parts, "Action: "+action)
		if reason := pickLooseString(payload, "reason", "message", "detail"); reason != "" {
			parts = append(parts, "Reason: "+clipPanelText(reason, 100))
		}
	case "state_patch":
		if model := formatModelDisplay(pickLooseString(payload, "model_id"), pickLooseString(payload, "model_provider")); model != "" {
			parts = append(parts, "Model: "+clipPanelText(model, 120))
		}
		if window := pickLooseString(payload, "model_context_window"); window != "" {
			parts = append(parts, "Window: "+window)
		}
		if reserve := pickLooseString(payload, "reserve_tokens"); reserve != "" {
			parts = append(parts, "Reserve: "+reserve)
		}
		if estimated := pickLooseString(payload, "estimated_input_tokens"); estimated != "" {
			parts = append(parts, "Estimated: "+estimated)
		}
		if retries := pickLooseString(payload, "overflow_retries"); retries != "" {
			parts = append(parts, "Retries: "+retries)
		}
		if stage := pickLooseString(payload, "overflow_stage"); stage != "" {
			parts = append(parts, "Stage: "+stage)
		}
		if truncations := pickLooseString(payload, "tool_result_truncation_count"); truncations != "" {
			parts = append(parts, "Tool truncations: "+truncations)
		}
	case "agent_start", "agent_stop":
		parts = append(parts, "Lifecycle: "+presentation.Title)
	default:
		parts = append(parts, "Summary: "+clipPanelText(digest, 120))
	}
	return parts
}

func describeTimelineEvent(eventType string, payload any, category string, waiting bool, anomaly bool) timelinePresentation {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "message":
		return describeTimelineMessage(payload)
	case "agent_thinking_delta":
		return describeTimelineThinking(payload)
	case "tool_call", "tool_result":
		return describeTimelineTool(payload, waiting)
	case "control":
		return describeTimelineControl(payload, anomaly)
	case "state_patch":
		return describeTimelineStatePatch(payload, anomaly)
	case "error":
		return describeTimelineError(payload)
	case "agent_start":
		return timelinePresentation{Emoji: "▶️", Title: "Agent started", Tone: "agent", IsMeaningful: false}
	case "agent_stop":
		return timelinePresentation{Emoji: "⏹️", Title: "Agent stopped", Tone: "control", IsMeaningful: false}
	default:
		return timelinePresentation{
			Emoji:        "📌",
			Title:        fallbackText(eventType, "event"),
			Subtitle:     clipPanelText(summarizeLooseValue(payload), 220),
			Tone:         normalizeStatusTone(category),
			IsMeaningful: anomaly,
		}
	}
}

func describeTimelineMessage(payload any) timelinePresentation {
	role := strings.ToLower(strings.TrimSpace(pickLooseString(payload, "role", "speaker")))
	content := clipPanelText(extractLooseText(pickLooseValue(payload, "content", "text", "message")), 260)
	if content == "" {
		content = clipPanelText(summarizeLooseValue(payload), 220)
	}
	switch role {
	case "user":
		return timelinePresentation{
			Emoji:        "🧑",
			Title:        "New user ask",
			Subtitle:     content,
			Tone:         "user",
			IsMeaningful: true,
		}
	case "agent":
		return timelinePresentation{
			Emoji:        "🤖",
			Title:        "Agent replied",
			Subtitle:     content,
			Tone:         "agent",
			IsMeaningful: true,
		}
	default:
		return timelinePresentation{
			Emoji:        "🖥️",
			Title:        "System message",
			Subtitle:     content,
			Tone:         "control",
			IsMeaningful: true,
		}
	}
}

func describeTimelineThinking(payload any) timelinePresentation {
	delta := clipPanelText(extractLooseText(pickLooseValue(payload, "delta", "content", "text", "message")), 220)
	if delta == "" {
		delta = clipPanelText(summarizeLooseValue(payload), 180)
	}
	return timelinePresentation{
		Emoji:        "🧠",
		Title:        "Thinking about next step",
		Subtitle:     delta,
		Tone:         "agent",
		IsMeaningful: delta != "" && delta != "{}",
	}
}

func describeTimelineTool(payload any, waiting bool) timelinePresentation {
	toolName := fallbackText(pickLooseString(payload, "tool_name", "name", "tool", "id"), "tool")
	args := pickLooseValue(payload, "arguments", "args", "input", "params", "payload")
	title := timelineToolActionTitle(toolName, args)
	resultStatus := timelineToolResultStatus(payload)
	subtitle := ""
	tone := "tools"
	emoji := "🛠️"
	switch resultStatus {
	case "success":
		emoji = "✅"
		tone = "active"
		outcome := timelineToolOutcomeSummary(payload)
		if outcome != "" {
			subtitle = "Completed successfully. " + clipPanelText(outcome, 200)
		} else {
			subtitle = "Completed successfully."
		}
	case "failure":
		emoji = "❌"
		tone = "danger"
		title += " failed"
		outcome := timelineToolOutcomeSummary(payload)
		if outcome != "" {
			subtitle = clipPanelText(outcome, 200)
		} else {
			subtitle = "The tool returned an error."
		}
	default:
		if waiting {
			subtitle = "Waiting for result."
			tone = "warn"
		} else if params := summarizeToolParams(toolName, args); params != "" {
			subtitle = "Input: " + params
		}
	}
	return timelinePresentation{
		Emoji:        emoji,
		Title:        title,
		Subtitle:     subtitle,
		Tone:         tone,
		ResultStatus: resultStatus,
		IsMeaningful: true,
	}
}

func describeTimelineControl(payload any, anomaly bool) timelinePresentation {
	action := strings.TrimSpace(pickLooseString(payload, "action", "command", "status", "state"))
	reason := clipPanelText(pickLooseString(payload, "reason", "message", "detail"), 180)
	title := "Control update"
	switch action {
	case sessionrt.ControlActionSessionCreated:
		title = "Session created"
	case sessionrt.ControlActionSessionCancelled:
		title = "Session cancelled"
	case sessionrt.ControlActionSessionFailed:
		title = "Session marked failed"
	case sessionrt.ControlActionSessionCompleted:
		title = "Session completed"
	default:
		if action != "" {
			title = humanizeTimelineLabel(action)
		}
	}
	tone := "control"
	if anomaly {
		tone = "danger"
	}
	return timelinePresentation{
		Emoji:        "⚙️",
		Title:        title,
		Subtitle:     reason,
		Tone:         tone,
		IsMeaningful: reason != "" || anomaly || action == sessionrt.ControlActionSessionFailed || action == sessionrt.ControlActionSessionCompleted,
	}
}

func describeTimelineStatePatch(payload any, anomaly bool) timelinePresentation {
	pieces := make([]string, 0, 5)
	if model := formatModelDisplay(pickLooseString(payload, "model_id"), pickLooseString(payload, "model_provider")); model != "" {
		pieces = append(pieces, "Model "+clipPanelText(model, 80))
	}
	if estimated := pickLooseString(payload, "estimated_input_tokens"); estimated != "" {
		pieces = append(pieces, "estimated "+estimated+" tokens")
	}
	if reserve := pickLooseString(payload, "reserve_tokens"); reserve != "" {
		pieces = append(pieces, "reserve "+reserve)
	}
	if retries := pickLooseString(payload, "overflow_retries"); retries != "" && retries != "0" {
		pieces = append(pieces, "overflow retries "+retries)
	}
	if stage := pickLooseString(payload, "overflow_stage"); stage != "" {
		pieces = append(pieces, "stage "+stage)
	}
	if truncations := pickLooseString(payload, "tool_result_truncation_count"); truncations != "" && truncations != "0" {
		pieces = append(pieces, "tool truncations "+truncations)
	}
	tone := "control"
	if anomaly {
		tone = "warn"
	}
	return timelinePresentation{
		Emoji:        "🧩",
		Title:        "Context updated",
		Subtitle:     clipPanelText(strings.Join(pieces, " · "), 220),
		Tone:         tone,
		IsMeaningful: anomaly || len(pieces) > 0,
	}
}

func describeTimelineError(payload any) timelinePresentation {
	message := clipPanelText(pickLooseString(payload, "error", "message", "detail", "reason"), 220)
	if message == "" {
		message = clipPanelText(summarizeLooseValue(payload), 220)
	}
	return timelinePresentation{
		Emoji:        "🚨",
		Title:        "Error reported",
		Subtitle:     message,
		Tone:         "danger",
		ResultStatus: "failure",
		IsMeaningful: true,
	}
}

func timelineToolResultStatus(payload any) string {
	if truthyLooseValue(pickLooseValue(payload, "waiting", "pending", "in_progress")) {
		return "waiting"
	}
	statusPayload := pickLooseValue(payload, "tool_result")
	if statusPayload == nil {
		statusPayload = payload
	}
	status := strings.ToLower(strings.TrimSpace(pickLooseString(statusPayload, "status", "state")))
	if stderr := strings.TrimSpace(summarizeLooseValue(pickLooseValue(statusPayload, "error", "stderr"))); stderr != "" && stderr != "{}" {
		return "failure"
	}
	switch {
	case status == "":
		if pickLooseValue(payload, "tool_result") != nil {
			return "success"
		}
		return ""
	case strings.Contains(status, "fail"), strings.Contains(status, "error"), strings.Contains(status, "timeout"):
		return "failure"
	case strings.Contains(status, "ok"), strings.Contains(status, "success"), strings.Contains(status, "done"), strings.Contains(status, "complete"):
		return "success"
	default:
		return "failure"
	}
}

func timelineToolResultStatusLabel(payload any) string {
	statusPayload := pickLooseValue(payload, "tool_result")
	if statusPayload == nil {
		statusPayload = payload
	}
	status := strings.TrimSpace(pickLooseString(statusPayload, "status", "state"))
	if status != "" {
		return status
	}
	switch timelineToolResultStatus(payload) {
	case "success":
		return "ok"
	case "failure":
		return "failed"
	case "waiting":
		return "waiting"
	default:
		return ""
	}
}

func timelineToolOutcomeSummary(payload any) string {
	statusPayload := pickLooseValue(payload, "tool_result")
	if statusPayload == nil {
		statusPayload = payload
	}
	for _, key := range []string{"error", "stderr", "stdout", "result", "output", "value", "content"} {
		if candidate := pickLooseValue(statusPayload, key); candidate != nil {
			if summary := strings.TrimSpace(summarizeLooseValue(candidate)); summary != "" && summary != "{}" {
				return clipPanelText(summary, 220)
			}
		}
	}
	return ""
}

func timelineToolActionTitle(toolName string, args any) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	switch name {
	case "read":
		if path := pickLooseString(args, "path", "file"); path != "" {
			return "Read " + clipPanelText(path, 96)
		}
		return "Read file"
	case "write":
		if path := pickLooseString(args, "path", "file"); path != "" {
			return "Write " + clipPanelText(path, 96)
		}
		return "Write file"
	case "edit":
		if path := pickLooseString(args, "path", "file"); path != "" {
			return "Edit " + clipPanelText(path, 96)
		}
		return "Edit file"
	case "apply_patch":
		return "Patch files"
	case "exec", "process":
		if command := pickLooseString(args, "cmd", "command"); command != "" {
			return "Run " + clipPanelText(command, 96)
		}
		return "Run command"
	case "web_search", "search_mcp", "search":
		if query := pickLooseString(args, "query", "search_query", "q", "text"); query != "" {
			return "Search \"" + clipPanelText(query, 72) + "\""
		}
		return "Search the web"
	case "web_fetch", "fetch", "fetch_mcp":
		if rawURL := pickLooseString(args, "url"); rawURL != "" {
			return "Fetch " + clipPanelText(rawURL, 96)
		}
		return "Fetch remote content"
	case "delegate":
		return "Delegate work"
	case "heartbeat":
		return "Send heartbeat"
	case "cron":
		return "Schedule automation"
	case "memory":
		return "Review memory"
	default:
		if summary := summarizeToolParams(toolName, args); summary != "" {
			return humanizeTimelineLabel(toolName) + " " + clipPanelText(summary, 72)
		}
		return humanizeTimelineLabel(toolName)
	}
}

func humanizeTimelineLabel(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "_", " "))
	value = strings.TrimSpace(strings.ReplaceAll(value, ".", " "))
	if value == "" {
		return "Event"
	}
	parts := strings.Fields(value)
	for idx, part := range parts {
		if part == "" {
			continue
		}
		parts[idx] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

func applyTimelineBundleHints(rows []workTimelineEvent) []workTimelineEvent {
	turnAnchorSeq := uint64(0)
	for idx := range rows {
		row := &rows[idx]
		switch {
		case row.Category == "user":
			turnAnchorSeq = row.Seq
			row.BundleKind = "turn"
			row.BundleID = fmt.Sprintf("turn-%d", row.Seq)
			row.BundleTitle = "User ask"
		case row.Category == "agent" && row.Type == "message":
			turnAnchorSeq = row.Seq
			row.BundleKind = "turn"
			row.BundleID = fmt.Sprintf("turn-%d", row.Seq)
			row.BundleTitle = "Agent response"
		case row.Category == "agent" && row.Type == "agent_thinking_delta":
			if turnAnchorSeq == 0 {
				turnAnchorSeq = row.Seq
			}
			row.BundleKind = "turn"
			row.BundleID = fmt.Sprintf("turn-%d", turnAnchorSeq)
			row.BundleTitle = "Agent work log"
		case row.Category == "tools":
			if turnAnchorSeq > 0 {
				row.BundleKind = "tools"
				row.BundleID = fmt.Sprintf("turn-%d-tools", turnAnchorSeq)
				row.BundleTitle = "Tool work"
			} else {
				row.BundleKind = "tools"
				row.BundleID = fmt.Sprintf("tools-%d", row.Seq)
				row.BundleTitle = "Tool work"
			}
		case row.Category == "control":
			if row.Type == "state_patch" {
				if turnAnchorSeq > 0 {
					row.BundleKind = "system"
					row.BundleID = fmt.Sprintf("turn-%d-context", turnAnchorSeq)
					row.BundleTitle = "Context and control"
				} else {
					row.BundleKind = "system"
					row.BundleID = fmt.Sprintf("system-%d", row.Seq)
					row.BundleTitle = "Context and control"
				}
				continue
			}
			row.BundleKind = "system"
			row.BundleID = fmt.Sprintf("system-%d", row.Seq)
			row.BundleTitle = "Control milestones"
		case row.Category == "errors":
			row.BundleKind = "anomaly"
			row.BundleID = fmt.Sprintf("anomaly-%d", row.Seq)
			row.BundleTitle = "Failures"
		default:
			if !row.IsMeaningful {
				row.BundleKind = "system"
				row.BundleID = fmt.Sprintf("system-%d", row.Seq)
				row.BundleTitle = "Background events"
			}
		}
	}
	return rows
}

func buildWorkSessionStory(summary workSessionSummary, timeline []workTimelineEvent, latestAnomaly string) workSessionStory {
	story := workSessionStory{
		CurrentState:  strings.TrimSpace(summary.PriorityLabel),
		LatestAnomaly: strings.TrimSpace(latestAnomaly),
	}
	switch summary.Status {
	case "failed":
		story.CurrentStateDetail = "The session is in a failed state."
	case "paused":
		story.CurrentStateDetail = "The session is paused."
	case "completed":
		story.CurrentStateDetail = "The session completed."
	default:
		if strings.TrimSpace(summary.WaitingReason) != "" {
			story.CurrentStateDetail = strings.TrimSpace(summary.WaitingReason)
		} else if summary.Working {
			story.CurrentStateDetail = "The agent is actively working."
		}
	}
	for idx := len(timeline) - 1; idx >= 0; idx-- {
		event := timeline[idx]
		switch {
		case story.Goal == "" && event.Category == "user" && strings.TrimSpace(event.Subtitle) != "":
			story.Goal = event.Subtitle
		case story.LatestConclusion == "" && event.Category == "agent" && event.Type == "message":
			story.LatestConclusion = firstNonEmpty(event.Subtitle, event.Title)
		case story.LastMeaningfulStep == "" && event.IsMeaningful && event.Category != "user":
			story.LastMeaningfulStep = firstNonEmpty(event.Digest, event.Title)
		case story.LatestAnomaly == "" && event.Anomaly:
			story.LatestAnomaly = firstNonEmpty(event.Digest, event.Title)
		}
		if story.Goal != "" && story.LatestConclusion != "" && story.LastMeaningfulStep != "" && story.LatestAnomaly != "" {
			break
		}
	}
	return story
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func pickLooseValue(value any, keys ...string) any {
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil
	}
	for _, key := range keys {
		if candidate, exists := obj[key]; exists {
			return candidate
		}
	}
	return nil
}

func pickLooseString(value any, keys ...string) string {
	raw := pickLooseValue(value, keys...)
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func summarizeLooseValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "{}"
	case string:
		return strings.TrimSpace(typed)
	case []any:
		if len(typed) == 0 {
			return "[]"
		}
		return fmt.Sprintf("list(%d) %s", len(typed), clipPanelText(summarizeLooseValue(typed[0]), 180))
	case map[string]any:
		if content := extractLooseText(pickLooseValue(typed, "content", "text", "message")); content != "" {
			return content
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return "{}"
		}
		return strings.Join(keys[:minInt(4, len(keys))], ", ")
	default:
		blob, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprintf("%v", typed)
		}
		return string(blob)
	}
}

func extractLooseText(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := extractLooseText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		if text := pickLooseString(typed, "text", "content", "message"); text != "" {
			return text
		}
	}
	return ""
}

func truthyLooseValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return parseTruthy(typed)
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return false
	}
}

func summarizeToolParams(toolName string, toolInput any) string {
	normalizedTool := strings.ToLower(strings.TrimSpace(toolName))
	if normalizedTool == "web_search" || normalizedTool == "search_mcp" || normalizedTool == "search" || normalizedTool == "web_fetch" || normalizedTool == "fetch" || normalizedTool == "fetch_mcp" {
		if query := pickLooseString(toolInput, "query", "search_query", "q", "text"); query != "" {
			return "query \"" + clipPanelText(query, 120) + "\""
		}
		if url := pickLooseString(toolInput, "url"); url != "" {
			return "url " + clipPanelText(url, 120)
		}
	}
	for _, key := range []string{"path", "file"} {
		if path := pickLooseString(toolInput, key); path != "" {
			return "file " + clipPanelText(path, 140)
		}
	}
	if normalizedTool == "exec" || normalizedTool == "process" {
		if command := pickLooseString(toolInput, "cmd", "command"); command != "" {
			return "command \"" + clipPanelText(command, 120) + "\""
		}
	}
	return ""
}

func toWorkContextHealth(value *contextHealthData) *workContextHealth {
	if value == nil {
		return nil
	}
	return &workContextHealth{
		ModelDisplay:         value.ModelDisplay,
		ModelContextWindow:   value.ModelContextWindow,
		ReserveTokens:        value.ReserveTokens,
		EstimatedInputTokens: value.EstimatedInputTokens,
		OverflowRetries:      value.OverflowRetries,
		OverflowStage:        value.OverflowStage,
		SummaryStrategy:      value.SummaryStrategy,
		ToolTruncation:       value.ToolResultTruncation,
		RecentMessages:       fmt.Sprintf("%d/%d", value.RecentMessagesUsedTokens, value.RecentMessagesCapTokens),
		Memory:               fmt.Sprintf("%d/%d", value.MemoryUsedTokens, value.MemoryCapTokens),
		Compaction:           fmt.Sprintf("%d/%d", value.CompactionUsedTokens, value.CompactionCapTokens),
	}
}

func sessionPriority(status string, working bool, waiting bool, anomaly bool) (string, int) {
	switch {
	case status == "failed":
		return "Failed", 0
	case waiting:
		return "Waiting", 1
	case anomaly:
		return "Needs review", 2
	case working:
		return "Running", 3
	case status == "active":
		return "Active", 4
	case status == "paused":
		return "Paused", 5
	case status == "completed":
		return "Completed", 6
	default:
		return "Unknown", 7
	}
}

func nodeIsStale(node scheduler.NodeInfo, now time.Time) bool {
	if node.LastHeartbeat.IsZero() {
		return true
	}
	return now.Sub(node.LastHeartbeat) > nodeStaleAfter
}

func relativeAgeText(value time.Time, now time.Time) string {
	if value.IsZero() {
		return "No heartbeat"
	}
	diff := now.Sub(value)
	switch {
	case diff < time.Minute:
		return "Updated just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	}
}

func normalizeFleetView(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case fleetViewAgents:
		return fleetViewAgents
	case fleetViewRemotes:
		return fleetViewRemotes
	default:
		return fleetViewNodes
	}
}

func normalizeWorkFilter(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user", "agent", "tools", "control", "errors":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "all"
	}
}

func normalizeWorkView(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "raw":
		return "raw"
	case "stream":
		return "stream"
	case "digest", "narrative":
		return "narrative"
	default:
		return "narrative"
	}
}

func normalizeWorkNoise(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == "all" {
		return "all"
	}
	return "grouped"
}

func normalizeStatusTone(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "failed", "error", "errors", "danger":
		return "danger"
	case "active", "healthy", "enabled", "success", "completed", "ok":
		return "active"
	case "paused", "warning", "warn":
		return "warn"
	case "message":
		return "message"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func fallbackText(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func optionalTimeValue(value *time.Time) time.Time {
	if value == nil || value.IsZero() {
		return time.Unix(1<<62, 0).UTC()
	}
	return value.UTC()
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}

func errorsIsNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not found")
}

func urlValuesCloneFromMap(values map[string][]string) url.Values {
	out := url.Values{}
	for key, items := range values {
		copied := make([]string, len(items))
		copy(copied, items)
		out[key] = copied
	}
	return out
}
