package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type fakeSessionStore struct {
	mu      sync.RWMutex
	events  map[sessionrt.SessionID][]sessionrt.Event
	records map[sessionrt.SessionID]sessionrt.SessionRecord
	stream  map[sessionrt.SessionID]chan sessionrt.Event
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{
		events:  map[sessionrt.SessionID][]sessionrt.Event{},
		records: map[sessionrt.SessionID]sessionrt.SessionRecord{},
		stream:  map[sessionrt.SessionID]chan sessionrt.Event{},
	}
}

func (s *fakeSessionStore) addSession(sessionID sessionrt.SessionID, status sessionrt.SessionStatus, events []sessionrt.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := make([]sessionrt.Event, len(events))
	copy(copied, events)
	s.events[sessionID] = copied
	updatedAt := time.Now().UTC()
	lastSeq := uint64(0)
	if len(copied) > 0 {
		updatedAt = copied[len(copied)-1].Timestamp
		lastSeq = copied[len(copied)-1].Seq
	}
	s.records[sessionID] = sessionrt.SessionRecord{
		SessionID: sessionID,
		Status:    status,
		CreatedAt: updatedAt,
		UpdatedAt: updatedAt,
		LastSeq:   lastSeq,
	}
}

func (s *fakeSessionStore) setInFlight(sessionID sessionrt.SessionID, inFlight bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[sessionID]
	if !ok {
		return
	}
	record.InFlight = inFlight
	s.records[sessionID] = record
}

func (s *fakeSessionStore) push(sessionID sessionrt.SessionID, event sessionrt.Event) {
	s.mu.RLock()
	ch := s.stream[sessionID]
	s.mu.RUnlock()
	if ch == nil {
		return
	}
	ch <- event
}

func (s *fakeSessionStore) waitForStream(sessionID sessionrt.SessionID, timeout time.Duration) bool {
	deadline := time.After(timeout)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			return false
		case <-tick.C:
			s.mu.RLock()
			_, ok := s.stream[sessionID]
			s.mu.RUnlock()
			if ok {
				return true
			}
		}
	}
}

func (s *fakeSessionStore) List(_ context.Context, sessionID sessionrt.SessionID) ([]sessionrt.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events, ok := s.events[sessionID]
	if !ok {
		return nil, sessionrt.ErrSessionNotFound
	}
	out := make([]sessionrt.Event, len(events))
	copy(out, events)
	return out, nil
}

func (s *fakeSessionStore) ListBefore(_ context.Context, sessionID sessionrt.SessionID, beforeSeq uint64, limit int) ([]sessionrt.Event, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events, ok := s.events[sessionID]
	if !ok {
		return nil, false, sessionrt.ErrSessionNotFound
	}
	end := len(events)
	if beforeSeq > 0 {
		for i, event := range events {
			if event.Seq >= beforeSeq {
				end = i
				break
			}
		}
	}
	if limit <= 0 || end <= limit {
		out := append([]sessionrt.Event(nil), events[:end]...)
		return out, false, nil
	}
	start := end - limit
	out := append([]sessionrt.Event(nil), events[start:end]...)
	return out, start > 0, nil
}

func (s *fakeSessionStore) ListAfter(_ context.Context, sessionID sessionrt.SessionID, afterSeq uint64, limit int) ([]sessionrt.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	events, ok := s.events[sessionID]
	if !ok {
		return nil, sessionrt.ErrSessionNotFound
	}
	start := len(events)
	for i, event := range events {
		if event.Seq > afterSeq {
			start = i
			break
		}
	}
	if start >= len(events) {
		return nil, nil
	}
	out := append([]sessionrt.Event(nil), events[start:]...)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeSessionStore) Stream(_ context.Context, sessionID sessionrt.SessionID) (<-chan sessionrt.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.events[sessionID]; !ok {
		return nil, sessionrt.ErrSessionNotFound
	}
	if ch, ok := s.stream[sessionID]; ok {
		return ch, nil
	}
	ch := make(chan sessionrt.Event, 16)
	s.stream[sessionID] = ch
	return ch, nil
}

func (s *fakeSessionStore) ListSessions(_ context.Context) ([]sessionrt.SessionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	records := make([]sessionrt.SessionRecord, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].SessionID < records[j].SessionID
	})
	return records, nil
}

func TestPanelMainPageRenders(t *testing.T) {
	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329"})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Gopher Control Panel") {
		t.Fatalf("expected page heading, got: %s", rec.Body.String())
	}
}

func TestPanelPathTabRendersActiveTab(t *testing.T) {
	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329"})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/tab/sessions", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data-initial-tab=\"sessions\"") {
		t.Fatalf("expected sessions tab in body data marker, got: %s", rec.Body.String())
	}
}

func TestPanelQueryTabNormalizesActiveTab(t *testing.T) {
	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329"})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel?tab=control-actions", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data-initial-tab=\"actions\"") {
		t.Fatalf("expected actions tab in body data marker, got: %s", rec.Body.String())
	}
}

func TestPanelPathCronTabRendersActiveTab(t *testing.T) {
	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329"})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/tab/cron", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "data-initial-tab=\"cron\"") {
		t.Fatalf("expected cron tab in body data marker, got: %s", rec.Body.String())
	}
}

func TestPanelLimitedModeSessionsFragment(t *testing.T) {
	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329"})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/sessions", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Session runtime is unavailable") {
		t.Fatalf("expected limited mode message, got: %s", rec.Body.String())
	}
}

func TestPanelNodesEndpointReturnsSnapshot(t *testing.T) {
	srv, err := NewServer(ServerOptions{
		ListenAddr: "127.0.0.1:29329",
		NodeSnapshot: func() []scheduler.NodeInfo {
			return []scheduler.NodeInfo{
				{
					NodeID:    "gateway",
					IsGateway: true,
					Capabilities: []scheduler.Capability{
						{Kind: scheduler.CapabilitySystem, Name: "router"},
					},
					LastHeartbeat: time.Unix(1700000000, 0).UTC(),
				},
				{
					NodeID:    "node-1",
					IsGateway: false,
					Capabilities: []scheduler.Capability{
						{Kind: scheduler.CapabilityTool, Name: "web_search"},
					},
					LastHeartbeat: time.Unix(1700000060, 0).UTC(),
				},
			}
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/nodes", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var payload struct {
		Nodes []scheduler.NodeInfo `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Nodes) != 2 {
		t.Fatalf("node count = %d, want 2", len(payload.Nodes))
	}
	if payload.Nodes[0].NodeID != "gateway" {
		t.Fatalf("first node id = %q, want gateway", payload.Nodes[0].NodeID)
	}
	if payload.Nodes[1].NodeID != "node-1" {
		t.Fatalf("second node id = %q, want node-1", payload.Nodes[1].NodeID)
	}
}

func TestPanelSessionFragmentsRender(t *testing.T) {
	store := newFakeSessionStore()
	now := time.Now().UTC()
	store.addSession("sess-1", sessionrt.SessionActive, []sessionrt.Event{
		{SessionID: "sess-1", Seq: 1, Type: sessionrt.EventMessage, From: "user:1", Payload: sessionrt.Message{Role: sessionrt.RoleUser, Content: "hi"}, Timestamp: now},
		{SessionID: "sess-1", Seq: 2, Type: sessionrt.EventMessage, From: "agent:a", Payload: sessionrt.Message{Role: sessionrt.RoleAgent, Content: "hello"}, Timestamp: now.Add(time.Second)},
		{SessionID: "sess-1", Seq: 3, Type: sessionrt.EventToolCall, From: "agent:a", Payload: map[string]any{"name": "read"}, Timestamp: now.Add(2 * time.Second)},
		{SessionID: "sess-1", Seq: 4, Type: sessionrt.EventAgentDelta, From: "agent:a", Payload: map[string]any{"delta": "thinking"}, Timestamp: now.Add(3 * time.Second)},
		{SessionID: "sess-1", Seq: 5, Type: sessionrt.EventStatePatch, From: "agent:a", Payload: map[string]any{
			"model_id":                     "gpt-5-codex",
			"model_provider":               "openai",
			"model_context_window":         128000,
			"reserve_tokens":               20000,
			"reserve_floor_tokens":         20000,
			"estimated_input_tokens":       44120,
			"overflow_retries":             2,
			"overflow_stage":               "retry_2",
			"summary_strategy":             "model_assisted",
			"tool_result_truncation_count": 3,
			"recent_messages_used_tokens":  12000,
			"recent_messages_cap_tokens":   30000,
			"retrieved_memory_used_tokens": 1300,
			"retrieved_memory_cap_tokens":  5000,
			"compaction_used_tokens":       900,
			"compaction_cap_tokens":        3600,
		}, Timestamp: now.Add(4 * time.Second)},
	})
	store.setInFlight("sess-1", true)
	srv, err := NewServer(ServerOptions{
		ListenAddr: "127.0.0.1:29329",
		Store:      store,
		SessionMetadata: func(sessionID sessionrt.SessionID) (SessionMetadata, bool) {
			if sessionID != "sess-1" {
				return SessionMetadata{}, false
			}
			return SessionMetadata{
				ConversationID:   "!room:1",
				ConversationName: "Writer Room",
			}, true
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	mux := srv.newMux()

	sessionsRec := httptest.NewRecorder()
	sessionsReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/sessions", nil)
	mux.ServeHTTP(sessionsRec, sessionsReq)
	if sessionsRec.Code != http.StatusOK {
		t.Fatalf("sessions status = %d, want 200", sessionsRec.Code)
	}
	if !strings.Contains(sessionsRec.Body.String(), "Writer Room") {
		t.Fatalf("expected room name in sessions fragment, got: %s", sessionsRec.Body.String())
	}
	if !strings.Contains(sessionsRec.Body.String(), "!room:1") {
		t.Fatalf("expected room id fallback metadata in sessions fragment, got: %s", sessionsRec.Body.String())
	}
	if !strings.Contains(sessionsRec.Body.String(), "session-working") {
		t.Fatalf("expected working indicator in sessions fragment, got: %s", sessionsRec.Body.String())
	}
	if !strings.Contains(sessionsRec.Body.String(), "data-session-working=\"true\"") {
		t.Fatalf("expected data-session-working marker in sessions fragment, got: %s", sessionsRec.Body.String())
	}
	if !strings.Contains(sessionsRec.Body.String(), "data-updated-at=") {
		t.Fatalf("expected machine-readable updated timestamp in sessions fragment, got: %s", sessionsRec.Body.String())
	}

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/session/sess-1", nil)
	mux.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", detailRec.Code)
	}
	if !strings.Contains(detailRec.Body.String(), ">read</span>") {
		t.Fatalf("expected built-in tool label in timeline, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "waiting for result") {
		t.Fatalf("expected pending tool indicator in timeline, got: %s", detailRec.Body.String())
	}
	if strings.Contains(detailRec.Body.String(), "agent_delta") {
		t.Fatalf("expected delta events to be hidden in timeline, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "badge-message-user") || !strings.Contains(detailRec.Body.String(), ">USER<") {
		t.Fatalf("expected USER badge in detail fragment, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "badge-message-agent") || !strings.Contains(detailRec.Body.String(), ">AGENT<") {
		t.Fatalf("expected AGENT badge in detail fragment, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "Writer Room") {
		t.Fatalf("expected room name in detail fragment, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "Context Health") {
		t.Fatalf("expected context health block in detail fragment, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "Model gpt-5-codex (openai)") {
		t.Fatalf("expected model in context health block, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "Stage retry_2") {
		t.Fatalf("expected overflow stage in context health block, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "data-event-keyfacts") {
		t.Fatalf("expected key facts container in detail fragment, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "data-payload-details") {
		t.Fatalf("expected collapsed payload details in detail fragment, got: %s", detailRec.Body.String())
	}
}

func TestPanelSessionsHideStaleByDefault(t *testing.T) {
	store := newFakeSessionStore()
	now := time.Now().UTC()
	store.addSession("sess-fresh", sessionrt.SessionActive, []sessionrt.Event{
		{SessionID: "sess-fresh", Seq: 1, Type: sessionrt.EventMessage, Timestamp: now},
	})
	store.addSession("sess-stale", sessionrt.SessionActive, []sessionrt.Event{
		{SessionID: "sess-stale", Seq: 1, Type: sessionrt.EventMessage, Timestamp: now.Add(-48 * time.Hour)},
	})
	store.mu.Lock()
	staleRecord := store.records["sess-stale"]
	staleRecord.CreatedAt = now.Add(-72 * time.Hour)
	staleRecord.UpdatedAt = now.Add(-48 * time.Hour)
	store.records["sess-stale"] = staleRecord
	store.mu.Unlock()

	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329", Store: store})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	mux := srv.newMux()

	defaultRec := httptest.NewRecorder()
	defaultReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/sessions", nil)
	mux.ServeHTTP(defaultRec, defaultReq)
	if defaultRec.Code != http.StatusOK {
		t.Fatalf("default sessions status = %d, want 200", defaultRec.Code)
	}
	body := defaultRec.Body.String()
	if !strings.Contains(body, "sess-fresh") {
		t.Fatalf("expected fresh session in default list, got: %s", body)
	}
	if strings.Contains(body, "sess-stale") {
		t.Fatalf("expected stale session to be hidden by default, got: %s", body)
	}

	includeRec := httptest.NewRecorder()
	includeReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/sessions?include_stale=true", nil)
	mux.ServeHTTP(includeRec, includeReq)
	if includeRec.Code != http.StatusOK {
		t.Fatalf("include stale sessions status = %d, want 200", includeRec.Code)
	}
	if !strings.Contains(includeRec.Body.String(), "sess-stale") {
		t.Fatalf("expected stale session when include_stale=true, got: %s", includeRec.Body.String())
	}
}

func TestPanelSessionsUseSessionDisplayNameFallback(t *testing.T) {
	store := newFakeSessionStore()
	now := time.Now().UTC()
	store.addSession("sess-name", sessionrt.SessionActive, []sessionrt.Event{
		{
			SessionID: "sess-name",
			Seq:       1,
			Type:      sessionrt.EventControl,
			Timestamp: now,
			Payload: sessionrt.ControlPayload{
				Action: sessionrt.ControlActionSessionCreated,
				Metadata: map[string]any{
					"display_name": "Weekly Planning",
				},
			},
		},
	})

	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329", Store: store})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/session/sess-name", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Weekly Planning") {
		t.Fatalf("expected display_name fallback in detail view, got: %s", rec.Body.String())
	}
}

func TestPanelSessionDetailLoadsNewestWindow(t *testing.T) {
	store := newFakeSessionStore()
	now := time.Now().UTC()
	events := make([]sessionrt.Event, 0, 15)
	for i := 1; i <= 15; i++ {
		events = append(events, sessionrt.Event{
			SessionID: "sess-window",
			Seq:       uint64(i),
			Type:      sessionrt.EventMessage,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleUser,
				Content: fmt.Sprintf("message %d", i),
			},
		})
	}
	store.addSession("sess-window", sessionrt.SessionActive, events)

	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329", Store: store})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/session/sess-window", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if got := strings.Count(body, "data-event-row"); got != 10 {
		t.Fatalf("rendered rows = %d, want 10", got)
	}
	if !strings.Contains(body, "data-first-seq=\"6\"") {
		t.Fatalf("expected first seq 6 in detail fragment, got: %s", body)
	}
	if !strings.Contains(body, "data-last-seq=\"15\"") {
		t.Fatalf("expected last seq 15 in detail fragment, got: %s", body)
	}
	if !strings.Contains(body, "data-has-older=\"true\"") {
		t.Fatalf("expected older rows marker in detail fragment, got: %s", body)
	}
	if strings.Contains(body, "message 5") {
		t.Fatalf("expected older message outside newest window to be omitted, got: %s", body)
	}
	if !strings.Contains(body, "message 6") || !strings.Contains(body, "message 15") {
		t.Fatalf("expected newest window contents in detail fragment, got: %s", body)
	}
}

func TestPanelSessionEventRowsEndpointPagesBeforeAndAfter(t *testing.T) {
	store := newFakeSessionStore()
	now := time.Now().UTC()
	events := make([]sessionrt.Event, 0, 15)
	for i := 1; i <= 15; i++ {
		events = append(events, sessionrt.Event{
			SessionID: "sess-window",
			Seq:       uint64(i),
			Type:      sessionrt.EventMessage,
			Timestamp: now.Add(time.Duration(i) * time.Second),
			Payload: sessionrt.Message{
				Role:    sessionrt.RoleUser,
				Content: fmt.Sprintf("message %d", i),
			},
		})
	}
	store.addSession("sess-window", sessionrt.SessionActive, events)

	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329", Store: store})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	beforeRec := httptest.NewRecorder()
	beforeReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/session/sess-window/events?before_seq=6&limit=3", nil)
	srv.newMux().ServeHTTP(beforeRec, beforeReq)
	if beforeRec.Code != http.StatusOK {
		t.Fatalf("before status = %d, want 200", beforeRec.Code)
	}
	beforeBody := beforeRec.Body.String()
	if !strings.Contains(beforeBody, "data-first-seq=\"3\"") || !strings.Contains(beforeBody, "data-last-seq=\"5\"") {
		t.Fatalf("expected paged older window metadata, got: %s", beforeBody)
	}
	if !strings.Contains(beforeBody, "data-has-older=\"true\"") {
		t.Fatalf("expected additional older rows marker, got: %s", beforeBody)
	}
	if got := strings.Count(beforeBody, "data-event-row"); got != 3 {
		t.Fatalf("older rows rendered = %d, want 3", got)
	}

	afterRec := httptest.NewRecorder()
	afterReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/session/sess-window/events?after_seq=12", nil)
	srv.newMux().ServeHTTP(afterRec, afterReq)
	if afterRec.Code != http.StatusOK {
		t.Fatalf("after status = %d, want 200", afterRec.Code)
	}
	afterBody := afterRec.Body.String()
	if !strings.Contains(afterBody, "data-first-seq=\"13\"") || !strings.Contains(afterBody, "data-last-seq=\"15\"") {
		t.Fatalf("expected appended newer window metadata, got: %s", afterBody)
	}
	if got := strings.Count(afterBody, "data-event-row"); got != 3 {
		t.Fatalf("newer rows rendered = %d, want 3", got)
	}
}

func TestToEventRowsFiltersDeltasAndLabelsBuiltInTools(t *testing.T) {
	now := time.Now().UTC()
	rows := toEventRows([]sessionrt.Event{
		{Seq: 1, Type: sessionrt.EventAgentDelta, Timestamp: now},
		{Seq: 2, Type: sessionrt.EventToolCall, Payload: map[string]any{"name": "web_search"}, Timestamp: now.Add(time.Second)},
		{Seq: 3, Type: sessionrt.EventToolCall, Payload: map[string]any{"name": "external_tool"}, Timestamp: now.Add(2 * time.Second)},
	})
	if len(rows) != 2 {
		t.Fatalf("row count = %d, want 2", len(rows))
	}
	if rows[0].Type != "tool_call" {
		t.Fatalf("rows[0].Type = %q, want tool_call", rows[0].Type)
	}
	if rows[0].TypeLabel != "web_search" {
		t.Fatalf("rows[0].TypeLabel = %q, want web_search", rows[0].TypeLabel)
	}
	if !rows[0].Waiting {
		t.Fatalf("rows[0].Waiting = false, want true")
	}
	if rows[1].TypeLabel != "tool_call" {
		t.Fatalf("rows[1].TypeLabel = %q, want tool_call", rows[1].TypeLabel)
	}
	if !rows[1].Waiting {
		t.Fatalf("rows[1].Waiting = false, want true")
	}
}

func TestToEventRowsMergesToolCallAndResultIntoSingleRow(t *testing.T) {
	now := time.Now().UTC()
	rows := toEventRows([]sessionrt.Event{
		{Seq: 1, Type: sessionrt.EventToolCall, Payload: map[string]any{"name": "read", "args": map[string]any{"path": "/tmp/file"}}, Timestamp: now},
		{Seq: 2, Type: sessionrt.EventToolResult, Payload: map[string]any{"name": "read", "status": "ok", "result": map[string]any{"content": "hello"}}, Timestamp: now.Add(time.Second)},
	})
	if len(rows) != 1 {
		t.Fatalf("row count = %d, want 1", len(rows))
	}
	if rows[0].Type != "tool_call" {
		t.Fatalf("rows[0].Type = %q, want tool_call", rows[0].Type)
	}
	if rows[0].TypeLabel != "read" {
		t.Fatalf("rows[0].TypeLabel = %q, want read", rows[0].TypeLabel)
	}
	if rows[0].Waiting {
		t.Fatalf("rows[0].Waiting = true, want false")
	}
	if rows[0].BadgeClass != "badge-tool-result" {
		t.Fatalf("rows[0].BadgeClass = %q, want badge-tool-result", rows[0].BadgeClass)
	}
	if !strings.Contains(rows[0].Payload, `"tool_call"`) {
		t.Fatalf("rows[0].Payload missing tool_call, got: %s", rows[0].Payload)
	}
	if !strings.Contains(rows[0].Payload, `"tool_result"`) {
		t.Fatalf("rows[0].Payload missing tool_result, got: %s", rows[0].Payload)
	}
}

func TestPanelAgentsFragmentRender(t *testing.T) {
	srv, err := NewServer(ServerOptions{
		ListenAddr: "127.0.0.1:29329",
		AgentSnapshot: func() []AgentInfo {
			return []AgentInfo{
				{
					AgentID:              "main",
					Name:                 "Main Agent",
					Role:                 "triage",
					Workspace:            "/tmp/workspace/agents/main",
					ModelPolicy:          "openai:gpt-5.3-codex-spark",
					RequiredCapabilities: []string{"tool:web_search"},
					EnabledTools:         []string{"exec_command", "apply_patch"},
					SkillsPaths:          []string{"/tmp/workspace/skills"},
					KnownAgents:          []string{"main", "ops"},
					FSRoots:              []string{"/tmp/workspace"},
					AllowDomains:         []string{"api.github.com"},
					BlockDomains:         []string{"example.com"},
					CanShell:             true,
					ApplyPatchEnabled:    true,
					CaptureThinking:      true,
					NetworkEnabled:       true,
					MaxContextMessages:   40,
				},
			}
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/agents", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Main Agent") {
		t.Fatalf("expected agent name, got: %s", body)
	}
	if !strings.Contains(body, "openai:gpt-5.3-codex-spark") {
		t.Fatalf("expected model policy, got: %s", body)
	}
	if !strings.Contains(body, "allow: api.github.com") {
		t.Fatalf("expected network summary, got: %s", body)
	}
}

func TestPanelControlAndNodesFragmentsRender(t *testing.T) {
	srv, err := NewServer(ServerOptions{
		ListenAddr: "127.0.0.1:29329",
		NodeSnapshot: func() []scheduler.NodeInfo {
			return []scheduler.NodeInfo{
				{
					NodeID:    "gateway",
					IsGateway: true,
					Capabilities: []scheduler.Capability{
						{Kind: scheduler.CapabilityAgent, Name: "agent"},
					},
					LastHeartbeat: time.Unix(1700000000, 0).UTC(),
				},
			}
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	mux := srv.newMux()

	controlRec := httptest.NewRecorder()
	controlReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/control", nil)
	mux.ServeHTTP(controlRec, controlReq)
	if controlRec.Code != http.StatusOK {
		t.Fatalf("control status = %d, want 200", controlRec.Code)
	}
	if !strings.Contains(controlRec.Body.String(), "Control directory unavailable") {
		t.Fatalf("expected control unavailable message, got: %s", controlRec.Body.String())
	}

	nodesRec := httptest.NewRecorder()
	nodesReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/nodes-table", nil)
	mux.ServeHTTP(nodesRec, nodesReq)
	if nodesRec.Code != http.StatusOK {
		t.Fatalf("nodes status = %d, want 200", nodesRec.Code)
	}
	if !strings.Contains(nodesRec.Body.String(), "gateway") {
		t.Fatalf("expected gateway in nodes fragment, got: %s", nodesRec.Body.String())
	}

	actionsRec := httptest.NewRecorder()
	actionsReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/control-actions", nil)
	mux.ServeHTTP(actionsRec, actionsReq)
	if actionsRec.Code != http.StatusOK {
		t.Fatalf("actions status = %d, want 200", actionsRec.Code)
	}
	if !strings.Contains(actionsRec.Body.String(), "Control directory unavailable") {
		t.Fatalf("expected actions unavailable message, got: %s", actionsRec.Body.String())
	}

	cronRec := httptest.NewRecorder()
	cronReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/cron", nil)
	mux.ServeHTTP(cronRec, cronReq)
	if cronRec.Code != http.StatusOK {
		t.Fatalf("cron status = %d, want 200", cronRec.Code)
	}
	if !strings.Contains(cronRec.Body.String(), "Cron storage unavailable") {
		t.Fatalf("expected cron unavailable message, got: %s", cronRec.Body.String())
	}
}

func TestPanelCronFragmentRendersJobs(t *testing.T) {
	tempDir := t.TempDir()
	cronStorePath := filepath.Join(tempDir, "cron", "jobs.json")
	if err := os.MkdirAll(filepath.Dir(cronStorePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error: %v", err)
	}

	blob := `{
  "jobs": [
    {
      "id": "cron-1",
      "session_id": "sess-1",
      "message": "daily check-in",
      "cron_expr": "0 9 * * *",
      "timezone": "UTC",
      "enabled": true,
      "created_by": "agent",
      "updated_at": "2026-03-05T00:00:00Z",
      "next_run_at": "2026-03-06T09:00:00Z"
    }
  ]
}`
	if err := os.WriteFile(cronStorePath, []byte(blob), 0o644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	srv, err := NewServer(ServerOptions{
		ListenAddr:    "127.0.0.1:29329",
		CronStorePath: cronStorePath,
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/cron", nil)
	srv.newMux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "cron-1") {
		t.Fatalf("expected cron id, got: %s", body)
	}
	if !strings.Contains(body, "daily check-in") {
		t.Fatalf("expected cron message, got: %s", body)
	}
	if !strings.Contains(body, "Enabled: 1") {
		t.Fatalf("expected enabled count, got: %s", body)
	}
}

func TestPanelSessionStreamCatchupAndLive(t *testing.T) {
	store := newFakeSessionStore()
	now := time.Now().UTC()
	store.addSession("sess-2", sessionrt.SessionActive, []sessionrt.Event{
		{SessionID: "sess-2", Seq: 1, Type: sessionrt.EventMessage, Timestamp: now},
		{SessionID: "sess-2", Seq: 2, Type: sessionrt.EventToolCall, Timestamp: now.Add(time.Second)},
	})
	srv, err := NewServer(ServerOptions{ListenAddr: "127.0.0.1:29329", Store: store})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/_gopher/panel/stream/session/sess-2?after_seq=1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.newMux().ServeHTTP(rec, req)
		close(done)
	}()

	if !store.waitForStream("sess-2", 500*time.Millisecond) {
		t.Fatalf("expected stream subscriber to be created")
	}
	store.push("sess-2", sessionrt.Event{SessionID: "sess-2", Seq: 3, Type: sessionrt.EventToolResult, Timestamp: now.Add(2 * time.Second)})
	time.Sleep(120 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, `"seq":2`) {
		t.Fatalf("expected catch-up event seq=2, got: %s", body)
	}
	if !strings.Contains(body, `"seq":3`) {
		t.Fatalf("expected live event seq=3, got: %s", body)
	}
}

func TestPanelRunWithRetryRecoversFromPortConflict(t *testing.T) {
	occupy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := occupy.Addr().String()

	srv, err := NewServer(ServerOptions{
		ListenAddr: addr,
		NodeSnapshot: func() []scheduler.NodeInfo {
			return []scheduler.NodeInfo{{NodeID: "gateway", IsGateway: true}}
		},
	})
	if err != nil {
		t.Fatalf("NewServer() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.RunWithRetry(ctx)
	}()

	time.Sleep(650 * time.Millisecond)

	_ = occupy.Close()
	client := &http.Client{Timeout: 250 * time.Millisecond}
	healthURL := fmt.Sprintf("http://%s/_gopher/panel/health", addr)
	healthy := false
	deadline := time.After(4 * time.Second)
	for !healthy {
		select {
		case <-deadline:
			cancel()
			t.Fatalf("panel server did not recover after port release")
		default:
			resp, err := client.Get(healthURL)
			if err == nil {
				_, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					healthy = true
					break
				}
			}
			time.Sleep(120 * time.Millisecond)
		}
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("RunWithRetry() error: %v", err)
	}
}
