package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bstncartwright/gopher/pkg/panel"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const (
	defaultMockPanelListenAddr = "127.0.0.1:39400"
	mockPanelChatAgentID       = sessionrt.ActorID("agent:mock-operator")
)

type mockPanelFixture struct {
	server        *panel.Server
	listenAddr    string
	dataDir       string
	controlDir    string
	cronStorePath string
	devSPAURL     string
	store         *sessionrt.InMemoryEventStore
	cleanup       func()
}

type mockSessionSeed struct {
	SessionID        sessionrt.SessionID
	DisplayName      string
	ConversationID   string
	ConversationName string
	Status           sessionrt.SessionStatus
	InFlight         bool
	PendingResume    bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Events           []mockEventSeed
}

type mockEventSeed struct {
	From      sessionrt.ActorID
	Type      sessionrt.EventType
	Payload   any
	Timestamp time.Time
}

type mockControlWaitingSeed struct {
	SessionID string `json:"session_id"`
	Reason    string `json:"waiting_reason"`
	UpdatedAt string `json:"updated_at"`
}

type mockControlDelegationSeed struct {
	DelegationID    string `json:"delegation_id"`
	SourceSessionID string `json:"source_session_id"`
	SourceAgentID   string `json:"source_agent_id"`
	TargetAgentID   string `json:"target_agent_id"`
	Status          string `json:"status"`
	UpdatedAt       string `json:"ts"`
}

type mockControlActionSeed struct {
	Action    string `json:"action"`
	SessionID string `json:"session_id"`
	OK        bool   `json:"ok"`
	UpdatedAt string `json:"ts"`
	Error     string `json:"error,omitempty"`
}

func runPanelSubcommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printPanelUsage(stdout)
		return nil
	}

	switch strings.TrimSpace(args[0]) {
	case "help", "-h", "--help":
		printPanelUsage(stdout)
		return nil
	case "mock":
		return runPanelMockSubcommand(args[1:], stdout, stderr)
	default:
		printPanelUsage(stderr)
		return fmt.Errorf("unknown panel subcommand %q", args[0])
	}
}

func printPanelUsage(out io.Writer) {
	fmt.Fprintln(out, "gopher panel")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "usage:")
	fmt.Fprintln(out, "  gopher panel mock [flags]")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "commands:")
	fmt.Fprintln(out, "  mock   run the admin and chat surfaces locally with seeded mock data")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "flags:")
	fmt.Fprintln(out, "  --listen-addr <addr>  bind address for the local panel server")
	fmt.Fprintln(out, "  --data-dir <path>     optional fixture directory to reuse across runs")
}

func runPanelMockSubcommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("panel mock", flag.ContinueOnError)
	flags.SetOutput(stderr)

	listenAddr := flags.String("listen-addr", defaultMockPanelListenAddr, "bind address for the local panel server")
	dataDir := flags.String("data-dir", "", "optional fixture directory to reuse across runs")
	devSPAURL := flags.String("dev-spa-url", "", "URL of the React dev server to redirect /admin and /chat to (e.g. http://127.0.0.1:4010)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	fixture, err := buildMockPanelFixture(strings.TrimSpace(*listenAddr), strings.TrimSpace(*dataDir), strings.TrimSpace(*devSPAURL))
	if err != nil {
		return err
	}
	defer fixture.cleanup()

	fmt.Fprintf(stdout, "mock panel data: %s\n", fixture.dataDir)
	if fixture.devSPAURL != "" {
		fmt.Fprintf(stdout, "admin: %s/admin\n", fixture.devSPAURL)
		fmt.Fprintf(stdout, "chat:  %s/chat\n", fixture.devSPAURL)
		fmt.Fprintf(stdout, "api:   http://%s\n", fixture.listenAddr)
	} else {
		fmt.Fprintf(stdout, "admin: http://%s/admin/work\n", fixture.listenAddr)
		fmt.Fprintf(stdout, "chat:  http://%s/chat\n", fixture.listenAddr)
	}
	fmt.Fprintln(stdout, "press ctrl-c to stop")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return fixture.server.RunWithRetry(ctx)
}

func buildMockPanelFixture(listenAddr, dataDir, devSPAURL string) (*mockPanelFixture, error) {
	listenAddr = strings.TrimSpace(listenAddr)
	if listenAddr == "" {
		listenAddr = defaultMockPanelListenAddr
	}

	ownDataDir := false
	if dataDir == "" {
		tempDir, err := os.MkdirTemp("", "gopher-panel-mock-*")
		if err != nil {
			return nil, fmt.Errorf("create mock panel data dir: %w", err)
		}
		dataDir = tempDir
		ownDataDir = true
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create mock panel data dir: %w", err)
	}

	controlDir := filepath.Join(dataDir, "control")
	actionsDir := filepath.Join(controlDir, "actions")
	cronDir := filepath.Join(dataDir, "cron")
	for _, dir := range []string{controlDir, actionsDir, cronDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create mock panel fixture dir %q: %w", dir, err)
		}
	}

	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	metadata := map[sessionrt.SessionID]panel.SessionMetadata{}

	now := time.Now().UTC().Truncate(time.Second)
	if err := seedMockWorkSessions(store, metadata, now); err != nil {
		return nil, err
	}

	chatNow := now
	chatManager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: mockChatExecutor{},
		Now: func() time.Time {
			return chatNow
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create mock chat manager: %w", err)
	}
	if err := seedMockChatSessions(chatManager, func(ts time.Time) {
		chatNow = ts.UTC()
	}, now); err != nil {
		return nil, err
	}

	waiting := []mockControlWaitingSeed{
		{
			SessionID: "sess-node-restart-approval",
			Reason:    "Awaiting operator approval to restart node us-west-2b-3.",
			UpdatedAt: now.Add(-7 * time.Minute).Format(time.RFC3339),
		},
	}
	delegations := []mockControlDelegationSeed{
		{
			DelegationID:    "dlg-1192",
			SourceSessionID: "sess-automation-storm",
			SourceAgentID:   "agent:mock-operator",
			TargetAgentID:   "agent:cron-auditor",
			Status:          "active",
			UpdatedAt:       now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		{
			DelegationID:    "dlg-1184",
			SourceSessionID: "sess-control-plane-spike",
			SourceAgentID:   "agent:mock-operator",
			TargetAgentID:   "agent:node-sweeper",
			Status:          "complete",
			UpdatedAt:       now.Add(-12 * time.Minute).Format(time.RFC3339),
		},
	}
	actions := []mockControlActionSeed{
		{
			Action:    "delegate.session",
			SessionID: "sess-automation-storm",
			OK:        true,
			UpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339),
		},
		{
			Action:    "pause.session",
			SessionID: "sess-tenant-import-freeze",
			OK:        true,
			UpdatedAt: now.Add(-28 * time.Minute).Format(time.RFC3339),
		},
		{
			Action:    "resume.blocked",
			SessionID: "sess-node-restart-approval",
			OK:        false,
			UpdatedAt: now.Add(-8 * time.Minute).Format(time.RFC3339),
			Error:     "approval_missing",
		},
	}
	if err := writeMockControlFiles(controlDir, waiting, delegations, actions); err != nil {
		return nil, err
	}

	cronStorePath := filepath.Join(cronDir, "jobs.json")
	if err := writeMockCronJobs(cronStorePath, now); err != nil {
		return nil, err
	}

	server, err := panel.NewServer(panel.ServerOptions{
		ListenAddr:  listenAddr,
		Store:       store,
		ChatManager: chatManager,
		ChatAgentID: mockPanelChatAgentID,
		SessionMetadata: func(sessionID sessionrt.SessionID) (panel.SessionMetadata, bool) {
			value, ok := metadata[sessionID]
			return value, ok
		},
		NodeSnapshot: func() []scheduler.NodeInfo {
			return mockNodeSnapshot(now)
		},
		AgentSnapshot: func() []panel.AgentInfo {
			return mockAgentSnapshot()
		},
		RemoteSnapshot: func() []panel.RemoteInfo {
			return mockRemoteSnapshot(now)
		},
		ControlDir:    controlDir,
		CronStorePath: cronStorePath,
		DevSPAURL:     devSPAURL,
		ServeSPA:      true,
	})
	if err != nil {
		return nil, fmt.Errorf("create mock panel server: %w", err)
	}

	cleanup := func() {}
	if ownDataDir {
		cleanup = func() {
			_ = os.RemoveAll(dataDir)
		}
	}

	return &mockPanelFixture{
		server:        server,
		listenAddr:    listenAddr,
		dataDir:       dataDir,
		controlDir:    controlDir,
		cronStorePath: cronStorePath,
		devSPAURL:     devSPAURL,
		store:         store,
		cleanup:       cleanup,
	}, nil
}

func seedMockWorkSessions(store *sessionrt.InMemoryEventStore, metadata map[sessionrt.SessionID]panel.SessionMetadata, now time.Time) error {
	seeds := []mockSessionSeed{
		{
			SessionID:        "sess-control-plane-spike",
			DisplayName:      "Control plane queue spike",
			ConversationID:   "OPS-401",
			ConversationName: "Control Plane Spike",
			Status:           sessionrt.SessionActive,
			InFlight:         true,
			CreatedAt:        now.Add(-52 * time.Minute),
			UpdatedAt:        now.Add(-4 * time.Minute),
			Events: []mockEventSeed{
				mockControlCreatedSeed("Control plane queue spike", now.Add(-52*time.Minute)),
				mockUserMessageSeed("Inspect the queue surge after deploy 428 and tell me whether we should roll back.", now.Add(-50*time.Minute)),
				mockThinkingSeed("Comparing scheduler heartbeats against cron fanout after deploy 428.", now.Add(-48*time.Minute)),
				mockToolSeed("exec", map[string]any{"cmd": "journalctl -u gopher-gateway --since -15m"}, map[string]any{
					"status": "ok",
					"stdout": "Gateway restarted 3 workers after a 41s heartbeat gap. Queue depth recovered once node bay-02 returned.",
				}, now.Add(-46*time.Minute)),
				mockStatePatchSeed(map[string]any{
					"model_id":                    "gpt-5.4",
					"model_provider":              "openai",
					"model_context_window":        200000,
					"reserve_tokens":              14000,
					"estimated_input_tokens":      64200,
					"recent_messages_used_tokens": 11800,
					"recent_messages_cap_tokens":  36000,
					"summary_strategy":            "rolling",
				}, now.Add(-15*time.Minute)),
				mockAgentMessageSeed("The spike lines up with a heartbeat gap on bay-02, not a scheduler regression. I’m validating whether the node needs a hard restart.", now.Add(-4*time.Minute)),
			},
		},
		{
			SessionID:        "sess-node-restart-approval",
			DisplayName:      "Node restart approval",
			ConversationID:   "OPS-418",
			ConversationName: "Restart Approval",
			Status:           sessionrt.SessionActive,
			PendingResume:    true,
			CreatedAt:        now.Add(-43 * time.Minute),
			UpdatedAt:        now.Add(-7 * time.Minute),
			Events: []mockEventSeed{
				mockControlCreatedSeed("Node restart approval", now.Add(-43*time.Minute)),
				mockUserMessageSeed("Prepare a restart plan for node us-west-2b-3 but do not execute it without me.", now.Add(-41*time.Minute)),
				mockToolSeed("delegate", map[string]any{
					"target": "agent:node-sweeper",
					"action": "restart-plan",
				}, map[string]any{
					"status": "ok",
					"result": "Plan created: drain queue, pause cron lane, restart node, verify scheduler reconnect.",
				}, now.Add(-32*time.Minute)),
				mockAgentMessageSeed("Restart plan is ready. Waiting on explicit operator approval before I touch the node.", now.Add(-7*time.Minute)),
			},
		},
		{
			SessionID:        "sess-automation-storm",
			DisplayName:      "Automation storm audit",
			ConversationID:   "CRON-207",
			ConversationName: "Automation Storm",
			Status:           sessionrt.SessionActive,
			InFlight:         true,
			CreatedAt:        now.Add(-39 * time.Minute),
			UpdatedAt:        now.Add(-5 * time.Minute),
			Events: []mockEventSeed{
				mockControlCreatedSeed("Automation storm audit", now.Add(-39*time.Minute)),
				mockUserMessageSeed("Find out why the midnight cron lane fanned out into duplicate sessions.", now.Add(-38*time.Minute)),
				mockToolSeed("search", map[string]any{
					"query": "duplicate cron dispatch midnight lane",
				}, map[string]any{
					"status": "ok",
					"result": "Matched 3 duplicate dispatches after cron timezone fallback.",
				}, now.Add(-30*time.Minute)),
				mockToolSeed("delegate", map[string]any{
					"target": "agent:cron-auditor",
					"action": "timeline-diff",
				}, map[string]any{
					"status": "ok",
					"result": "Delegated timeline diff to cron auditor.",
				}, now.Add(-18*time.Minute)),
				mockAgentMessageSeed("Duplicate fanout seems tied to a timezone fallback edge. I’ve delegated a timeline diff to confirm before I suggest a fix.", now.Add(-5*time.Minute)),
			},
		},
		{
			SessionID:        "sess-context-overflow",
			DisplayName:      "Context overflow cleanup",
			ConversationID:   "CTX-112",
			ConversationName: "Context Overflow",
			Status:           sessionrt.SessionActive,
			InFlight:         true,
			CreatedAt:        now.Add(-34 * time.Minute),
			UpdatedAt:        now.Add(-6 * time.Minute),
			Events: []mockEventSeed{
				mockControlCreatedSeed("Context overflow cleanup", now.Add(-34*time.Minute)),
				mockUserMessageSeed("Continue the migration review and stop losing tool output context.", now.Add(-33*time.Minute)),
				mockStatePatchSeed(map[string]any{
					"model_id":                     "gpt-5.4",
					"model_provider":               "openai",
					"model_context_window":         200000,
					"reserve_tokens":               16000,
					"estimated_input_tokens":       176400,
					"overflow_retries":             2,
					"overflow_stage":               "tool_results",
					"tool_result_truncation_count": 3,
					"summary_strategy":             "aggressive",
				}, now.Add(-11*time.Minute)),
				mockToolSeed("read", map[string]any{
					"path": "/workspace/pkg/gateway/cron_service.go",
				}, map[string]any{
					"status": "ok",
					"stdout": "Read 348 lines from cron_service.go after compaction.",
				}, now.Add(-9*time.Minute)),
				mockAgentMessageSeed("Context pressure is still high. I compacted tool output and reloaded the cron service slice before continuing.", now.Add(-6*time.Minute)),
			},
		},
		{
			SessionID:        "sess-remote-sync-failure",
			DisplayName:      "Remote sync failure",
			ConversationID:   "SYNC-099",
			ConversationName: "Remote Sync Failure",
			Status:           sessionrt.SessionFailed,
			CreatedAt:        now.Add(-71 * time.Minute),
			UpdatedAt:        now.Add(-23 * time.Minute),
			Events: []mockEventSeed{
				mockControlCreatedSeed("Remote sync failure", now.Add(-71*time.Minute)),
				mockUserMessageSeed("Sync the Berlin remote and summarize any auth issues.", now.Add(-70*time.Minute)),
				mockToolSeed("web_fetch", map[string]any{
					"url": "https://berlin.remote.internal/health",
				}, map[string]any{
					"status": "failed",
					"error":  "TLS handshake timeout",
				}, now.Add(-24*time.Minute)),
				{
					From:      mockPanelChatAgentID,
					Type:      sessionrt.EventError,
					Payload:   sessionrt.ErrorPayload{Message: "remote sync failed: TLS handshake timeout"},
					Timestamp: now.Add(-23 * time.Minute),
				},
				{
					From:      sessionrt.SystemActorID,
					Type:      sessionrt.EventControl,
					Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionFailed, Reason: "remote sync failed: TLS handshake timeout"},
					Timestamp: now.Add(-23 * time.Minute),
				},
			},
		},
		{
			SessionID:        "sess-tenant-import-freeze",
			DisplayName:      "Tenant import freeze",
			ConversationID:   "OPS-377",
			ConversationName: "Tenant Import Freeze",
			Status:           sessionrt.SessionPaused,
			CreatedAt:        now.Add(-88 * time.Minute),
			UpdatedAt:        now.Add(-28 * time.Minute),
			Events: []mockEventSeed{
				mockControlCreatedSeed("Tenant import freeze", now.Add(-88*time.Minute)),
				mockUserMessageSeed("Pause the import while support confirms whether tenant 44 is safe to retry.", now.Add(-85*time.Minute)),
				mockAgentMessageSeed("Import workers have been drained. Session is paused until support clears tenant 44.", now.Add(-28*time.Minute)),
				{
					From:      sessionrt.SystemActorID,
					Type:      sessionrt.EventControl,
					Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCancelled, Reason: "Paused pending support confirmation for tenant 44."},
					Timestamp: now.Add(-28 * time.Minute),
				},
			},
		},
		{
			SessionID:        "sess-fleet-capacity-review",
			DisplayName:      "Fleet capacity review",
			ConversationID:   "FLEET-061",
			ConversationName: "Fleet Capacity Review",
			Status:           sessionrt.SessionCompleted,
			CreatedAt:        now.Add(-2 * time.Hour),
			UpdatedAt:        now.Add(-45 * time.Minute),
			Events: []mockEventSeed{
				mockControlCreatedSeed("Fleet capacity review", now.Add(-2*time.Hour)),
				mockUserMessageSeed("Compare current fleet headroom against the overnight launch plan.", now.Add(-118*time.Minute)),
				mockToolSeed("exec", map[string]any{
					"cmd": "gopher status --role node",
				}, map[string]any{
					"status": "ok",
					"stdout": "4 healthy nodes, 31 idle agents, 18 queued sessions, projected headroom 2.3x.",
				}, now.Add(-62*time.Minute)),
				mockAgentMessageSeed("Current fleet headroom is acceptable for the overnight launch. No scale-out is required unless queue depth doubles from the current baseline.", now.Add(-47*time.Minute)),
				{
					From:      sessionrt.SystemActorID,
					Type:      sessionrt.EventControl,
					Payload:   sessionrt.ControlPayload{Action: sessionrt.ControlActionSessionCompleted, Reason: "Review delivered to operations."},
					Timestamp: now.Add(-45 * time.Minute),
				},
			},
		},
	}

	for _, seed := range seeds {
		if err := appendMockSessionSeed(store, seed); err != nil {
			return err
		}
		metadata[seed.SessionID] = panel.SessionMetadata{
			ConversationID:   seed.ConversationID,
			ConversationName: seed.ConversationName,
		}
	}
	return nil
}

func appendMockSessionSeed(store *sessionrt.InMemoryEventStore, seed mockSessionSeed) error {
	ctx := context.Background()
	for idx, event := range seed.Events {
		if err := store.Append(ctx, sessionrt.Event{
			ID:        sessionrt.EventID(fmt.Sprintf("%s-%02d", seed.SessionID, idx+1)),
			SessionID: seed.SessionID,
			From:      event.From,
			Type:      event.Type,
			Payload:   event.Payload,
			Timestamp: event.Timestamp.UTC(),
			Seq:       uint64(idx + 1),
		}); err != nil {
			return fmt.Errorf("append mock event for %s: %w", seed.SessionID, err)
		}
	}
	return store.UpsertSession(ctx, sessionrt.SessionRecord{
		SessionID:     seed.SessionID,
		DisplayName:   seed.DisplayName,
		Status:        seed.Status,
		CreatedAt:     seed.CreatedAt.UTC(),
		UpdatedAt:     seed.UpdatedAt.UTC(),
		LastSeq:       uint64(len(seed.Events)),
		InFlight:      seed.InFlight,
		PendingResume: seed.PendingResume,
	})
}

func seedMockChatSessions(chatManager sessionrt.SessionManager, setNow func(time.Time), now time.Time) error {
	type chatSeed struct {
		title   string
		prompt  string
		created time.Time
		updated time.Time
	}
	seeds := []chatSeed{
		{
			title:   "Admin shell direction",
			prompt:  "Give me three sharper visual directions for the admin workbench, with one sentence on the tradeoff for each.",
			created: now.Add(-36 * time.Hour),
			updated: now.Add(-35*time.Hour - 12*time.Minute),
		},
		{
			title:   "Chat composer tone",
			prompt:  "Write a tighter placeholder and helper copy for the local chat composer so it feels more like an ops console.",
			created: now.Add(-29 * time.Hour),
			updated: now.Add(-28*time.Hour - 40*time.Minute),
		},
	}

	for _, seed := range seeds {
		setNow(seed.created)
		session, err := chatManager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
			DisplayName: seed.title,
			Participants: []sessionrt.Participant{
				{ID: mockPanelChatAgentID, Type: sessionrt.ActorAgent},
				{
					ID:   sessionrt.ActorID("local:web"),
					Type: sessionrt.ActorHuman,
					Metadata: map[string]string{
						"name":                 "Local Operator",
						"channel":              "web_chat",
						"transport":            "web",
						"session_visibility":   "local",
						"session_origin":       "panel_chat",
						"conversation_surface": "chat",
					},
				},
			},
		})
		if err != nil {
			return fmt.Errorf("create mock chat session: %w", err)
		}
		setNow(seed.updated)
		if err := chatManager.SendEvent(context.Background(), sessionrt.Event{
			SessionID: session.ID,
			From:      sessionrt.ActorID("local:web"),
			Type:      sessionrt.EventMessage,
			Payload: sessionrt.Message{
				Role:          sessionrt.RoleUser,
				Content:       seed.prompt,
				TargetActorID: mockPanelChatAgentID,
			},
		}); err != nil {
			return fmt.Errorf("seed mock chat session %s: %w", session.ID, err)
		}
	}
	return nil
}

func writeMockControlFiles(controlDir string, waiting []mockControlWaitingSeed, delegations []mockControlDelegationSeed, actions []mockControlActionSeed) error {
	sessions := make([]map[string]any, 0, len(waiting))
	for _, item := range waiting {
		sessions = append(sessions, map[string]any{
			"session_id":         item.SessionID,
			"waiting_on_human":   true,
			"waiting_reason":     item.Reason,
			"updated_at":         item.UpdatedAt,
			"control_surface":    "mock",
			"attention_required": true,
		})
	}

	index := map[string]any{
		"summary": map[string]any{
			"active":    4,
			"paused":    1,
			"completed": 1,
			"failed":    1,
			"waiting":   len(waiting),
			"delegated": len(delegations),
		},
		"sessions": sessions,
	}
	if err := writeJSONFile(filepath.Join(controlDir, "session_index.json"), index); err != nil {
		return fmt.Errorf("write mock control index: %w", err)
	}
	if err := writeJSONLLines(filepath.Join(controlDir, "delegations.jsonl"), delegations); err != nil {
		return fmt.Errorf("write mock control delegations: %w", err)
	}
	if err := writeJSONLLines(filepath.Join(controlDir, "actions", "applied.jsonl"), actions); err != nil {
		return fmt.Errorf("write mock control actions: %w", err)
	}
	return nil
}

func writeMockCronJobs(path string, now time.Time) error {
	lastA := now.Add(-26 * time.Minute)
	nextA := now.Add(34 * time.Minute)
	lastB := now.Add(-95 * time.Minute)
	nextB := now.Add(25 * time.Minute)
	nextC := now.Add(3 * time.Hour)

	doc := map[string]any{
		"jobs": []map[string]any{
			{
				"id":              "cron-capacity-audit",
				"session_id":      "sess-fleet-capacity-review",
				"message":         "Review fleet headroom before launch window",
				"cron_expr":       "0 * * * *",
				"timezone":        "America/Denver",
				"enabled":         true,
				"created_by":      "local:mock",
				"updated_at":      now.Add(-14 * time.Minute).Format(time.RFC3339),
				"last_run_at":     lastA.Format(time.RFC3339),
				"next_run_at":     nextA.Format(time.RFC3339),
				"last_run_status": "ok",
			},
			{
				"id":              "cron-midnight-lane",
				"session_id":      "sess-automation-storm",
				"message":         "Audit duplicate automation fanout after midnight lane",
				"cron_expr":       "30 * * * *",
				"timezone":        "UTC",
				"enabled":         true,
				"created_by":      "ops@mock",
				"updated_at":      now.Add(-11 * time.Minute).Format(time.RFC3339),
				"last_run_at":     lastB.Format(time.RFC3339),
				"next_run_at":     nextB.Format(time.RFC3339),
				"last_run_status": "warning",
			},
			{
				"id":              "cron-tenant-import",
				"session_id":      "sess-tenant-import-freeze",
				"message":         "Resume tenant import after support sign-off",
				"cron_expr":       "0 */4 * * *",
				"timezone":        "America/New_York",
				"enabled":         false,
				"created_by":      "support@mock",
				"updated_at":      now.Add(-37 * time.Minute).Format(time.RFC3339),
				"next_run_at":     nextC.Format(time.RFC3339),
				"last_run_status": "paused",
			},
		},
	}
	if err := writeJSONFile(path, doc); err != nil {
		return fmt.Errorf("write mock cron store: %w", err)
	}
	return nil
}

func writeJSONFile(path string, value any) error {
	blob, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')
	return os.WriteFile(path, blob, 0o644)
}

func writeJSONLLines(path string, records any) error {
	blob, err := json.Marshal(records)
	if err != nil {
		return err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(blob, &items); err != nil {
		return err
	}
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, string(item))
	}
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func mockControlCreatedSeed(displayName string, when time.Time) mockEventSeed {
	return mockEventSeed{
		From: sessionrt.SystemActorID,
		Type: sessionrt.EventControl,
		Payload: sessionrt.ControlPayload{
			Action: sessionrt.ControlActionSessionCreated,
			Metadata: map[string]any{
				"display_name": displayName,
				"participants": []sessionrt.Participant{
					{ID: mockPanelChatAgentID, Type: sessionrt.ActorAgent},
					{ID: sessionrt.ActorID("operator:desk"), Type: sessionrt.ActorHuman, Metadata: map[string]string{"name": "Desk Operator"}},
				},
			},
		},
		Timestamp: when,
	}
}

func mockUserMessageSeed(content string, when time.Time) mockEventSeed {
	return mockEventSeed{
		From: sessionrt.ActorID("operator:desk"),
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:          sessionrt.RoleUser,
			Content:       content,
			TargetActorID: mockPanelChatAgentID,
		},
		Timestamp: when,
	}
}

func mockAgentMessageSeed(content string, when time.Time) mockEventSeed {
	return mockEventSeed{
		From: mockPanelChatAgentID,
		Type: sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleAgent,
			Content: content,
		},
		Timestamp: when,
	}
}

func mockThinkingSeed(content string, when time.Time) mockEventSeed {
	return mockEventSeed{
		From:      mockPanelChatAgentID,
		Type:      sessionrt.EventAgentThinkingDelta,
		Payload:   map[string]any{"delta": content},
		Timestamp: when,
	}
}

func mockStatePatchSeed(payload map[string]any, when time.Time) mockEventSeed {
	return mockEventSeed{
		From:      mockPanelChatAgentID,
		Type:      sessionrt.EventStatePatch,
		Payload:   payload,
		Timestamp: when,
	}
}

func mockToolSeed(toolName string, args map[string]any, result map[string]any, when time.Time) mockEventSeed {
	return mockEventSeed{
		From: mockPanelChatAgentID,
		Type: sessionrt.EventToolCall,
		Payload: map[string]any{
			"tool_name":   toolName,
			"arguments":   args,
			"tool_result": result,
		},
		Timestamp: when,
	}
}

func mockNodeSnapshot(now time.Time) []scheduler.NodeInfo {
	return []scheduler.NodeInfo{
		{
			NodeID:        "gateway-denver-1",
			IsGateway:     true,
			Version:       "mock-2026.03.18",
			Capabilities:  []scheduler.Capability{{Kind: scheduler.CapabilitySystem, Name: "control"}, {Kind: scheduler.CapabilityAgent, Name: "router"}},
			Agents:        []string{"agent:mock-operator", "agent:cron-auditor"},
			LastHeartbeat: now.Add(-18 * time.Second),
		},
		{
			NodeID:        "node-bay-02",
			IsGateway:     false,
			Version:       "mock-2026.03.18",
			Capabilities:  []scheduler.Capability{{Kind: scheduler.CapabilityAgent, Name: "node-sweeper"}, {Kind: scheduler.CapabilityTool, Name: "exec"}},
			Agents:        []string{"agent:node-sweeper"},
			LastHeartbeat: now.Add(-44 * time.Second),
		},
		{
			NodeID:        "node-berlin-04",
			IsGateway:     false,
			Version:       "mock-2026.03.18",
			Capabilities:  []scheduler.Capability{{Kind: scheduler.CapabilityAgent, Name: "remote-sync"}, {Kind: scheduler.CapabilityTool, Name: "web_fetch"}},
			Agents:        []string{"agent:remote-sync"},
			LastHeartbeat: now.Add(-2 * time.Minute),
		},
	}
}

func mockAgentSnapshot() []panel.AgentInfo {
	return []panel.AgentInfo{
		{
			AgentID:              "agent:mock-operator",
			Name:                 "Mock Operator",
			Role:                 "orchestrator",
			Workspace:            "/workspace/mock",
			ModelPolicy:          "gpt-5.4",
			RequiredCapabilities: []string{"agent:router", "tool:exec"},
			EnabledTools:         []string{"exec", "read", "search", "delegate"},
			SkillsPaths:          []string{"/Users/bstn/.agents/skills/frontend-design/SKILL.md"},
			KnownAgents:          []string{"agent:cron-auditor", "agent:node-sweeper"},
			FSRoots:              []string{"/workspace/mock", "/workspace/mock/assets"},
			AllowDomains:         []string{"github.com", "vercel.com"},
			CanShell:             true,
			ApplyPatchEnabled:    true,
			CaptureThinking:      true,
			NetworkEnabled:       true,
			MaxContextMessages:   48,
		},
		{
			AgentID:              "agent:cron-auditor",
			Name:                 "Cron Auditor",
			Role:                 "specialist",
			Workspace:            "/workspace/mock/cron",
			ModelPolicy:          "gpt-5.4-mini",
			RequiredCapabilities: []string{"agent:cron", "tool:search"},
			EnabledTools:         []string{"search", "read"},
			KnownAgents:          []string{"agent:mock-operator"},
			FSRoots:              []string{"/workspace/mock/cron"},
			CaptureThinking:      false,
			NetworkEnabled:       false,
			MaxContextMessages:   18,
		},
		{
			AgentID:              "agent:node-sweeper",
			Name:                 "Node Sweeper",
			Role:                 "infra",
			Workspace:            "/workspace/mock/fleet",
			ModelPolicy:          "gpt-5.4-mini",
			RequiredCapabilities: []string{"agent:ops", "tool:exec"},
			EnabledTools:         []string{"exec", "read"},
			KnownAgents:          []string{"agent:mock-operator"},
			FSRoots:              []string{"/workspace/mock/fleet"},
			CanShell:             true,
			CaptureThinking:      false,
			NetworkEnabled:       false,
			MaxContextMessages:   24,
		},
	}
}

func mockRemoteSnapshot(now time.Time) []panel.RemoteInfo {
	return []panel.RemoteInfo{
		{
			TargetID:    "remote-denver",
			DisplayName: "Denver Core",
			Description: "Primary ops workspace",
			Endpoint:    "nats://denver.mock.internal:4222",
			Healthy:     true,
			LastRefresh: now.Add(-25 * time.Second).Format(time.RFC3339),
		},
		{
			TargetID:    "remote-berlin",
			DisplayName: "Berlin Edge",
			Description: "Secondary EU edge cluster",
			Endpoint:    "nats://berlin.mock.internal:4222",
			Healthy:     false,
			LastRefresh: now.Add(-3 * time.Minute).Format(time.RFC3339),
			LastError:   "TLS handshake timeout",
		},
	}
}

type mockChatExecutor struct{}

func (mockChatExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	lastUserMessage := ""
	for i := len(input.History) - 1; i >= 0; i-- {
		event := input.History[i]
		if event.Type != sessionrt.EventMessage {
			continue
		}
		msg, ok := event.Payload.(sessionrt.Message)
		if !ok {
			continue
		}
		if msg.Role == sessionrt.RoleUser {
			lastUserMessage = strings.TrimSpace(msg.Content)
			break
		}
	}
	if lastUserMessage == "" {
		lastUserMessage = "Keep pushing the interface until it reads like a tool, not a toy."
	}

	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				Type: sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: mockChatReply(lastUserMessage),
				},
			},
		},
	}, nil
}

func mockChatReply(prompt string) string {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	switch {
	case strings.Contains(normalized, "visual direction"), strings.Contains(normalized, "direction"):
		return "Direction one: brushed-metal control room with hard dividers. Direction two: radar-console green with dense telemetry rails. Direction three: editorial terminal with oversized headlines and sharp utility modules."
	case strings.Contains(normalized, "composer"), strings.Contains(normalized, "placeholder"):
		return "Placeholder: Dispatch an ask, inspect a session, or issue a command. Helper copy: Messages here should move the work queue forward."
	case strings.Contains(normalized, "design"), strings.Contains(normalized, "layout"):
		return "The current read is too decorative. Cut the oversized banners, tighten the queue rail, and make the active timeline carry the page."
	default:
		return "Mock reply: sharper hierarchy, less ornamental chrome, and tighter operational copy will get the panel back into command-deck territory."
	}
}
