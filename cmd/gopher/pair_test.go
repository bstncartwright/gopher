package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bstncartwright/gopher/pkg/transport"
)

type fakePairingPipeline struct {
	calls int
	err   error
}

func (f *fakePairingPipeline) HandleInbound(context.Context, transport.InboundMessage) error {
	f.calls++
	return f.err
}

type fakePairingTransport struct {
	messages []transport.OutboundMessage
	err      error
}

func (f *fakePairingTransport) SendMessage(_ context.Context, outbound transport.OutboundMessage) error {
	f.messages = append(f.messages, outbound)
	return f.err
}

func TestProcessTelegramInboundWritesPendingAndRepliesWhenUnpaired(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	pipeline := &fakePairingPipeline{}
	bridge := &fakePairingTransport{}

	err := processTelegramInbound(
		context.Background(),
		transport.InboundMessage{
			ConversationID:   "telegram:777",
			ConversationName: "ops",
			SenderID:         "telegram-user:123",
			EventID:          "1",
			Text:             "hello",
		},
		pipeline,
		bridge,
		dataDir,
		"",
		"",
	)
	if err != nil {
		t.Fatalf("processTelegramInbound() error: %v", err)
	}
	if pipeline.calls != 0 {
		t.Fatalf("pipeline calls = %d, want 0", pipeline.calls)
	}
	if len(bridge.messages) != 1 {
		t.Fatalf("bridge send calls = %d, want 1", len(bridge.messages))
	}
	if got := bridge.messages[0].Text; got != "device needs to be paired" {
		t.Fatalf("bridge reply = %q, want %q", got, "device needs to be paired")
	}
	state, err := readTelegramPairingState(dataDir)
	if err != nil {
		t.Fatalf("readTelegramPairingState() error: %v", err)
	}
	if state.Pending == nil || state.Pending.ChatID != "777" || state.Pending.UserID != "123" {
		t.Fatalf("pending pairing state = %#v, want chat_id=777 user_id=123", state.Pending)
	}
}

func TestProcessTelegramInboundOnlyPairedChatIsHandled(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	if err := writeTelegramPairingState(dataDir, telegramPairingState{
		PairedChatID: "1234",
		PairedUserID: "10",
		Pending: &telegramPairRequest{
			ChatID: "9999",
			UserID: "10",
		},
	}); err != nil {
		t.Fatalf("writeTelegramPairingState() error: %v", err)
	}
	pipeline := &fakePairingPipeline{}
	bridge := &fakePairingTransport{}

	// Different chat should be ignored, even when pairing is configured.
	if err := processTelegramInbound(
		context.Background(),
		transport.InboundMessage{
			ConversationID:   "telegram:777",
			ConversationName: "ops",
			SenderID:         "telegram-user:10",
			EventID:          "1",
			Text:             "wrong chat",
		},
		pipeline,
		bridge,
		dataDir,
		"",
		"",
	); err != nil {
		t.Fatalf("processTelegramInbound(wrong chat) error: %v", err)
	}
	if pipeline.calls != 0 || len(bridge.messages) != 0 {
		t.Fatalf("unexpected actions on wrong chat: pipeline=%d messages=%d", pipeline.calls, len(bridge.messages))
	}

	// Matching chat should reach pipeline and clear pending.
	if err := processTelegramInbound(
		context.Background(),
		transport.InboundMessage{
			ConversationID:   "telegram:1234",
			ConversationName: "ops",
			SenderID:         "telegram-user:10",
			EventID:          "2",
			Text:             "right chat",
		},
		pipeline,
		bridge,
		dataDir,
		"",
		"",
	); err != nil {
		t.Fatalf("processTelegramInbound(paired chat) error: %v", err)
	}
	if pipeline.calls != 1 {
		t.Fatalf("pipeline calls = %d, want 1", pipeline.calls)
	}
	state, err := readTelegramPairingState(dataDir)
	if err != nil {
		t.Fatalf("readTelegramPairingState() error: %v", err)
	}
	if state.Pending != nil {
		t.Fatalf("pending should be cleared, got %#v", state.Pending)
	}
}

func TestHandleTelegramInboundWithErrorReplySuppressesUserMessage(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	if err := writeTelegramPairingState(dataDir, telegramPairingState{
		PairedChatID: "777",
		PairedUserID: "123",
	}); err != nil {
		t.Fatalf("writeTelegramPairingState() error: %v", err)
	}
	pipeline := &fakePairingPipeline{err: errors.New("create session: timeout")}
	bridge := &fakePairingTransport{}

	err := handleTelegramInboundWithErrorReply(
		context.Background(),
		transport.InboundMessage{
			ConversationID:   "telegram:777",
			ConversationName: "ops",
			SenderID:         "telegram-user:123",
			EventID:          "1",
			Text:             "hello",
		},
		pipeline,
		bridge,
		dataDir,
		"",
		"",
	)
	if err != nil {
		t.Fatalf("handleTelegramInboundWithErrorReply() error: %v", err)
	}
	if pipeline.calls != 1 {
		t.Fatalf("pipeline calls = %d, want 1", pipeline.calls)
	}
	if len(bridge.messages) != 0 {
		t.Fatalf("bridge send calls = %d, want 0", len(bridge.messages))
	}
}

func TestRunPairStatusAndApprove(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	dataDir := resolveGatewayDataDir(workspace)
	if err := writeTelegramPairingState(dataDir, telegramPairingState{
		PairedChatID: "2002",
		PairedUserID: "1001",
		Pending: &telegramPairRequest{
			ChatID:         "3003",
			UserID:         "111",
			Conversation:   "Ops Chat",
			SenderUsername: "ops_user",
			RequestedAt:    "2026-01-01T00:00:00Z",
			LastSeenAt:     "2026-01-01T00:00:01Z",
		},
	}); err != nil {
		t.Fatalf("writeTelegramPairingState() error: %v", err)
	}

	var statusOut bytes.Buffer
	if err := runPairStatusSubcommand([]string{"--workspace", workspace}, &statusOut, &statusOut); err != nil {
		t.Fatalf("runPairStatusSubcommand() error: %v", err)
	}
	status := statusOut.String()
	if !contains(status, "paired chat id: 2002") {
		t.Fatalf("pair status missing active chat: %q", status)
	}
	if !contains(status, "pending pair request:") {
		t.Fatalf("pair status missing pending block: %q", status)
	}
	if !contains(status, "chat id:   3003") {
		t.Fatalf("pair status missing pending chat: %q", status)
	}

	var approveOut bytes.Buffer
	if err := runPairApproveSubcommand([]string{"--workspace", workspace}, &approveOut, &approveOut); err != nil {
		t.Fatalf("runPairApproveSubcommand() error: %v", err)
	}
	if !contains(approveOut.String(), "approved telegram pairing to chat id 3003") {
		t.Fatalf("pair approve output unexpected: %q", approveOut.String())
	}

	state, err := readTelegramPairingState(dataDir)
	if err != nil {
		t.Fatalf("readTelegramPairingState() error: %v", err)
	}
	if state.PairedChatID != "3003" {
		t.Fatalf("paired chat id = %q, want 3003", state.PairedChatID)
	}
	if state.Pending != nil {
		t.Fatalf("pending should be cleared after approve, got %#v", state.Pending)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
