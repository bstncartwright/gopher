package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	storepkg "github.com/bstncartwright/gopher/pkg/store"
	"github.com/bstncartwright/gopher/pkg/transport"
	matrixtransport "github.com/bstncartwright/gopher/pkg/transport/matrix"
)

type matrixDMBridge struct {
	transport *matrixtransport.Transport
	pipeline  *gateway.DMPipeline
	cron      *gateway.CronRunner
	heartbeat *gateway.HeartbeatRunner
	bindings  gateway.ConversationBindingStore
	store     interface {
		sessionrt.EventStore
		sessionrt.SessionRegistryStore
	}
	cancel context.CancelFunc
}

type agentMatrixIdentitySet struct {
	DefaultUserID  string
	ManagedUserIDs []string
	UserByActorID  map[sessionrt.ActorID]string
	ActorByUserID  map[string]sessionrt.ActorID
}

const matrixStartupCatchupLimit = 64

func startMatrixDMBridgeWithRuntime(
	ctx context.Context,
	cfg config.GatewayConfig,
	workspace string,
	agentRuntime *gatewayAgentRuntime,
	executor sessionrt.AgentExecutor,
	logger *log.Logger,
) (*matrixDMBridge, error) {
	var err error
	if agentRuntime == nil {
		agentRuntime, err = loadGatewayAgentRuntime(workspace)
		if err != nil {
			return nil, fmt.Errorf("load gateway agents: %w", err)
		}
	}
	identities, err := buildAgentMatrixIdentitySet(agentRuntime, cfg.Matrix.BotUserID)
	if err != nil {
		return nil, fmt.Errorf("build matrix identities: %w", err)
	}
	if executor == nil {
		executor = agentRuntime.Executor
	}
	dataDir := resolveGatewayDataDir(workspace)
	storeDir := filepath.Join(dataDir, "sessions")
	store, err := storepkg.NewFileEventStore(storepkg.FileEventStoreOptions{Dir: storeDir})
	if err != nil {
		return nil, fmt.Errorf("create session store: %w", err)
	}
	bindingStorePath := filepath.Join(storeDir, "conversation_bindings.json")
	bindingStore, err := gateway.NewFileConversationBindingStore(bindingStorePath)
	if err != nil {
		return nil, fmt.Errorf("create conversation binding store: %w", err)
	}

	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:          store,
		Executor:       executor,
		AgentSelector:  matrixMentionAgentSelector(identities, newMatrixLLMUntaggedResponderRouter(agentRuntime, logger)),
		RecoverOnStart: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create session manager: %w", err)
	}
	var cronRunner *gateway.CronRunner
	if cfg.Cron.Enabled {
		dispatcher, err := gateway.NewSessionCronDispatcher(manager)
		if err != nil {
			return nil, fmt.Errorf("create cron dispatcher: %w", err)
		}
		cronFilePath := filepath.Join(dataDir, "cron", "jobs.json")
		cronStore, err := gateway.NewFileCronStore(cronFilePath)
		if err != nil {
			return nil, fmt.Errorf("create cron store: %w", err)
		}
		cronService, err := gateway.NewCronService(gateway.CronServiceOptions{
			Store:              cronStore,
			Dispatcher:         dispatcher,
			DefaultTimezone:    cfg.Cron.DefaultTimezone,
			CatchupOnStartOnce: true,
		})
		if err != nil {
			return nil, fmt.Errorf("create cron service: %w", err)
		}
		cronTool := newGatewayCronToolService(cronService)
		for _, agent := range agentRuntime.Agents {
			agent.Cron = cronTool
		}
		cronRunner, err = gateway.NewCronRunner(gateway.CronRunnerOptions{
			Service:      cronService,
			PollInterval: cfg.Cron.PollInterval,
		})
		if err != nil {
			return nil, fmt.Errorf("create cron runner: %w", err)
		}
		if err := cronRunner.Start(ctx); err != nil {
			return nil, fmt.Errorf("start cron runner: %w", err)
		}
		if logger != nil {
			logger.Printf("cron runner started timezone=%s poll_interval=%s", cfg.Cron.DefaultTimezone, cfg.Cron.PollInterval.String())
		}
	}

	matrixBridge, err := matrixtransport.New(matrixtransport.Options{
		HomeserverURL:     cfg.Matrix.HomeserverURL,
		AppserviceID:      cfg.Matrix.AppserviceID,
		ASToken:           cfg.Matrix.ASToken,
		HSToken:           cfg.Matrix.HSToken,
		ListenAddr:        cfg.Matrix.ListenAddr,
		BotUserID:         identities.DefaultUserID,
		ManagedUserIDs:    identities.ManagedUserIDs,
		RichTextEnabled:   cfg.Matrix.RichTextEnabled,
		PresenceEnabled:   cfg.Matrix.PresenceEnabled,
		PresenceInterval:  cfg.Matrix.PresenceInterval,
		PresenceStatusMsg: cfg.Matrix.PresenceStatusMsg,
		AvatarProvider:    newMatrixManagedAvatarProvider(agentRuntime, identities, logger),
	})
	if err != nil {
		return nil, fmt.Errorf("create matrix transport: %w", err)
	}

	var tracePublisher gateway.TracePublisher
	var traceProvisioner gateway.TraceConversationProvisioner
	if cfg.Matrix.TraceEnabled {
		tracePublisher = gateway.NewMatrixTracePublisher(matrixBridge)
		traceProvisioner = newMatrixTraceConversationProvisioner(matrixBridge, logger)
	}

	pipeline, err := gateway.NewDMPipeline(gateway.DMPipelineOptions{
		Manager:          manager,
		Transport:        matrixBridge,
		AgentID:          agentRuntime.DefaultActorID,
		AgentByRecipient: identities.ActorByUserID,
		RecipientByAgent: identities.UserByActorID,
		Conversations:    gateway.NewConversationSessionMap(),
		Bindings:         bindingStore,
		TracePublisher:   tracePublisher,
		TraceProvisioner: traceProvisioner,
	})
	if err != nil {
		return nil, fmt.Errorf("create matrix dm pipeline: %w", err)
	}
	delegationTool := newGatewayDelegationToolService(manager, pipeline, matrixBridge, identities, logger)
	for _, agent := range agentRuntime.Agents {
		agent.Delegation = delegationTool
	}

	heartbeatSchedules := collectHeartbeatSchedules(agentRuntime)
	heartbeatRunner, err := gateway.NewHeartbeatRunner(gateway.HeartbeatRunnerOptions{
		Manager:   manager,
		Pipeline:  pipeline,
		Schedules: heartbeatSchedules,
		Logger:    logger,
	})
	if err != nil {
		return nil, fmt.Errorf("create heartbeat runner: %w", err)
	}
	if err := heartbeatRunner.Start(ctx); err != nil {
		return nil, fmt.Errorf("start heartbeat runner: %w", err)
	}
	if logger != nil {
		logger.Printf("heartbeat runner started agents=%d", len(heartbeatSchedules))
	}
	heartbeatTool := newGatewayHeartbeatToolService(agentRuntime.Agents, heartbeatRunner, logger)
	for _, agent := range agentRuntime.Agents {
		agent.HeartbeatService = heartbeatTool
	}

	bridge := &matrixDMBridge{
		transport: matrixBridge,
		pipeline:  pipeline,
		cron:      cronRunner,
		heartbeat: heartbeatRunner,
		bindings:  bindingStore,
		store:     store,
	}
	runMatrixStartupCatchup(ctx, cfg, identities, bindingStore, pipeline, logger)
	bridgeCtx, cancel := context.WithCancel(ctx)
	bridge.cancel = cancel
	go bridge.runSupervisor(bridgeCtx, logger)
	if logger != nil {
		logger.Printf("matrix dm bridge started appservice_id=%s listen_addr=%s", cfg.Matrix.AppserviceID, cfg.Matrix.ListenAddr)
	}
	return bridge, nil
}

func resolveGatewayDataDir(workspace string) string {
	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspace == "" {
		return ".gopher"
	}
	if filepath.Base(workspace) != ".gopher" {
		return filepath.Join(workspace, ".gopher")
	}
	canonical := workspace
	legacy := filepath.Join(workspace, ".gopher")
	if hasGatewayData(canonical) {
		return canonical
	}
	if hasGatewayData(legacy) {
		return legacy
	}
	return canonical
}

func hasGatewayData(dataDir string) bool {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return false
	}
	sessionsPath := filepath.Join(dataDir, "sessions", "conversation_bindings.json")
	if _, err := os.Stat(sessionsPath); err == nil {
		return true
	}
	cronPath := filepath.Join(dataDir, "cron", "jobs.json")
	if _, err := os.Stat(cronPath); err == nil {
		return true
	}
	return false
}

type matrixTimelineResponse struct {
	Chunk []matrixTimelineEvent `json:"chunk"`
}

type matrixTimelineEvent struct {
	EventID string         `json:"event_id"`
	Type    string         `json:"type"`
	Sender  string         `json:"sender"`
	Content map[string]any `json:"content"`
}

func runMatrixStartupCatchup(
	ctx context.Context,
	cfg config.GatewayConfig,
	identities agentMatrixIdentitySet,
	bindings gateway.ConversationBindingStore,
	pipeline *gateway.DMPipeline,
	logger *log.Logger,
) {
	if bindings == nil || pipeline == nil {
		return
	}
	allBindings := bindings.List()
	if len(allBindings) == 0 {
		return
	}
	totalReplayed := 0
	for _, binding := range allBindings {
		replayed, err := replayUnprocessedConversationEvents(ctx, cfg, identities, pipeline, binding)
		if err != nil {
			if logger != nil {
				logger.Printf("matrix startup catchup skipped conversation_id=%s err=%v", binding.ConversationID, err)
			}
			continue
		}
		totalReplayed += replayed
	}
	if logger != nil && totalReplayed > 0 {
		logger.Printf("matrix startup catchup replayed_messages=%d", totalReplayed)
	}
}

func replayUnprocessedConversationEvents(
	ctx context.Context,
	cfg config.GatewayConfig,
	identities agentMatrixIdentitySet,
	pipeline *gateway.DMPipeline,
	binding gateway.ConversationBinding,
) (int, error) {
	conversationID := strings.TrimSpace(binding.ConversationID)
	if conversationID == "" {
		return 0, nil
	}
	userID := strings.TrimSpace(binding.RecipientID)
	if userID == "" && strings.TrimSpace(string(binding.AgentID)) != "" {
		userID = strings.TrimSpace(identities.UserByActorID[binding.AgentID])
	}
	if userID == "" {
		userID = strings.TrimSpace(identities.DefaultUserID)
	}
	events, err := fetchRecentMatrixRoomEvents(ctx, cfg, conversationID, userID, matrixStartupCatchupLimit)
	if err != nil {
		return 0, err
	}
	replay := selectCatchupReplayEvents(events, binding.LastInboundEvent)
	if len(replay) == 0 {
		return 0, nil
	}
	count := 0
	for _, event := range replay {
		if strings.TrimSpace(event.Type) != "m.room.message" {
			continue
		}
		if strings.TrimSpace(event.Sender) == "" {
			continue
		}
		msgType, _ := event.Content["msgtype"].(string)
		if strings.TrimSpace(msgType) != "m.text" {
			continue
		}
		text, _ := event.Content["body"].(string)
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
			ConversationID:   conversationID,
			ConversationName: strings.TrimSpace(binding.ConversationName),
			SenderID:         strings.TrimSpace(event.Sender),
			RecipientID:      userID,
			EventID:          strings.TrimSpace(event.EventID),
			Text:             text,
		}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func fetchRecentMatrixRoomEvents(ctx context.Context, cfg config.GatewayConfig, conversationID, userID string, limit int) ([]matrixTimelineEvent, error) {
	if limit <= 0 {
		limit = matrixStartupCatchupLimit
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/messages", strings.TrimRight(cfg.Matrix.HomeserverURL, "/"), url.PathEscape(conversationID))
	query := url.Values{}
	query.Set("access_token", cfg.Matrix.ASToken)
	query.Set("dir", "b")
	query.Set("limit", fmt.Sprintf("%d", limit))
	if strings.TrimSpace(userID) != "" {
		query.Set("user_id", strings.TrimSpace(userID))
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build room catchup request: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("send room catchup request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("room catchup status=%s body=%s", response.Status, strings.TrimSpace(string(body)))
	}
	var payload matrixTimelineResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode room catchup payload: %w", err)
	}
	return payload.Chunk, nil
}

func selectCatchupReplayEvents(events []matrixTimelineEvent, lastInboundEvent string) []matrixTimelineEvent {
	lastInboundEvent = strings.TrimSpace(lastInboundEvent)
	if len(events) == 0 {
		return nil
	}
	// `/messages?dir=b` returns newest -> oldest.
	pending := make([]matrixTimelineEvent, 0, len(events))
	for _, event := range events {
		eventID := strings.TrimSpace(event.EventID)
		if lastInboundEvent != "" && eventID == lastInboundEvent {
			break
		}
		pending = append(pending, event)
	}
	for i, j := 0, len(pending)-1; i < j; i, j = i+1, j-1 {
		pending[i], pending[j] = pending[j], pending[i]
	}
	return pending
}

func (b *matrixDMBridge) runSupervisor(ctx context.Context, logger *log.Logger) {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		attempt++
		if logger != nil {
			logger.Printf("matrix_bridge_starting attempt=%d", attempt)
		}
		err := b.transport.Start(ctx)
		if err == nil {
			if logger != nil {
				logger.Printf("matrix_bridge_ready")
			}
			if ctx.Err() != nil {
				return
			}
		} else {
			if logger != nil {
				logger.Printf("matrix_bridge_degraded err=%v", err)
			}
		}
		if ctx.Err() != nil {
			return
		}
		delay := matrixBridgeRetryDelay(attempt)
		if logger != nil {
			logger.Printf("matrix_bridge_retrying attempt=%d next_in=%s", attempt, delay.String())
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func matrixBridgeRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := 500 * time.Millisecond
	maxDelay := 30 * time.Second
	delay := base * time.Duration(1<<max(0, attempt-1))
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (b *matrixDMBridge) Stop() {
	if b == nil {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	if b.cron != nil {
		b.cron.Stop()
	}
	if b.heartbeat != nil {
		b.heartbeat.Stop()
	}
	if b.transport != nil {
		_ = b.transport.Stop()
	}
}

func buildAgentMatrixIdentitySet(runtime *gatewayAgentRuntime, templateUserID string) (agentMatrixIdentitySet, error) {
	if runtime == nil {
		return agentMatrixIdentitySet{}, fmt.Errorf("runtime is required")
	}
	domain, err := matrixDomainFromUserID(templateUserID)
	if err != nil {
		return agentMatrixIdentitySet{}, err
	}
	out := agentMatrixIdentitySet{
		DefaultUserID:  "",
		ManagedUserIDs: make([]string, 0, len(runtime.Agents)),
		UserByActorID:  make(map[sessionrt.ActorID]string, len(runtime.Agents)),
		ActorByUserID:  make(map[string]sessionrt.ActorID, len(runtime.Agents)),
	}
	for actorID := range runtime.Agents {
		localpart, err := matrixLocalpartFromActorID(actorID)
		if err != nil {
			return agentMatrixIdentitySet{}, err
		}
		userID := "@" + localpart + ":" + domain
		out.ManagedUserIDs = append(out.ManagedUserIDs, userID)
		out.UserByActorID[actorID] = userID
		out.ActorByUserID[strings.ToLower(userID)] = actorID
		if actorID == runtime.DefaultActorID {
			out.DefaultUserID = userID
		}
	}
	sort.Strings(out.ManagedUserIDs)
	if strings.TrimSpace(out.DefaultUserID) == "" && len(out.ManagedUserIDs) > 0 {
		out.DefaultUserID = out.ManagedUserIDs[0]
	}
	return out, nil
}

func matrixDomainFromUserID(userID string) (string, error) {
	value := strings.TrimSpace(userID)
	if value == "" {
		return "", fmt.Errorf("gateway.matrix.bot_user_id is required to derive matrix domain")
	}
	if !strings.HasPrefix(value, "@") {
		return "", fmt.Errorf("invalid gateway.matrix.bot_user_id %q", userID)
	}
	parts := strings.SplitN(strings.TrimPrefix(value, "@"), ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
		return "", fmt.Errorf("invalid gateway.matrix.bot_user_id %q", userID)
	}
	return strings.TrimSpace(parts[1]), nil
}

func matrixLocalpartFromActorID(actorID sessionrt.ActorID) (string, error) {
	value := strings.TrimSpace(string(actorID))
	value = strings.TrimPrefix(value, "agent:")
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("invalid empty agent id for matrix user mapping")
	}
	if strings.ContainsAny(value, " @:") {
		return "", fmt.Errorf("agent id %q cannot be mapped to matrix localpart", actorID)
	}
	return value, nil
}

type matrixTraceConversationProvisioner struct {
	transport *matrixtransport.Transport
	logger    *log.Logger
}

func newMatrixTraceConversationProvisioner(transport *matrixtransport.Transport, logger *log.Logger) *matrixTraceConversationProvisioner {
	return &matrixTraceConversationProvisioner{
		transport: transport,
		logger:    logger,
	}
}

func (p *matrixTraceConversationProvisioner) CreateTraceConversation(ctx context.Context, req gateway.TraceConversationRequest) (gateway.TraceConversationBinding, error) {
	if p == nil || p.transport == nil {
		return gateway.TraceConversationBinding{}, fmt.Errorf("matrix trace provisioner is unavailable")
	}
	creatorUserID := strings.TrimSpace(req.RecipientID)
	if creatorUserID == "" {
		return gateway.TraceConversationBinding{}, fmt.Errorf("trace room creator user is required")
	}
	traceRoomName := traceRoomNameFromSessionID(req.SessionID)
	traceRoomTopic := fmt.Sprintf("Trace stream for session %s", strings.TrimSpace(string(req.SessionID)))
	roomID, err := p.transport.CreatePublicRoom(ctx, matrixtransport.CreatePublicRoomOptions{
		Name:          traceRoomName,
		Topic:         traceRoomTopic,
		CreatorUserID: creatorUserID,
		InviteUserIDs: nil,
	})
	if err != nil {
		if p.logger != nil {
			p.logger.Printf("matrix trace room creation failed session=%s conversation=%s err=%v", req.SessionID, req.ConversationID, err)
		}
		return gateway.TraceConversationBinding{}, err
	}
	p.transport.RecordTraceRoomCreated()
	return gateway.TraceConversationBinding{
		ConversationID:   roomID,
		ConversationName: traceRoomName,
		Mode:             gateway.TraceModeReadOnly,
		Render:           gateway.TraceRenderCards,
	}, nil
}

func traceRoomNameFromSessionID(sessionID sessionrt.SessionID) string {
	raw := strings.TrimSpace(string(sessionID))
	if raw == "" {
		return "trace-session"
	}
	short := raw
	if len(short) > 12 {
		short = short[:12]
	}
	short = strings.ToLower(short)
	short = strings.ReplaceAll(short, ":", "-")
	short = strings.ReplaceAll(short, "_", "-")
	short = strings.ReplaceAll(short, " ", "-")
	return "trace-" + short
}

type gatewayCronToolService struct {
	service *gateway.CronService
}

func newGatewayCronToolService(service *gateway.CronService) *gatewayCronToolService {
	return &gatewayCronToolService{service: service}
}

func (s *gatewayCronToolService) CreateCronJob(ctx context.Context, req agentcore.CronCreateRequest) (agentcore.CronJob, error) {
	job, err := s.service.Create(ctx, gateway.CronCreateInput{
		SessionID: req.SessionID,
		Message:   req.Message,
		CronExpr:  req.CronExpr,
		Timezone:  req.Timezone,
		CreatedBy: req.CreatedBy,
	})
	if err != nil {
		return agentcore.CronJob{}, err
	}
	return toAgentCronJob(job), nil
}

func (s *gatewayCronToolService) ListCronJobs(ctx context.Context, req agentcore.CronListRequest) ([]agentcore.CronJob, error) {
	jobs, err := s.service.List(ctx, gateway.CronListOptions{SessionID: req.SessionID})
	if err != nil {
		return nil, err
	}
	out := make([]agentcore.CronJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toAgentCronJob(job))
	}
	return out, nil
}

func (s *gatewayCronToolService) DeleteCronJob(ctx context.Context, jobID string) (bool, error) {
	return s.service.Delete(ctx, jobID)
}

func (s *gatewayCronToolService) PauseCronJob(ctx context.Context, jobID string) (agentcore.CronJob, error) {
	job, err := s.service.Pause(ctx, jobID)
	if err != nil {
		return agentcore.CronJob{}, err
	}
	return toAgentCronJob(job), nil
}

func (s *gatewayCronToolService) ResumeCronJob(ctx context.Context, jobID string) (agentcore.CronJob, error) {
	job, err := s.service.Resume(ctx, jobID)
	if err != nil {
		return agentcore.CronJob{}, err
	}
	return toAgentCronJob(job), nil
}

func toAgentCronJob(job gateway.CronJob) agentcore.CronJob {
	out := agentcore.CronJob{
		ID:        job.ID,
		SessionID: job.SessionID,
		Message:   job.Message,
		CronExpr:  job.CronExpr,
		Timezone:  job.Timezone,
		Enabled:   job.Enabled,
		CreatedBy: job.CreatedBy,
		CreatedAt: job.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: job.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if job.LastRunAt != nil {
		value := job.LastRunAt.UTC().Format(time.RFC3339Nano)
		out.LastRunAt = &value
	}
	if job.NextRunAt != nil {
		value := job.NextRunAt.UTC().Format(time.RFC3339Nano)
		out.NextRunAt = &value
	}
	return out
}

func collectHeartbeatSchedules(runtime *gatewayAgentRuntime) []gateway.HeartbeatSchedule {
	if runtime == nil {
		return nil
	}
	out := make([]gateway.HeartbeatSchedule, 0, len(runtime.Agents))
	for actorID, agent := range runtime.Agents {
		if agent == nil || !agent.Heartbeat.Enabled || agent.Heartbeat.Every <= 0 {
			continue
		}
		out = append(out, gateway.HeartbeatSchedule{
			AgentID:     actorID,
			Every:       agent.Heartbeat.Every,
			Prompt:      agent.Heartbeat.Prompt,
			AckMaxChars: agent.Heartbeat.AckMaxChars,
			Timezone:    strings.TrimSpace(agent.Config.UserTimezone),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return string(out[i].AgentID) < string(out[j].AgentID)
	})
	return out
}
