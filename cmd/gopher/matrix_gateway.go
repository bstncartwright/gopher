package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
}

func loadGatewayRuntimeExecutor(workspace string) (sessionrt.AgentExecutor, error) {
	agent, err := agentcore.LoadAgent(workspace)
	if err != nil {
		return nil, fmt.Errorf("load gateway runtime agent: %w", err)
	}
	return agentcore.NewSessionRuntimeAdapter(agent), nil
}

func startMatrixDMBridge(ctx context.Context, cfg config.GatewayConfig, logger *log.Logger) (*matrixDMBridge, error) {
	workspace, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolve workspace: %w", err)
	}

	agent, err := agentcore.LoadAgent(workspace)
	if err != nil {
		return nil, fmt.Errorf("load agent for gateway runtime: %w", err)
	}
	executor := agentcore.NewSessionRuntimeAdapter(agent)
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
		agent.Cron = newGatewayCronToolService(cronService)
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
		HomeserverURL: cfg.Matrix.HomeserverURL,
		AppserviceID:  cfg.Matrix.AppserviceID,
		ASToken:       cfg.Matrix.ASToken,
		HSToken:       cfg.Matrix.HSToken,
		ListenAddr:    cfg.Matrix.ListenAddr,
		BotUserID:     cfg.Matrix.BotUserID,
	})
	if err != nil {
		return nil, fmt.Errorf("create matrix transport: %w", err)
	}

	pipeline, err := gateway.NewDMPipeline(gateway.DMPipelineOptions{
		Manager:       manager,
		Transport:     matrixBridge,
		AgentID:       sessionrt.ActorID(agent.ID),
		Conversations: gateway.NewConversationSessionMap(),
		Logger:        logger,
	})
	if err != nil {
		return nil, fmt.Errorf("create matrix dm pipeline: %w", err)
	}

	bridge := &matrixDMBridge{transport: matrixBridge, pipeline: pipeline, cron: cronRunner}

	go func() {
		if err := matrixBridge.Start(ctx); err != nil && logger != nil {
			logger.Printf("matrix transport stopped: %v", err)
		}
	}()
	if logger != nil {
		logger.Printf("matrix dm bridge started appservice_id=%s listen_addr=%s", cfg.Matrix.AppserviceID, cfg.Matrix.ListenAddr)
	}
	return bridge, nil
}

func (b *matrixDMBridge) Stop() {
	if b == nil {
		return
	}
	if b.cron != nil {
		b.cron.Stop()
	}
	if b.transport != nil {
		_ = b.transport.Stop()
	}
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
