package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bstncartwright/gopher/pkg/agentcore"
	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/gateway"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	storepkg "github.com/bstncartwright/gopher/pkg/store"
	"github.com/bstncartwright/gopher/pkg/transport"
	telegramtransport "github.com/bstncartwright/gopher/pkg/transport/telegram"
	"github.com/pelletier/go-toml/v2"
)

type telegramDMBridge struct {
	transport telegramBridgeTransport
	mode      string
	manager   sessionrt.SessionManager
	pipeline  *gateway.DMPipeline
	cron      *gateway.CronRunner
	heartbeat *gateway.HeartbeatRunner
	webhook   telegramWebhookRuntime
	bindings  gateway.ConversationBindingStore
	store     interface {
		sessionrt.EventStore
		sessionrt.SessionRegistryStore
	}
	cancel context.CancelFunc
}

type telegramBridgeTransport interface {
	transport.Transport
	SetCommands(context.Context, []telegramtransport.BotCommand) error
	SetWebhook(context.Context, string, string) error
	DeleteWebhook(context.Context, bool) error
	HandleWebhookUpdate(context.Context, []byte) error
}

var buildTelegramWebhookRuntime = func(opts telegramWebhookServerOptions) (telegramWebhookRuntime, error) {
	return newTelegramWebhookServer(opts)
}

var newTelegramBridgeTransport = func(opts telegramtransport.Options) (telegramBridgeTransport, error) {
	return telegramtransport.New(opts)
}

func startTelegramDMBridgeWithRuntime(
	ctx context.Context,
	cfg config.GatewayConfig,
	workspace string,
	agentRuntime *gatewayAgentRuntime,
	executor sessionrt.AgentExecutor,
	logger *log.Logger,
) (*telegramDMBridge, error) {
	var err error
	slog.Info("telegram_gateway: starting dm bridge", "workspace", workspace)
	slog.Info(
		"telegram_gateway: configuration",
		"telegram_enabled", true,
		"mode", cfg.Telegram.Mode,
		"poll_interval", cfg.Telegram.PollInterval,
		"poll_timeout", cfg.Telegram.PollTimeout,
		"allowed_user_id_set", cfg.Telegram.AllowedUserID != "",
		"allowed_chat_id_set", cfg.Telegram.AllowedChatID != "",
		"webhook_listen_addr", cfg.Telegram.Webhook.ListenAddr,
		"webhook_path", cfg.Telegram.Webhook.Path,
		"webhook_url_set", strings.TrimSpace(cfg.Telegram.Webhook.URL) != "",
	)
	if agentRuntime == nil {
		agentRuntime, err = loadGatewayAgentRuntime(workspace)
		if err != nil {
			return nil, fmt.Errorf("load gateway agents: %w", err)
		}
	}
	slog.Info("telegram_gateway: runtime loaded", "runtime_agents", len(agentRuntime.Agents))
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
		AgentSelector:  gatewayMessageTargetSelector(agentRuntime.DefaultActorID),
		RecoverOnStart: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create session manager: %w", err)
	}
	slog.Info(
		"telegram_gateway: session manager created",
		"agent_default_actor_id", agentRuntime.DefaultActorID,
		"recover_on_start", true,
	)
	for _, agent := range agentRuntime.Agents {
		if agent == nil || agent.LongTermMemory == nil {
			continue
		}
		agent.SessionMemoryFlusher = agentcore.NewStoreBackedSessionMemoryFlusher(store, agent.LongTermMemory, agent.ID)
	}

	delegationTool := newGatewaySessionDelegationToolService(manager, store, agentRuntime.Agents, dataDir, logger, agentRuntime.Router)
	for _, agent := range agentRuntime.Agents {
		agent.Delegation = delegationTool
	}

	var cronRunner *gateway.CronRunner
	if cfg.Cron.Enabled {
		dispatcher, err := newScheduledTaskCronDispatcher(manager, delegationTool)
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
	}

	telegramBridge, err := newTelegramBridgeTransport(telegramtransport.Options{
		BotToken:      cfg.Telegram.BotToken,
		PollInterval:  cfg.Telegram.PollInterval,
		PollTimeout:   cfg.Telegram.PollTimeout,
		AllowedUserID: cfg.Telegram.AllowedUserID,
		AllowedChatID: cfg.Telegram.AllowedChatID,
		OffsetPath:    filepath.Join(dataDir, "telegram", "offset.json"),
	})
	if err != nil {
		return nil, fmt.Errorf("create telegram transport: %w", err)
	}
	slog.Info("telegram_gateway: telegram transport initialized", "offset_path", filepath.Join(dataDir, "telegram", "offset.json"))
	var pipeline *gateway.DMPipeline
	modelPolicyCommand := func(cmdCtx context.Context, req gateway.ModelPolicyCommandRequest) (gateway.ModelPolicyCommandResult, error) {
		return handleTelegramModelPolicyCommand(cmdCtx, workspace, agentRuntime, req, func(waitCtx context.Context) error {
			if pipeline == nil {
				return nil
			}
			return pipeline.WaitForOutboundIdle(waitCtx)
		}, logger)
	}
	registerCtx, registerCancel := context.WithTimeout(ctx, 5*time.Second)
	if err := telegramBridge.SetCommands(registerCtx, []telegramtransport.BotCommand{
		{Command: "status", Description: "Show session and context status"},
		{Command: "context", Description: "Context commands: clear or summarize"},
		{Command: "trace", Description: "Trace commands: on/off/status"},
		{Command: "model", Description: "Model commands: status or set provider:model"},
	}); err != nil {
		slog.Warn("telegram_gateway: register telegram bot commands failed", "error", err)
	}
	registerCancel()

	pipeline, err = gateway.NewDMPipeline(gateway.DMPipelineOptions{
		Manager:            manager,
		Transport:          telegramBridge,
		EventStore:         store,
		AgentID:            agentRuntime.DefaultActorID,
		Conversations:      gateway.NewConversationSessionMap(),
		Bindings:           bindingStore,
		ModelPolicyCommand: modelPolicyCommand,
	})
	if err != nil {
		return nil, fmt.Errorf("create telegram dm pipeline: %w", err)
	}

	messageTool := newGatewayMessageToolService(pipeline, telegramBridge)
	for _, agent := range agentRuntime.Agents {
		agent.MessageService = messageTool
		agent.ReactionService = messageTool
	}

	heartbeatSchedules := collectHeartbeatSchedules(agentRuntime)
	heartbeatRunner, err := gateway.NewHeartbeatRunner(gateway.HeartbeatRunnerOptions{
		Manager:   manager,
		Pipeline:  pipeline,
		Schedules: heartbeatSchedules,
	})
	if err != nil {
		return nil, fmt.Errorf("create heartbeat runner: %w", err)
	}
	if err := heartbeatRunner.Start(ctx); err != nil {
		return nil, fmt.Errorf("start heartbeat runner: %w", err)
	}
	heartbeatTool := newGatewayHeartbeatToolService(agentRuntime.Agents, heartbeatRunner)
	for _, agent := range agentRuntime.Agents {
		agent.HeartbeatService = heartbeatTool
	}

	// Wrap inbound processing with durable telegram ingress audit logging.
	telegramBridge.SetInboundHandler(func(handlerCtx context.Context, inbound transport.InboundMessage) error {
		slog.Info(
			"telegram_gateway: inbound handler received message",
			"conversation_id", inbound.ConversationID,
			"sender_id", inbound.SenderID,
			"event_id", inbound.EventID,
		)
		recordTelegramInbound(dataDir, inbound)
		return handleTelegramInboundWithErrorReply(handlerCtx, inbound, pipeline, telegramBridge, dataDir, cfg.Telegram.AllowedChatID, cfg.Telegram.AllowedUserID)
	})

	bridge := &telegramDMBridge{
		transport: telegramBridge,
		mode:      normalizeTelegramMode(cfg.Telegram.Mode),
		manager:   manager,
		pipeline:  pipeline,
		cron:      cronRunner,
		heartbeat: heartbeatRunner,
		bindings:  bindingStore,
		store:     store,
	}
	bridgeCtx, cancel := context.WithCancel(ctx)
	bridge.cancel = cancel
	if err := bridge.startIngress(bridgeCtx, cfg.Telegram, logger); err != nil {
		cancel()
		return nil, err
	}
	return bridge, nil
}

func processTelegramInbound(
	ctx context.Context,
	inbound transport.InboundMessage,
	pipeline telegramPairingPipeline,
	bridge telegramPairingTransport,
	dataDir string,
	defaultPairedChatID string,
	defaultPairedUserID string,
) error {
	chatID := parseTelegramConversationID(inbound.ConversationID)
	userID := parseTelegramSenderID(inbound.SenderID)
	if chatID == "" || userID == "" {
		return nil
	}

	state, err := readTelegramPairingState(dataDir)
	if err != nil {
		return err
	}

	effectiveChatID := strings.TrimSpace(state.PairedChatID)
	effectiveUserID := strings.TrimSpace(state.PairedUserID)
	if effectiveChatID == "" {
		effectiveChatID = strings.TrimSpace(defaultPairedChatID)
	}
	if effectiveUserID == "" {
		effectiveUserID = strings.TrimSpace(defaultPairedUserID)
	}
	if effectiveUserID != "" && userID != effectiveUserID {
		return nil
	}
	if state.Pending != nil && effectiveChatID != "" {
		state.Pending = nil
		if err := writeTelegramPairingState(dataDir, state); err != nil {
			return err
		}
	}

	if effectiveChatID == "" {
		if err := upsertPendingTelegramPairing(dataDir, telegramPairRequest{
			ChatID:         chatID,
			UserID:         userID,
			Conversation:   inbound.ConversationName,
			SenderUsername: strings.TrimPrefix(inbound.SenderID, "telegram-user:"),
			RequestedAt:    "",
			LastSeenAt:     "",
		}); err != nil {
			return err
		}
		return bridge.SendMessage(ctx, transport.OutboundMessage{
			ConversationID: inbound.ConversationID,
			Text:           "device needs to be paired",
		})
	}

	if chatID != effectiveChatID {
		return nil
	}
	return pipeline.HandleInbound(ctx, inbound)
}

func handleTelegramInboundWithErrorReply(
	ctx context.Context,
	inbound transport.InboundMessage,
	pipeline telegramPairingPipeline,
	bridge telegramPairingTransport,
	dataDir string,
	defaultPairedChatID string,
	defaultPairedUserID string,
) error {
	err := processTelegramInbound(ctx, inbound, pipeline, bridge, dataDir, defaultPairedChatID, defaultPairedUserID)
	if err == nil {
		return nil
	}
	slog.Error(
		"telegram_gateway: failed to process inbound message",
		"conversation_id", inbound.ConversationID,
		"sender_id", inbound.SenderID,
		"event_id", inbound.EventID,
		"error", err,
	)
	// Swallow the handler error so this update is not retried indefinitely.
	// User-facing error fallback messages are handled by dm_pipeline only for terminal session failures.
	return nil
}

type telegramPairingPipeline interface {
	HandleInbound(context.Context, transport.InboundMessage) error
}

type telegramPairingTransport interface {
	SendMessage(context.Context, transport.OutboundMessage) error
}

func normalizeTelegramMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return "polling"
	}
	return mode
}

func (b *telegramDMBridge) startIngress(ctx context.Context, cfg config.TelegramConfig, logger *log.Logger) error {
	if b == nil || b.transport == nil {
		return fmt.Errorf("telegram transport is required")
	}
	mode := normalizeTelegramMode(cfg.Mode)
	switch mode {
	case "polling":
		if err := b.transport.DeleteWebhook(ctx, false); err != nil {
			return fmt.Errorf("delete telegram webhook before polling: %w", err)
		}
		slog.Info("telegram_gateway: polling ingress enabled")
		go b.runSupervisor(ctx, logger)
		return nil
	case "webhook":
		server, err := buildTelegramWebhookRuntime(telegramWebhookServerOptions{
			ListenAddr: cfg.Webhook.ListenAddr,
			Path:       cfg.Webhook.Path,
			Secret:     cfg.Webhook.Secret,
			HandleUpdate: func(handlerCtx context.Context, payload []byte) error {
				return b.transport.HandleWebhookUpdate(handlerCtx, payload)
			},
		})
		if err != nil {
			return fmt.Errorf("create telegram webhook server: %w", err)
		}
		b.webhook = server
		go func() {
			if runErr := server.RunWithRetry(ctx); runErr != nil && ctx.Err() == nil && logger != nil {
				logger.Printf("telegram webhook server stopped err=%v", runErr)
			}
		}()
		if err := b.transport.SetWebhook(ctx, cfg.Webhook.URL, cfg.Webhook.Secret); err != nil {
			_ = server.Stop()
			return fmt.Errorf("set telegram webhook: %w", err)
		}
		slog.Info(
			"telegram_gateway: webhook ingress enabled",
			"listen_addr", cfg.Webhook.ListenAddr,
			"path", cfg.Webhook.Path,
			"url", cfg.Webhook.URL,
		)
		return nil
	default:
		return fmt.Errorf("unsupported telegram mode %q", mode)
	}
}

func parseTelegramConversationID(conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	prefix := "telegram:"
	if !strings.HasPrefix(conversationID, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(conversationID, prefix))
}

func parseTelegramSenderID(senderID string) string {
	senderID = strings.TrimSpace(senderID)
	prefix := "telegram-user:"
	if !strings.HasPrefix(senderID, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(senderID, prefix))
}

func (b *telegramDMBridge) runSupervisor(ctx context.Context, logger *log.Logger) {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		attempt++
		if logger != nil {
			logger.Printf("telegram bridge start attempt=%d", attempt)
		}
		err := b.transport.Start(ctx)
		if err == nil {
			if ctx.Err() != nil {
				return
			}
		} else if logger != nil {
			logger.Printf("telegram bridge degraded attempt=%d err=%v", attempt, err)
			logger.Printf(
				"telegram bridge retrying in %s (attempt=%d)",
				channelBridgeRetryDelay(attempt),
				attempt,
			)
		}
		if ctx.Err() != nil {
			return
		}
		delay := channelBridgeRetryDelay(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func channelBridgeRetryDelay(attempt int) time.Duration {
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

func (b *telegramDMBridge) Stop() {
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
	if b.webhook != nil {
		_ = b.webhook.Stop()
	}
	if b.transport != nil {
		_ = b.transport.Stop()
	}
}

func recordTelegramInbound(dataDir string, inbound transport.InboundMessage) {
	dataDir = filepath.Clean(strings.TrimSpace(dataDir))
	if dataDir == "" {
		return
	}
	path := filepath.Join(dataDir, "control", "inbound_telegram.jsonl")
	payload := map[string]any{
		"ts":                 time.Now().UTC().Format(time.RFC3339Nano),
		"actor":              "human_telegram",
		"conversation_id":    inbound.ConversationID,
		"conversation_name":  inbound.ConversationName,
		"sender_id":          inbound.SenderID,
		"event_id":           inbound.EventID,
		"raw_text":           inbound.Text,
		"interpreted_intent": "",
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(blob, '\n'))
}

func handleTelegramModelPolicyCommand(
	ctx context.Context,
	workspace string,
	agentRuntime *gatewayAgentRuntime,
	req gateway.ModelPolicyCommandRequest,
	waitForOutbound func(context.Context) error,
	logger *log.Logger,
) (gateway.ModelPolicyCommandResult, error) {
	agentID, configPath, err := resolveTelegramModelAgentConfigPath(workspace, agentRuntime, req.AgentID)
	if err != nil {
		return gateway.ModelPolicyCommandResult{}, err
	}

	requested := strings.TrimSpace(req.RequestedModelPolicy)
	if requested == "" {
		current, err := readAgentModelPolicy(configPath)
		if err != nil {
			return gateway.ModelPolicyCommandResult{}, err
		}
		return gateway.ModelPolicyCommandResult{
			CurrentModelPolicy: current,
		}, nil
	}
	if err := validateModelPolicy(requested); err != nil {
		return gateway.ModelPolicyCommandResult{}, err
	}

	previous, changed, err := setAgentModelPolicy(configPath, requested)
	if err != nil {
		return gateway.ModelPolicyCommandResult{}, err
	}

	if agentRuntime != nil {
		if agent, ok := agentRuntime.Agents[agentID]; ok && agent != nil {
			agent.Config.ModelPolicy = requested
		}
	}

	result := gateway.ModelPolicyCommandResult{
		CurrentModelPolicy:  requested,
		PreviousModelPolicy: previous,
		Updated:             changed,
	}
	if changed {
		result.RestartScheduled = scheduleGatewayRestartAfterOutbound(waitForOutbound, logger)
	}
	return result, nil
}

func resolveTelegramModelAgentConfigPath(workspace string, runtime *gatewayAgentRuntime, requestedAgentID sessionrt.ActorID) (sessionrt.ActorID, string, error) {
	agentID := sessionrt.ActorID(strings.TrimSpace(string(requestedAgentID)))
	if strings.TrimSpace(string(agentID)) == "" && runtime != nil {
		agentID = runtime.DefaultActorID
	}
	if strings.TrimSpace(string(agentID)) == "" {
		agentID = sessionrt.ActorID(defaultAgentWorkspaceID)
	}

	if runtime != nil {
		if agent, ok := runtime.Agents[agentID]; ok && agent != nil {
			path, err := resolveRuntimeConfigPath(agent.Workspace)
			if err == nil {
				return agentID, path, nil
			}
		}
	}

	workspace = filepath.Clean(strings.TrimSpace(workspace))
	if workspace == "" {
		return "", "", fmt.Errorf("workspace is required")
	}
	candidate := filepath.Join(workspace, "agents", strings.TrimSpace(string(agentID)))
	path, err := resolveRuntimeConfigPath(candidate)
	if err != nil {
		return "", "", fmt.Errorf("resolve config path for agent %q: %w", agentID, err)
	}
	return agentID, path, nil
}

func validateModelPolicy(raw string) error {
	raw = strings.TrimSpace(raw)
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid model policy %q: expected provider:model", raw)
	}
	if strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("invalid model policy %q: provider and model are required", raw)
	}
	return nil
}

func readAgentModelPolicy(configPath string) (string, error) {
	doc, err := readAgentConfigDoc(configPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(readRuntimeStringField(doc, "model_policy")), nil
}

func setAgentModelPolicy(configPath, modelPolicy string) (previous string, changed bool, err error) {
	doc, err := readAgentConfigDoc(configPath)
	if err != nil {
		return "", false, err
	}
	previous = strings.TrimSpace(readRuntimeStringField(doc, "model_policy"))
	modelPolicy = strings.TrimSpace(modelPolicy)
	if previous == modelPolicy {
		return previous, false, nil
	}
	doc["model_policy"] = modelPolicy
	updated, err := encodeAgentConfigDoc(configPath, doc)
	if err != nil {
		return "", false, err
	}
	if err := writeConfigFileWithBackup(configPath, updated); err != nil {
		return "", false, err
	}
	return previous, true, nil
}

func readAgentConfigDoc(configPath string) (map[string]any, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	blob, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read agent config %s: %w", configPath, err)
	}
	doc := map[string]any{}
	switch strings.ToLower(filepath.Ext(configPath)) {
	case ".toml":
		if err := toml.Unmarshal(blob, &doc); err != nil {
			return nil, fmt.Errorf("parse agent config %s: %w", configPath, err)
		}
	case ".json":
		if err := json.Unmarshal(blob, &doc); err != nil {
			return nil, fmt.Errorf("parse agent config %s: %w", configPath, err)
		}
	default:
		return nil, fmt.Errorf("unsupported agent config format %q", filepath.Ext(configPath))
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

func encodeAgentConfigDoc(configPath string, doc map[string]any) ([]byte, error) {
	switch strings.ToLower(filepath.Ext(configPath)) {
	case ".toml":
		updated, err := toml.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("serialize agent config %s: %w", configPath, err)
		}
		return updated, nil
	case ".json":
		updated, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("serialize agent config %s: %w", configPath, err)
		}
		return append(updated, '\n'), nil
	default:
		return nil, fmt.Errorf("unsupported agent config format %q", filepath.Ext(configPath))
	}
}

func scheduleGatewayRestartAfterOutbound(waitForOutbound func(context.Context) error, logger *log.Logger) bool {
	go func() {
		waitCtx, cancelWait := context.WithTimeout(context.Background(), 12*time.Second)
		if waitForOutbound != nil {
			if err := waitForOutbound(waitCtx); err != nil && waitCtx.Err() == nil {
				slog.Warn("telegram_gateway: outbound idle wait before restart failed", "error", err)
			}
		}
		cancelWait()

		time.Sleep(250 * time.Millisecond)

		restartCtx, cancelRestart := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelRestart()
		if err := triggerGatewayRestartCommand(restartCtx); err != nil {
			slog.Warn("telegram_gateway: scheduled restart command failed", "error", err)
			if logger != nil {
				logger.Printf("scheduled restart command failed err=%v", err)
			}
		}
	}()
	return true
}

func triggerGatewayRestartCommand(ctx context.Context) error {
	candidates := []string{}
	if executablePath, err := os.Executable(); err == nil {
		executablePath = strings.TrimSpace(executablePath)
		if executablePath != "" {
			candidates = append(candidates, executablePath)
		}
	}
	candidates = append(candidates, "gopher")

	var failures []string
	seen := map[string]struct{}{}
	for _, bin := range candidates {
		bin = strings.TrimSpace(bin)
		if bin == "" {
			continue
		}
		if _, ok := seen[bin]; ok {
			continue
		}
		seen[bin] = struct{}{}
		cmd := exec.CommandContext(ctx, bin, "restart")
		output, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		trimmedOutput := strings.TrimSpace(string(output))
		if trimmedOutput != "" {
			failures = append(failures, fmt.Sprintf("%s: %v (%s)", bin, err, trimmedOutput))
		} else {
			failures = append(failures, fmt.Sprintf("%s: %v", bin, err))
		}
	}
	if len(failures) == 0 {
		return fmt.Errorf("no restart command candidates available")
	}
	return fmt.Errorf("restart failed: %s", strings.Join(failures, "; "))
}
