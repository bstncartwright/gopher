package gateway

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	sessionrt "github.com/bstncartwright/gopher/pkg/session"
	"github.com/bstncartwright/gopher/pkg/transport"
)

type flakySubscribeManager struct {
	sessionrt.SessionManager
	failFirst bool
}

func (m *flakySubscribeManager) Subscribe(ctx context.Context, sessionID sessionrt.SessionID) (<-chan sessionrt.Event, error) {
	if m.failFirst {
		m.failFirst = false
		return nil, fmt.Errorf("subscribe failed")
	}
	return m.SessionManager.Subscribe(ctx, sessionID)
}

type delayedSendManager struct {
	sessionrt.SessionManager
	block chan struct{}
}

func (m *delayedSendManager) SendEvent(ctx context.Context, e sessionrt.Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.block:
	}
	return m.SessionManager.SendEvent(ctx, e)
}

type sendEventFailManager struct {
	sessionrt.SessionManager
	err error
}

func (m *sendEventFailManager) SendEvent(context.Context, sessionrt.Event) error {
	if m.err != nil {
		return m.err
	}
	return context.DeadlineExceeded
}

type fakeTransport struct {
	mu      sync.Mutex
	handler transport.InboundHandler
	sent    []transport.OutboundMessage
}

func (f *fakeTransport) Start(context.Context) error { return nil }
func (f *fakeTransport) Stop() error                 { return nil }
func (f *fakeTransport) SetInboundHandler(handler transport.InboundHandler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}
func (f *fakeTransport) SendMessage(_ context.Context, message transport.OutboundMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, message)
	return nil
}
func (f *fakeTransport) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}
func (f *fakeTransport) lastSent() transport.OutboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return transport.OutboundMessage{}
	}
	return f.sent[len(f.sent)-1]
}

type dmStaticExecutor struct {
	text string
}

func (e *dmStaticExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: e.text,
				},
			},
		},
	}, nil
}

type dmErrorExecutor struct {
	message string
}

func (e *dmErrorExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventError,
				Payload: sessionrt.ErrorPayload{
					Message: e.message,
				},
			},
		},
	}, nil
}

type dmFailThenSucceedExecutor struct {
	mu    sync.Mutex
	calls int
}

func (e *dmFailThenSucceedExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()
	if call == 1 {
		return sessionrt.AgentOutput{}, fmt.Errorf("boom")
	}
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: "ack",
				},
			},
		},
	}, nil
}

func TestDMPipelineRoutesInboundToAgentAndOutbound(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
	got := fake.lastSent()
	if got.ConversationID != "!dm:one" || got.Text != "ack" {
		t.Fatalf("outbound message = %#v, want room ack", got)
	}

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@user:hs",
		Text:           "next",
	}); err != nil {
		t.Fatalf("HandleInbound(second) error: %v", err)
	}

	sessions, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(sessions))
	}
}

func TestDMPipelineDoesNotCreateDuplicateSessionsForSameConversation(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pipeline.HandleInbound(ctx, transport.InboundMessage{
				ConversationID: "!dm:race",
				SenderID:       "@user:hs",
				Text:           "hello",
			})
		}()
	}
	wg.Wait()

	sessions, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(sessions))
	}
}

func TestDMPipelineRecoversAfterInitialSubscribeFailure(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	baseManager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	manager := &flakySubscribeManager{SessionManager: baseManager, failFirst: true}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	firstErr := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:retry",
		SenderID:       "@user:hs",
		Text:           "first",
	})
	if firstErr == nil {
		t.Fatalf("expected first inbound to fail due to subscribe failure")
	}
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:retry",
		SenderID:       "@user:hs",
		Text:           "second",
	}); err != nil {
		t.Fatalf("second inbound should recover, got error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
}

func TestDMPipelineHandleInboundReturnsBeforeSlowSendCompletes(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	baseManager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	manager := &delayedSendManager{
		SessionManager: baseManager,
		block:          make(chan struct{}),
	}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:slow",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("HandleInbound() took too long: %s", elapsed)
	}

	close(manager.block)
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
}

func TestDMPipelineRebindsConversationAfterSessionFailure(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmFailThenSucceedExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:recover",
		SenderID:       "@user:hs",
		Text:           "first",
	}); err != nil {
		t.Fatalf("first HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		sessions, listErr := store.ListSessions(context.Background())
		if listErr != nil || len(sessions) == 0 {
			return false
		}
		return sessions[0].Status == sessionrt.SessionFailed
	})

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:recover",
		SenderID:       "@user:hs",
		Text:           "second",
	}); err != nil {
		t.Fatalf("second HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})

	sessions, err := store.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) < 2 {
		t.Fatalf("session count = %d, want >= 2 after rebinding", len(sessions))
	}
	active := 0
	for _, session := range sessions {
		if session.Status == sessionrt.SessionActive {
			active++
		}
	}
	if active == 0 {
		t.Fatalf("expected at least one active session after rebinding")
	}
}

func TestDMPipelineSendsFallbackOnAgentErrorEvent(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmErrorExecutor{message: "429 status code"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:error",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
	got := fake.lastSent()
	if got.ConversationID != "!dm:error" {
		t.Fatalf("conversation id = %q, want !dm:error", got.ConversationID)
	}
	if got.Text != dmRateLimitFallbackReply {
		t.Fatalf("fallback reply = %q, want %q", got.Text, dmRateLimitFallbackReply)
	}
}

func TestDMPipelineSendsFallbackWhenSendEventFails(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	baseManager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	manager := &sendEventFailManager{
		SessionManager: baseManager,
		err:            context.DeadlineExceeded,
	}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:timeout",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
	got := fake.lastSent()
	if got.ConversationID != "!dm:timeout" {
		t.Fatalf("conversation id = %q, want !dm:timeout", got.ConversationID)
	}
	if got.Text != dmErrorFallbackReply {
		t.Fatalf("fallback reply = %q, want %q", got.Text, dmErrorFallbackReply)
	}
}

func TestDMPipelineIgnoresStaleSessionResponses(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "old-ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	createdA, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "agent:a", Type: sessionrt.ActorAgent},
			{ID: "human:a", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession(A) error: %v", err)
	}
	createdB, err := manager.CreateSession(ctx, sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "agent:a", Type: sessionrt.ActorAgent},
			{ID: "human:b", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession(B) error: %v", err)
	}

	conversationID := "!dm:stale"
	if err := pipeline.ensureSubscription(conversationID, createdA.ID); err != nil {
		t.Fatalf("ensureSubscription(A) error: %v", err)
	}
	pipeline.conversations.Set(conversationID, createdA.ID)
	pipeline.conversations.Set(conversationID, createdB.ID)

	if err := manager.SendEvent(ctx, sessionrt.Event{
		SessionID: createdA.ID,
		From:      "human:a",
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "hello",
		},
	}); err != nil {
		t.Fatalf("SendEvent(A) error: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	if fake.sentCount() != 0 {
		t.Fatalf("expected no outbound from stale session, got %d", fake.sentCount())
	}
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for condition")
}
