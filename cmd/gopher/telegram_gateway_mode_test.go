package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bstncartwright/gopher/pkg/config"
	"github.com/bstncartwright/gopher/pkg/transport"
	telegramtransport "github.com/bstncartwright/gopher/pkg/transport/telegram"
)

func TestTelegramBridgeStartIngressPollingMode(t *testing.T) {
	tr := &fakeTelegramBridgeTransport{
		startCalled: make(chan struct{}),
	}
	bridge := &telegramDMBridge{transport: tr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := bridge.startIngress(ctx, config.TelegramConfig{Mode: "polling"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("startIngress() error: %v", err)
	}
	waitForSignal(t, tr.startCalled)
	if tr.deleteWebhookCalls != 1 {
		t.Fatalf("delete webhook calls = %d, want 1", tr.deleteWebhookCalls)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
}

func TestTelegramBridgeStartIngressWebhookMode(t *testing.T) {
	tr := &fakeTelegramBridgeTransport{
		startCalled: make(chan struct{}),
	}
	webhook := &fakeTelegramWebhookRuntime{runCalled: make(chan struct{})}
	originalBuilder := buildTelegramWebhookRuntime
	buildTelegramWebhookRuntime = func(opts telegramWebhookServerOptions) (telegramWebhookRuntime, error) {
		return webhook, nil
	}
	defer func() {
		buildTelegramWebhookRuntime = originalBuilder
	}()

	bridge := &telegramDMBridge{transport: tr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := bridge.startIngress(ctx, config.TelegramConfig{
		Mode: "webhook",
		Webhook: config.TelegramWebhookConfig{
			ListenAddr: "127.0.0.1:29330",
			Path:       "/_gopher/telegram/webhook",
			URL:        "https://example.ts.net/_gopher/telegram/webhook",
			Secret:     "secret",
		},
	}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("startIngress() error: %v", err)
	}
	waitForSignal(t, webhook.runCalled)
	if tr.setWebhookCalls != 1 {
		t.Fatalf("set webhook calls = %d, want 1", tr.setWebhookCalls)
	}
	if tr.deleteWebhookCalls != 0 {
		t.Fatalf("delete webhook calls = %d, want 0", tr.deleteWebhookCalls)
	}
	cancel()
}

func TestTelegramBridgeStartIngressWebhookModeStopsServerOnSetWebhookError(t *testing.T) {
	tr := &fakeTelegramBridgeTransport{
		startCalled:   make(chan struct{}),
		setWebhookErr: fmt.Errorf("boom"),
	}
	webhook := &fakeTelegramWebhookRuntime{runCalled: make(chan struct{})}
	originalBuilder := buildTelegramWebhookRuntime
	buildTelegramWebhookRuntime = func(opts telegramWebhookServerOptions) (telegramWebhookRuntime, error) {
		return webhook, nil
	}
	defer func() {
		buildTelegramWebhookRuntime = originalBuilder
	}()

	bridge := &telegramDMBridge{transport: tr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := bridge.startIngress(ctx, config.TelegramConfig{
		Mode: "webhook",
		Webhook: config.TelegramWebhookConfig{
			ListenAddr: "127.0.0.1:29330",
			Path:       "/_gopher/telegram/webhook",
			URL:        "https://example.ts.net/_gopher/telegram/webhook",
			Secret:     "secret",
		},
	}, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatalf("expected startIngress() error")
	}
	if webhook.stopCalls != 1 {
		t.Fatalf("webhook stop calls = %d, want 1", webhook.stopCalls)
	}
}

func TestStartTelegramDMBridgeAssignsSessionMemoryFlusher(t *testing.T) {
	workspace := t.TempDir()
	createGatewayTestAgentWorkspace(t, filepath.Join(workspace, "agents", "main"), "main")
	runtime, err := loadGatewayAgentRuntime(workspace)
	if err != nil {
		t.Fatalf("loadGatewayAgentRuntime() error: %v", err)
	}

	fakeTransport := &fakeTelegramBridgeTransport{startCalled: make(chan struct{})}
	prevFactory := newTelegramBridgeTransport
	newTelegramBridgeTransport = func(opts telegramtransport.Options) (telegramBridgeTransport, error) {
		return fakeTransport, nil
	}
	defer func() {
		newTelegramBridgeTransport = prevFactory
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge, err := startTelegramDMBridgeWithRuntime(ctx, config.GatewayConfig{
		Telegram: config.TelegramConfig{
			Enabled:      true,
			Mode:         "polling",
			BotToken:     "token",
			PollInterval: 10 * time.Millisecond,
			PollTimeout:  time.Second,
		},
		Cron: config.CronConfig{Enabled: false},
	}, workspace, runtime, runtime.Executor, nil, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("startTelegramDMBridgeWithRuntime() error: %v", err)
	}
	defer bridge.Stop()

	for actorID, agent := range runtime.Agents {
		if agent.SessionMemoryFlusher == nil {
			t.Fatalf("expected session memory flusher for agent %q", actorID)
		}
	}
}

type fakeTelegramBridgeTransport struct {
	mu sync.Mutex

	startCalled        chan struct{}
	setWebhookErr      error
	deleteWebhookErr   error
	deleteWebhookCalls int
	setWebhookCalls    int
}

func (f *fakeTelegramBridgeTransport) Start(ctx context.Context) error {
	select {
	case <-f.startCalled:
	default:
		close(f.startCalled)
	}
	<-ctx.Done()
	return nil
}

func (f *fakeTelegramBridgeTransport) Stop() error {
	return nil
}

func (f *fakeTelegramBridgeTransport) SetInboundHandler(handler transport.InboundHandler) {}

func (f *fakeTelegramBridgeTransport) SendMessage(ctx context.Context, message transport.OutboundMessage) error {
	return nil
}

func (f *fakeTelegramBridgeTransport) SendTyping(ctx context.Context, conversationID string, typing bool) error {
	return nil
}

func (f *fakeTelegramBridgeTransport) SetCommands(ctx context.Context, commands []telegramtransport.BotCommand) error {
	return nil
}

func (f *fakeTelegramBridgeTransport) SetWebhook(ctx context.Context, webhookURL, secret string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setWebhookCalls++
	return f.setWebhookErr
}

func (f *fakeTelegramBridgeTransport) DeleteWebhook(ctx context.Context, dropPending bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleteWebhookCalls++
	return f.deleteWebhookErr
}

func (f *fakeTelegramBridgeTransport) HandleWebhookUpdate(ctx context.Context, payload []byte) error {
	return nil
}

type fakeTelegramWebhookRuntime struct {
	runCalled chan struct{}
	stopCalls int
}

func (f *fakeTelegramWebhookRuntime) RunWithRetry(ctx context.Context) error {
	select {
	case <-f.runCalled:
	default:
		close(f.runCalled)
	}
	<-ctx.Done()
	return nil
}

func (f *fakeTelegramWebhookRuntime) Stop() error {
	f.stopCalls++
	return nil
}

func waitForSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for signal")
	}
}
