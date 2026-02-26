package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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
		{SessionID: "sess-1", Seq: 2, Type: sessionrt.EventToolCall, From: "agent:a", Payload: map[string]any{"name": "read"}, Timestamp: now.Add(time.Second)},
	})
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

	detailRec := httptest.NewRecorder()
	detailReq := httptest.NewRequest(http.MethodGet, "/_gopher/panel/fragments/session/sess-1", nil)
	mux.ServeHTTP(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", detailRec.Code)
	}
	if !strings.Contains(detailRec.Body.String(), "tool_call") {
		t.Fatalf("expected timeline events, got: %s", detailRec.Body.String())
	}
	if !strings.Contains(detailRec.Body.String(), "Writer Room") {
		t.Fatalf("expected room name in detail fragment, got: %s", detailRec.Body.String())
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
