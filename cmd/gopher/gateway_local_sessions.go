package main

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	storepkg "github.com/bstncartwright/gopher/pkg/store"
)

type gatewayLocalSessionRuntime struct {
	manager sessionrt.SessionManager
	store   interface {
		sessionrt.EventStore
		sessionrt.SessionRegistryStore
	}
	delegation *gatewaySessionDelegationToolService
	cron       *gateway.CronRunner
}

func startGatewayLocalSessionRuntime(
	ctx context.Context,
	cfg config.GatewayConfig,
	workspace string,
	agentRuntime *gatewayAgentRuntime,
	executor sessionrt.AgentExecutor,
	remoteAgentExists func(sessionrt.ActorID) bool,
	remoteTargets func() []agentcore.RemoteDelegationTarget,
	logger *log.Logger,
) (*gatewayLocalSessionRuntime, error) {
	if agentRuntime == nil {
		return nil, fmt.Errorf("agent runtime is required")
	}
	if executor == nil {
		executor = agentRuntime.Executor
	}
	if executor == nil {
		return nil, fmt.Errorf("session executor is required")
	}
	if remoteAgentExists == nil {
		remoteAgentExists = func(sessionrt.ActorID) bool { return false }
	}
	if remoteTargets == nil {
		remoteTargets = func() []agentcore.RemoteDelegationTarget { return nil }
	}

	dataDir := resolveGatewayDataDir(workspace)
	storeDir := filepath.Join(dataDir, "sessions")
	store, err := storepkg.NewFileEventStore(storepkg.FileEventStoreOptions{Dir: storeDir})
	if err != nil {
		return nil, fmt.Errorf("create session store: %w", err)
	}

	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:          store,
		Executor:       executor,
		AgentSelector:  gatewayMessageTargetSelector(agentRuntime.DefaultActorID),
		RecoverOnStart: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create session manager: %w", err)
	}

	for _, agent := range agentRuntime.Agents {
		if agent == nil || agent.LongTermMemory == nil {
			continue
		}
		agent.SessionMemoryFlusher = agentcore.NewStoreBackedSessionMemoryFlusher(store, agent.LongTermMemory, agent.ID)
	}

	delegationTool := newGatewaySessionDelegationToolService(
		manager,
		store,
		agentRuntime.Agents,
		dataDir,
		logger,
		agentRuntime.Router,
		remoteAgentExists,
		remoteTargets,
	)
	if cfg.A2A.Enabled {
		delegationTool.SetA2ABackend(ctx, newGatewayA2ABackend(cfg.A2A, nil))
	}
	for _, agent := range agentRuntime.Agents {
		if agent == nil {
			continue
		}
		agent.Delegation = delegationTool
	}

	var cronRunner *gateway.CronRunner
	if cfg.Cron.Enabled {
		dispatcher, err := newScheduledTaskCronDispatcher(manager, delegationTool)
		if err != nil {
			return nil, fmt.Errorf("create cron dispatcher: %w", err)
		}
		cronStore, err := gateway.NewFileCronStore(filepath.Join(dataDir, "cron", "jobs.json"))
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
			if agent == nil {
				continue
			}
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
	}

	return &gatewayLocalSessionRuntime{
		manager:    manager,
		store:      store,
		delegation: delegationTool,
		cron:       cronRunner,
	}, nil
}
