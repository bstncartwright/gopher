package main

import (
	"context"
	"fmt"
	"log"
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
	matrixtransport "github.com/bstncartwright/gopher/pkg/transport/matrix"
)

type matrixDMBridge struct {
	transport *matrixtransport.Transport
	pipeline  *gateway.DMPipeline
	cron      *gateway.CronRunner
	cancel    context.CancelFunc
}

type agentMatrixIdentitySet struct {
	DefaultUserID  string
	ManagedUserIDs []string
	UserByActorID  map[sessionrt.ActorID]string
	ActorByUserID  map[string]sessionrt.ActorID
}

func startMatrixDMBridge(ctx context.Context, cfg config.GatewayConfig, logger *log.Logger) (*matrixDMBridge, error) {
	workspace, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}
	return startMatrixDMBridgeWithRuntime(ctx, cfg, workspace, nil, nil, logger)
}

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
	storeDir := filepath.Join(workspace, ".gopher", "sessions")
	store, err := storepkg.NewFileEventStore(storepkg.FileEventStoreOptions{Dir: storeDir})
	if err != nil {
		return nil, fmt.Errorf("create session store: %w", err)
	}

	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:          store,
		Executor:       executor,
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
		cronFilePath := filepath.Join(workspace, ".gopher", "cron", "jobs.json")
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
	})
	if err != nil {
		return nil, fmt.Errorf("create matrix transport: %w", err)
	}

	pipeline, err := gateway.NewDMPipeline(gateway.DMPipelineOptions{
		Manager:          manager,
		Transport:        matrixBridge,
		AgentID:          agentRuntime.DefaultActorID,
		AgentByRecipient: identities.ActorByUserID,
		RecipientByAgent: identities.UserByActorID,
		Conversations:    gateway.NewConversationSessionMap(),
		Logger:           logger,
	})
	if err != nil {
		return nil, fmt.Errorf("create matrix dm pipeline: %w", err)
	}

	bridge := &matrixDMBridge{transport: matrixBridge, pipeline: pipeline, cron: cronRunner}
	bridgeCtx, cancel := context.WithCancel(ctx)
	bridge.cancel = cancel
	go bridge.runSupervisor(bridgeCtx, logger)
	if logger != nil {
		logger.Printf("matrix dm bridge started appservice_id=%s listen_addr=%s", cfg.Matrix.AppserviceID, cfg.Matrix.ListenAddr)
	}
	return bridge, nil
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
