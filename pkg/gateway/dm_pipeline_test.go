package gateway

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	mu                  sync.Mutex
	handler             transport.InboundHandler
	sent                []transport.OutboundMessage
	typing              []typingSignal
	managed             map[string][]string
	traceInboundIgnored int
}

type typingSignal struct {
	ConversationID string
	SenderID       string
	Typing         bool
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
func (f *fakeTransport) SendTyping(_ context.Context, conversationID string, typing bool) error {
	return f.SendTypingAs(context.Background(), conversationID, "", typing)
}
func (f *fakeTransport) SendTypingAs(_ context.Context, conversationID, senderID string, typing bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing = append(f.typing, typingSignal{
		ConversationID: conversationID,
		SenderID:       senderID,
		Typing:         typing,
	})
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
func (f *fakeTransport) sentMessages() []transport.OutboundMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]transport.OutboundMessage, len(f.sent))
	copy(out, f.sent)
	return out
}
func (f *fakeTransport) typingSignals() []typingSignal {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]typingSignal, len(f.typing))
	copy(out, f.typing)
	return out
}

func (f *fakeTransport) ManagedUsersForConversation(conversationID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	users := f.managed[conversationID]
	if len(users) == 0 {
		return nil
	}
	out := make([]string, len(users))
	copy(out, users)
	return out
}

func (f *fakeTransport) RecordTraceInboundIgnored() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.traceInboundIgnored++
}

func (f *fakeTransport) traceInboundIgnoredCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.traceInboundIgnored
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

type dmRecoverableErrorExecutor struct {
	errorMessage string
	replyText    string
}

func (e *dmRecoverableErrorExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventError,
				Payload: sessionrt.ErrorPayload{
					Message: e.errorMessage,
				},
			},
			{
				From: input.ActorID,
				Type: sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: e.replyText,
				},
			},
		},
	}, nil
}

type dmPromptExecutor struct {
	defaultText string
	responses   map[string]string
}

func (e *dmPromptExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	text := e.defaultText
	prompt := latestUserContent(input.History)
	if override, ok := e.responses[prompt]; ok {
		text = override
	}
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventMessage,
				Payload: sessionrt.Message{
					Role:    sessionrt.RoleAgent,
					Content: text,
				},
			},
		},
	}, nil
}

type dmTraceExecutor struct{}

func (e *dmTraceExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventToolCall,
				Payload: map[string]any{
					"name": "exec",
					"args": map[string]any{"command": "echo hi"},
				},
			},
			{
				From: input.ActorID,
				Type: sessionrt.EventToolResult,
				Payload: map[string]any{
					"name":   "exec",
					"status": "ok",
					"result": map[string]any{
						"command": "echo hi",
						"stdout":  "hi",
					},
				},
			},
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

type dmWriteTraceExecutor struct{}

func (e *dmWriteTraceExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventToolCall,
				Payload: map[string]any{
					"name": "write",
					"args": map[string]any{
						"path":    "/tmp/report.md",
						"content": "hello",
					},
				},
			},
			{
				From: input.ActorID,
				Type: sessionrt.EventToolResult,
				Payload: map[string]any{
					"name":   "write",
					"status": "ok",
					"result": map[string]any{
						"path":          "/tmp/report.md",
						"bytes_written": 5,
					},
				},
			},
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

type dmWritePathExecutor struct {
	path string
}

func (e *dmWritePathExecutor) Step(_ context.Context, input sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{
		Events: []sessionrt.Event{
			{
				From: input.ActorID,
				Type: sessionrt.EventToolResult,
				Payload: map[string]any{
					"name":   "write",
					"status": "ok",
					"result": map[string]any{
						"path": e.path,
					},
				},
			},
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

func latestUserContent(history []sessionrt.Event) string {
	for i := len(history) - 1; i >= 0; i-- {
		event := history[i]
		if event.Type != sessionrt.EventMessage {
			continue
		}
		switch payload := event.Payload.(type) {
		case sessionrt.Message:
			if payload.Role == sessionrt.RoleUser {
				return strings.TrimSpace(payload.Content)
			}
		case map[string]any:
			role, _ := payload["role"].(string)
			content, _ := payload["content"].(string)
			if role == string(sessionrt.RoleUser) {
				return strings.TrimSpace(content)
			}
		}
	}
	return ""
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

type fakeTracePublisher struct {
	mu     sync.Mutex
	events []sessionrt.Event
	rooms  []string
}

func (p *fakeTracePublisher) PublishEvent(_ context.Context, traceConversationID string, event sessionrt.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rooms = append(p.rooms, strings.TrimSpace(traceConversationID))
	p.events = append(p.events, event)
	return nil
}

func (p *fakeTracePublisher) publishedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.events)
}

func (p *fakeTracePublisher) lastRoom() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.rooms) == 0 {
		return ""
	}
	return p.rooms[len(p.rooms)-1]
}

type fakeTraceProvisioner struct {
	mu      sync.Mutex
	calls   int
	lastReq TraceConversationRequest
	result  TraceConversationBinding
	err     error
}

func (p *fakeTraceProvisioner) CreateTraceConversation(_ context.Context, req TraceConversationRequest) (TraceConversationBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastReq = req
	if p.err != nil {
		return TraceConversationBinding{}, p.err
	}
	return p.result, nil
}

func (p *fakeTraceProvisioner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
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
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 2
	})
	waitFor(t, 2*time.Second, func() bool {
		signals := fake.typingSignals()
		return len(signals) >= 2 && !signals[len(signals)-1].Typing
	})

	sessions, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(sessions))
	}

	signals := fake.typingSignals()
	if len(signals) < 2 {
		t.Fatalf("typing signal count = %d, want >= 2", len(signals))
	}
	first := signals[0]
	if first.ConversationID != "!dm:one" || !first.Typing {
		t.Fatalf("first typing signal = %#v, want typing=true for !dm:one", first)
	}
	last := signals[len(signals)-1]
	if last.ConversationID != "!dm:one" || last.Typing {
		t.Fatalf("last typing signal = %#v, want typing=false for !dm:one", last)
	}
}

func TestDMPipelineDoesNotSendProgressUpdatesDuringToolExecution(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmTraceExecutor{},
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
		ConversationID: "!dm:progress",
		SenderID:       "@user:hs",
		Text:           "run tool",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 1
	})
	messages := fake.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("sent message count = %d, want 1", len(messages))
	}
	if strings.TrimSpace(messages[0].Text) != "ack" {
		t.Fatalf("final response = %q, want ack", messages[0].Text)
	}
}

func TestDMPipelineDoesNotSendWriteProgressUpdatesToDM(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmWriteTraceExecutor{},
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
		ConversationID: "!dm:write-progress",
		SenderID:       "@user:hs",
		Text:           "run tool",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 1
	})
	messages := fake.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("sent message count = %d, want 1", len(messages))
	}
	if strings.TrimSpace(messages[0].Text) != "ack" {
		t.Fatalf("final response = %q, want ack", messages[0].Text)
	}
}

func TestDMPipelineIncludesAttachmentsFromToolResultResolver(t *testing.T) {
	workspace := t.TempDir()
	reportPath := workspace + "/report.md"
	if err := os.WriteFile(reportPath, []byte("# report\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error: %v", err)
	}

	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmWritePathExecutor{path: reportPath},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
		AttachmentResolver: func(_ string, _ sessionrt.ActorID, event sessionrt.Event) []transport.OutboundAttachment {
			payload, ok := event.Payload.(map[string]any)
			if !ok {
				return nil
			}
			result, ok := payload["result"].(map[string]any)
			if !ok {
				return nil
			}
			pathValue, _ := result["path"].(string)
			pathValue = strings.TrimSpace(pathValue)
			if pathValue == "" {
				return nil
			}
			return []transport.OutboundAttachment{{Path: pathValue}}
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:attachments",
		SenderID:       "@user:hs",
		Text:           "write a report",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 1
	})
	message := fake.lastSent()
	if strings.TrimSpace(message.Text) != "ack" {
		t.Fatalf("final response = %q, want ack", message.Text)
	}
	if len(message.Attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1", len(message.Attachments))
	}
	if message.Attachments[0].Path != reportPath {
		t.Fatalf("attachment path = %q, want %q", message.Attachments[0].Path, reportPath)
	}
}

func TestDMPipelineCreatesTraceConversationAndPublishesTraceEvents(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmTraceExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	tracePublisher := &fakeTracePublisher{}
	traceProvisioner := &fakeTraceProvisioner{
		result: TraceConversationBinding{
			ConversationID:   "!trace:one",
			ConversationName: "trace-sess",
			Mode:             TraceModeReadOnly,
			Render:           TraceRenderCards,
		},
	}
	bindings := NewInMemoryConversationBindingStore()
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:          manager,
		Transport:        fake,
		AgentID:          "agent:a",
		Bindings:         bindings,
		TracePublisher:   tracePublisher,
		TraceProvisioner: traceProvisioner,
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		EventID:        "$dm-event-1",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 2
	})
	waitFor(t, 2*time.Second, func() bool {
		return tracePublisher.publishedCount() >= 3
	})

	if traceProvisioner.callCount() != 1 {
		t.Fatalf("trace provisioner call count = %d, want 1", traceProvisioner.callCount())
	}
	binding, ok := bindings.GetByConversation("!dm:one")
	if !ok {
		t.Fatalf("expected binding for dm conversation")
	}
	if binding.TraceConversationID != "!trace:one" {
		t.Fatalf("trace conversation id = %q, want !trace:one", binding.TraceConversationID)
	}
	if binding.TraceMode != TraceModeReadOnly {
		t.Fatalf("trace mode = %q, want %q", binding.TraceMode, TraceModeReadOnly)
	}
	if binding.TraceRender != TraceRenderCards {
		t.Fatalf("trace render = %q, want %q", binding.TraceRender, TraceRenderCards)
	}
	if tracePublisher.lastRoom() != "!trace:one" {
		t.Fatalf("last trace room = %q, want !trace:one", tracePublisher.lastRoom())
	}
	foundTraceNotice := false
	for _, message := range fake.sentMessages() {
		if strings.Contains(message.Text, "Trace channel (read-only): ") {
			if message.ThreadRootEventID != "$dm-event-1" {
				t.Fatalf("trace notice thread root = %q, want $dm-event-1", message.ThreadRootEventID)
			}
			foundTraceNotice = true
			break
		}
	}
	if !foundTraceNotice {
		t.Fatalf("expected trace channel notice in outbound messages")
	}

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		Text:           "!context clear",
	}); err != nil {
		t.Fatalf("HandleInbound(clear) error: %v", err)
	}
	if traceProvisioner.callCount() != 1 {
		t.Fatalf("trace provisioner call count after clear = %d, want 1", traceProvisioner.callCount())
	}
}

func TestDMPipelineIgnoresTraceRoomInboundMessages(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	created, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "agent:a", Type: sessionrt.ActorAgent},
			{ID: "external:@user:hs", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	fake := &fakeTransport{}
	bindings := NewInMemoryConversationBindingStore()
	if err := bindings.Set(ConversationBinding{
		ConversationID:      "!dm:one",
		SessionID:           created.ID,
		TraceConversationID: "!trace:one",
		TraceMode:           TraceModeReadOnly,
		TraceRender:         TraceRenderCards,
		Mode:                ConversationModeDM,
	}); err != nil {
		t.Fatalf("bindings.Set() error: %v", err)
	}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
		Bindings:  bindings,
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	if err := pipeline.HandleInbound(context.Background(), transport.InboundMessage{
		ConversationID: "!trace:one",
		SenderID:       "@user:hs",
		Text:           "debug this",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	time.Sleep(120 * time.Millisecond)

	events, err := store.List(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("store.List() error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1 (session created only)", len(events))
	}
	if fake.sentCount() != 0 {
		t.Fatalf("dm outbound count = %d, want 0", fake.sentCount())
	}
	if fake.traceInboundIgnoredCount() != 1 {
		t.Fatalf("trace inbound ignored count = %d, want 1", fake.traceInboundIgnoredCount())
	}
}

func TestDMPipelineTraceProvisionerBackoffAppliesAcrossSessionReset(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	traceProvisioner := &fakeTraceProvisioner{
		err: fmt.Errorf("trace room unavailable"),
	}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:          manager,
		Transport:        fake,
		AgentID:          "agent:a",
		TraceProvisioner: traceProvisioner,
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound(first) error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() == 1
	})
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		Text:           "next",
	}); err != nil {
		t.Fatalf("HandleInbound(second) error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 2
	})
	if traceProvisioner.callCount() != 1 {
		t.Fatalf("trace provisioner call count = %d, want 1", traceProvisioner.callCount())
	}

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		Text:           "!context clear",
	}); err != nil {
		t.Fatalf("HandleInbound(clear) error: %v", err)
	}
	if traceProvisioner.callCount() != 1 {
		t.Fatalf("trace provisioner call count = %d after clear, want 1", traceProvisioner.callCount())
	}
}

func TestDMPipelinePersistsConversationNameFromInbound(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	fake := &fakeTransport{}
	bindings := NewInMemoryConversationBindingStore()
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
		Bindings:  bindings,
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID:   "!dm:name",
		ConversationName: "Writer Room",
		SenderID:         "@user:hs",
		Text:             "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	binding, ok := bindings.GetByConversation("!dm:name")
	if !ok {
		t.Fatalf("expected binding for conversation")
	}
	if binding.ConversationName != "Writer Room" {
		t.Fatalf("conversation name = %q, want Writer Room", binding.ConversationName)
	}
}

func TestDMPipelinePersistsLastInboundEventCheckpoint(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	fake := &fakeTransport{}
	bindings := NewInMemoryConversationBindingStore()
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:a",
		Bindings:  bindings,
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:event",
		SenderID:       "@user:hs",
		EventID:        "$evt-1",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		binding, ok := bindings.GetByConversation("!dm:event")
		return ok && binding.LastInboundEvent == "$evt-1"
	})
}

func TestDMPipelineIgnoresManagedSenderOutsideDelegationRoom(t *testing.T) {
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
		AgentID:   "writer",
		AgentByRecipient: map[string]sessionrt.ActorID{
			"@writer:hs": "writer",
		},
		RecipientByAgent: map[sessionrt.ActorID]string{
			"writer": "@writer:hs",
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "writer", Type: sessionrt.ActorAgent},
			{ID: "external:@user:hs", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := pipeline.BindConversation("!dm:one", created.ID, "writer", "@writer:hs", ConversationModeDM); err != nil {
		t.Fatalf("BindConversation() error: %v", err)
	}
	if err := pipeline.HandleInbound(context.Background(), transport.InboundMessage{
		ConversationID: "!dm:one",
		SenderID:       "@writer:hs",
		SenderManaged:  true,
		RecipientID:    "@writer:hs",
		Text:           "@writer:hs should be ignored in dm mode",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if got := fake.sentCount(); got != 0 {
		t.Fatalf("outbound count = %d, want 0", got)
	}
}

func TestDMPipelineAcceptsManagedSenderInDelegationRoom(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "delegation-ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "writer",
		AgentByRecipient: map[string]sessionrt.ActorID{
			"@writer:hs": "writer",
		},
		RecipientByAgent: map[sessionrt.ActorID]string{
			"writer": "@writer:hs",
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "writer", Type: sessionrt.ActorAgent},
			{ID: "external:@user:hs", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := pipeline.BindConversation("!room:delegate", created.ID, "writer", "@writer:hs", ConversationModeDelegation); err != nil {
		t.Fatalf("BindConversation() error: %v", err)
	}
	if err := pipeline.HandleInbound(context.Background(), transport.InboundMessage{
		ConversationID: "!room:delegate",
		SenderID:       "@writer:hs",
		SenderManaged:  true,
		RecipientID:    "@writer:hs",
		Text:           "@writer:hs please continue",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() == 1
	})
	got := fake.lastSent()
	if got.Text != "delegation-ack" {
		t.Fatalf("last outbound text = %q, want delegation-ack", got.Text)
	}
}

func TestDMPipelineDelegationSupportsTraceChannel(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmTraceExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	tracePublisher := &fakeTracePublisher{}
	traceProvisioner := &fakeTraceProvisioner{
		result: TraceConversationBinding{
			ConversationID:   "!trace:delegation",
			ConversationName: "trace-delegation",
			Mode:             TraceModeReadOnly,
			Render:           TraceRenderCards,
		},
	}
	bindings := NewInMemoryConversationBindingStore()
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:          manager,
		Transport:        fake,
		AgentID:          "writer",
		AgentByRecipient: map[string]sessionrt.ActorID{"@writer:hs": "writer"},
		RecipientByAgent: map[sessionrt.ActorID]string{"writer": "@writer:hs"},
		Bindings:         bindings,
		TracePublisher:   tracePublisher,
		TraceProvisioner: traceProvisioner,
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	created, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "writer", Type: sessionrt.ActorAgent},
			{ID: "external:@user:hs", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := pipeline.BindConversation("!room:delegate", created.ID, "writer", "@writer:hs", ConversationModeDelegation); err != nil {
		t.Fatalf("BindConversation() error: %v", err)
	}
	pipeline.EnsureTraceConversation(context.Background(), TraceConversationRequest{
		ConversationID: "!room:delegate",
		SessionID:      created.ID,
		AgentID:        "writer",
		SenderID:       "@user:hs",
		RecipientID:    "@writer:hs",
	})

	if err := pipeline.HandleInbound(context.Background(), transport.InboundMessage{
		ConversationID: "!room:delegate",
		SenderID:       "@writer:hs",
		SenderManaged:  true,
		RecipientID:    "@writer:hs",
		Text:           "@writer:hs continue",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 2
	})
	waitFor(t, 2*time.Second, func() bool {
		return tracePublisher.publishedCount() >= 3
	})

	if traceProvisioner.callCount() != 1 {
		t.Fatalf("trace provisioner call count = %d, want 1", traceProvisioner.callCount())
	}
	binding, ok := bindings.GetByConversation("!room:delegate")
	if !ok {
		t.Fatalf("expected binding for delegation room")
	}
	if binding.TraceConversationID != "!trace:delegation" {
		t.Fatalf("trace conversation id = %q, want !trace:delegation", binding.TraceConversationID)
	}
	if tracePublisher.lastRoom() != "!trace:delegation" {
		t.Fatalf("trace publish room = %q, want !trace:delegation", tracePublisher.lastRoom())
	}
	foundTraceNotice := false
	for _, message := range fake.sentMessages() {
		if strings.Contains(message.Text, "Trace channel (read-only): ") {
			foundTraceNotice = true
			break
		}
	}
	if !foundTraceNotice {
		t.Fatalf("expected trace channel notice in delegation outbound messages")
	}
}

func TestDMPipelineRoutesByRecipientToMatchingAgentWorkspace(t *testing.T) {
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
		AgentID:   "agent:planner",
		AgentByRecipient: map[string]sessionrt.ActorID{
			"@writer:hs": "agent:writer",
		},
		RecipientByAgent: map[sessionrt.ActorID]string{
			"agent:planner": "@planner:hs",
			"agent:writer":  "@writer:hs",
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:writer",
		SenderID:       "@user:hs",
		RecipientID:    "@writer:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})

	outbound := fake.lastSent()
	if outbound.SenderID != "@writer:hs" {
		t.Fatalf("outbound sender id = %q, want @writer:hs", outbound.SenderID)
	}

	sessionID, ok := pipeline.conversations.Get("!dm:writer")
	if !ok {
		t.Fatalf("expected conversation session mapping")
	}
	loaded, err := manager.GetSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	participant, ok := loaded.Participants["agent:writer"]
	if !ok || participant.Type != sessionrt.ActorAgent {
		t.Fatalf("expected agent:writer participant, got %#v", loaded.Participants)
	}
}

func TestDMPipelineTypingUsesRoutedRecipientSender(t *testing.T) {
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
		AgentID:   "agent:planner",
		AgentByRecipient: map[string]sessionrt.ActorID{
			"@writer:hs": "agent:writer",
		},
		RecipientByAgent: map[sessionrt.ActorID]string{
			"agent:planner": "@planner:hs",
			"agent:writer":  "@writer:hs",
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:typing-route",
		SenderID:       "@user:hs",
		RecipientID:    "@writer:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		signals := fake.typingSignals()
		for _, signal := range signals {
			if signal.ConversationID == "!dm:typing-route" && signal.Typing {
				return true
			}
		}
		return false
	})

	signals := fake.typingSignals()
	var first typingSignal
	found := false
	for _, signal := range signals {
		if signal.ConversationID == "!dm:typing-route" && signal.Typing {
			first = signal
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected typing=true signal for !dm:typing-route")
	}
	if first.SenderID != "@writer:hs" {
		t.Fatalf("typing sender id = %q, want @writer:hs", first.SenderID)
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
	waitFor(t, 2*time.Second, func() bool {
		signals := fake.typingSignals()
		return len(signals) >= 2 && !signals[len(signals)-1].Typing
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
	waitFor(t, 2*time.Second, func() bool {
		signals := fake.typingSignals()
		return len(signals) >= 2 && !signals[len(signals)-1].Typing
	})
}

func TestDMPipelineTypingKeepaliveDuringLongProcessing(t *testing.T) {
	prevInterval := dmTypingKeepaliveInterval
	dmTypingKeepaliveInterval = 20 * time.Millisecond
	t.Cleanup(func() {
		dmTypingKeepaliveInterval = prevInterval
	})

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
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:keepalive",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 500*time.Millisecond, func() bool {
		signals := fake.typingSignals()
		typingTrue := 0
		for _, signal := range signals {
			if signal.ConversationID == "!dm:keepalive" && signal.Typing {
				typingTrue++
			}
		}
		return typingTrue >= 2
	})

	close(manager.block)
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
	waitFor(t, 2*time.Second, func() bool {
		signals := fake.typingSignals()
		return len(signals) >= 2 && !signals[len(signals)-1].Typing
	})
}

func TestDMPipelineKeepsConversationSessionAfterAgentStepError(t *testing.T) {
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
		return fake.sentCount() > 0
	})
	beforeSecond := fake.sentCount()

	waitFor(t, 2*time.Second, func() bool {
		sessions, listErr := store.ListSessions(context.Background())
		if listErr != nil || len(sessions) == 0 {
			return false
		}
		return sessions[0].Status == sessionrt.SessionActive
	})

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:recover",
		SenderID:       "@user:hs",
		Text:           "second",
	}); err != nil {
		t.Fatalf("second HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > beforeSecond
	})

	sessions, err := store.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("session count = %d, want 1 active room-backed session", len(sessions))
	}
	if sessions[0].Status != sessionrt.SessionActive {
		t.Fatalf("session status = %v, want active", sessions[0].Status)
	}
}

func TestDMPipelineRebindsConversationWhenExistingSessionIsInactive(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	stale, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "agent:a", Type: sessionrt.ActorAgent},
			{ID: "external:@user:hs", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := manager.CancelSession(context.Background(), stale.ID); err != nil {
		t.Fatalf("CancelSession() error: %v", err)
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
	if err := pipeline.BindConversation("!dm:stale", stale.ID, "agent:a", "@agent:a", ConversationModeDM); err != nil {
		t.Fatalf("BindConversation() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:stale",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
	if got := fake.lastSent().Text; got != "ack" {
		t.Fatalf("outbound text = %q, want ack", got)
	}

	currentSessionID, ok := pipeline.conversations.Get("!dm:stale")
	if !ok {
		t.Fatalf("expected conversation mapping")
	}
	if currentSessionID == stale.ID {
		t.Fatalf("expected stale session to be replaced")
	}

	sessions, err := store.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2 after rebinding", len(sessions))
	}
}

func TestDMPipelineRebindInactiveSessionPreservesBoundRoute(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	stale, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "milo", Type: sessionrt.ActorAgent},
			{ID: "external:@user:hs", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}
	if err := manager.CancelSession(context.Background(), stale.ID); err != nil {
		t.Fatalf("CancelSession() error: %v", err)
	}

	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "gateway-agent",
		AgentByRecipient: map[string]sessionrt.ActorID{
			"@gateway-agent:hs": "gateway-agent",
			"@milo:hs":          "milo",
		},
		RecipientByAgent: map[sessionrt.ActorID]string{
			"gateway-agent": "@gateway-agent:hs",
			"milo":          "@milo:hs",
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}
	if err := pipeline.BindConversation("!dm:milo", stale.ID, "milo", "@milo:hs", ConversationModeDM); err != nil {
		t.Fatalf("BindConversation() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:milo",
		SenderID:       "@user:hs",
		RecipientID:    "@gateway-agent:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
	if got := fake.lastSent(); got.SenderID != "@milo:hs" {
		t.Fatalf("sender id = %q, want @milo:hs", got.SenderID)
	}

	currentSessionID, ok := pipeline.conversations.Get("!dm:milo")
	if !ok {
		t.Fatalf("expected conversation mapping")
	}
	if currentSessionID == stale.ID {
		t.Fatalf("expected stale session to be replaced")
	}
	createdSession, err := manager.GetSession(context.Background(), currentSessionID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if createdSession == nil {
		t.Fatalf("expected created session")
	}
	if _, exists := createdSession.Participants["milo"]; !exists {
		t.Fatalf("expected replacement session to keep milo participant")
	}
	if _, exists := createdSession.Participants["gateway-agent"]; exists {
		t.Fatalf("replacement session unexpectedly switched to gateway-agent")
	}

	binding, ok := pipeline.bindings.GetByConversation("!dm:milo")
	if !ok {
		t.Fatalf("expected stored binding")
	}
	if binding.AgentID != "milo" {
		t.Fatalf("binding agent_id = %q, want milo", binding.AgentID)
	}
	if binding.RecipientID != "@milo:hs" {
		t.Fatalf("binding recipient_id = %q, want @milo:hs", binding.RecipientID)
	}
}

func TestDMPipelineReplacesActiveSessionWhenAgentRouteMismatches(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	active, err := manager.CreateSession(context.Background(), sessionrt.CreateSessionOptions{
		Participants: []sessionrt.Participant{
			{ID: "gateway-agent", Type: sessionrt.ActorAgent},
			{ID: "external:@user:hs", Type: sessionrt.ActorHuman},
		},
	})
	if err != nil {
		t.Fatalf("CreateSession() error: %v", err)
	}

	fake := &fakeTransport{}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "gateway-agent",
		AgentByRecipient: map[string]sessionrt.ActorID{
			"@gateway-agent:hs": "gateway-agent",
			"@milo:hs":          "milo",
		},
		RecipientByAgent: map[sessionrt.ActorID]string{
			"gateway-agent": "@gateway-agent:hs",
			"milo":          "@milo:hs",
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}
	if err := pipeline.BindConversation("!dm:milo", active.ID, "milo", "@milo:hs", ConversationModeDM); err != nil {
		t.Fatalf("BindConversation() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:milo",
		SenderID:       "@user:hs",
		RecipientID:    "@gateway-agent:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() > 0
	})
	if got := fake.lastSent(); got.SenderID != "@milo:hs" {
		t.Fatalf("sender id = %q, want @milo:hs", got.SenderID)
	}

	currentSessionID, ok := pipeline.conversations.Get("!dm:milo")
	if !ok {
		t.Fatalf("expected conversation mapping")
	}
	if currentSessionID == active.ID {
		t.Fatalf("expected active mismatched session to be replaced")
	}
	createdSession, err := manager.GetSession(context.Background(), currentSessionID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if createdSession == nil {
		t.Fatalf("expected created session")
	}
	if _, exists := createdSession.Participants["milo"]; !exists {
		t.Fatalf("expected replacement session to include milo participant")
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
	if !strings.HasPrefix(got.Text, strings.TrimSuffix(dmRateLimitFallbackReply, ".")) {
		t.Fatalf("fallback reply = %q, want prefix %q", got.Text, dmRateLimitFallbackReply)
	}
	if !strings.Contains(got.Text, "Details: rate limit (429).") {
		t.Fatalf("fallback details = %q, want rate-limit details", got.Text)
	}
	signals := fake.typingSignals()
	if len(signals) < 2 {
		t.Fatalf("typing signal count = %d, want >= 2", len(signals))
	}
	if !signals[0].Typing || signals[len(signals)-1].Typing {
		t.Fatalf("typing lifecycle = %#v, want starts true and ends false", signals)
	}
}

func TestDMPipelineDoesNotSendFallbackWhenAgentRecoversAfterError(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store: store,
		Executor: &dmRecoverableErrorExecutor{
			errorMessage: "validation failed for tool \"read\": root.path: required field missing",
			replyText:    "Morning, bstn! What can I help you with today?",
		},
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
		ConversationID: "!dm:recoverable-error",
		SenderID:       "@user:hs",
		Text:           "good morning milo!",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 1
	})
	time.Sleep(dmErrorFallbackDelay + 300*time.Millisecond)
	messages := fake.sentMessages()
	if len(messages) != 1 {
		t.Fatalf("sent message count = %d, want 1", len(messages))
	}
	if got := strings.TrimSpace(messages[0].Text); got != "Morning, bstn! What can I help you with today?" {
		t.Fatalf("final response = %q, want recovered assistant message", got)
	}
	if strings.Contains(messages[0].Text, "I ran into an upstream error while processing that message.") {
		t.Fatalf("unexpected fallback reply in recovered response: %q", messages[0].Text)
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
	if !strings.HasPrefix(got.Text, strings.TrimSuffix(dmErrorFallbackReply, ".")) {
		t.Fatalf("fallback reply = %q, want prefix %q", got.Text, dmErrorFallbackReply)
	}
	if !strings.Contains(got.Text, "Details: request timed out.") {
		t.Fatalf("fallback details = %q, want timeout details", got.Text)
	}
	signals := fake.typingSignals()
	if len(signals) < 2 {
		t.Fatalf("typing signal count = %d, want >= 2", len(signals))
	}
	if !signals[0].Typing || signals[len(signals)-1].Typing {
		t.Fatalf("typing lifecycle = %#v, want starts true and ends false", signals)
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

func TestDMPipelineSuppressesHeartbeatOKReplies(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store: store,
		Executor: &dmPromptExecutor{
			defaultText: "ack",
			responses: map[string]string{
				"__heartbeat__": "HEARTBEAT_OK",
			},
		},
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
		ConversationID: "!dm:heartbeat",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() == 1
	})

	sessionID, ok := pipeline.conversations.Get("!dm:heartbeat")
	if !ok {
		t.Fatalf("expected mapped session")
	}
	pipeline.MarkHeartbeatPending("!dm:heartbeat", 300)
	if err := manager.SendEvent(ctx, sessionrt.Event{
		SessionID: sessionID,
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "__heartbeat__",
		},
	}); err != nil {
		t.Fatalf("SendEvent() heartbeat failed: %v", err)
	}

	time.Sleep(150 * time.Millisecond)
	if got := fake.sentCount(); got != 1 {
		t.Fatalf("outbound count = %d, want 1 (heartbeat ack suppressed)", got)
	}
}

func TestDMPipelineStripsHeartbeatTokenWhenAlertExceedsAckLimit(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	alert := strings.Repeat("x", 305)
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store: store,
		Executor: &dmPromptExecutor{
			defaultText: "ack",
			responses: map[string]string{
				"__heartbeat__": "HEARTBEAT_OK " + alert,
			},
		},
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
		ConversationID: "!dm:heartbeat-alert",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound() error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() == 1
	})

	sessionID, ok := pipeline.conversations.Get("!dm:heartbeat-alert")
	if !ok {
		t.Fatalf("expected mapped session")
	}
	pipeline.MarkHeartbeatPending("!dm:heartbeat-alert", 300)
	if err := manager.SendEvent(ctx, sessionrt.Event{
		SessionID: sessionID,
		From:      sessionrt.SystemActorID,
		Type:      sessionrt.EventMessage,
		Payload: sessionrt.Message{
			Role:    sessionrt.RoleUser,
			Content: "__heartbeat__",
		},
	}); err != nil {
		t.Fatalf("SendEvent() heartbeat failed: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 2
	})
	got := fake.lastSent()
	if strings.Contains(got.Text, heartbeatOKToken) {
		t.Fatalf("heartbeat token should be stripped from forwarded alert, got %q", got.Text)
	}
	if len([]rune(got.Text)) != len([]rune(alert)) {
		t.Fatalf("forwarded alert length = %d, want %d", len([]rune(got.Text)), len([]rune(alert)))
	}
}

func TestDMPipelineCanDispatchHeartbeatUsesManagedMembership(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmStaticExecutor{text: "ack"},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{
		managed: map[string][]string{
			"!room:one": {"@writer:hs", "@planner:hs"},
		},
	}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:   manager,
		Transport: fake,
		AgentID:   "agent:planner",
		RecipientByAgent: map[sessionrt.ActorID]string{
			"agent:planner": "@planner:hs",
			"agent:writer":  "@writer:hs",
		},
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	if !pipeline.CanDispatchHeartbeat("!room:one", "agent:writer") {
		t.Fatalf("expected writer heartbeat dispatch to be allowed")
	}
	if pipeline.CanDispatchHeartbeat("!room:one", "agent:reviewer") {
		t.Fatalf("did not expect dispatch for unknown agent mapping")
	}
	if pipeline.CanDispatchHeartbeat("!room:two", "agent:writer") {
		t.Fatalf("did not expect dispatch for room without membership")
	}
}

func TestDMPipelineClearCommandResetsSessionAndAcknowledges(t *testing.T) {
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
		ConversationID: "!dm:clear",
		SenderID:       "@user:hs",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound(initial) error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() == 1
	})

	initialSessionID, ok := pipeline.conversations.Get("!dm:clear")
	if !ok {
		t.Fatalf("expected initial conversation mapping")
	}

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:clear",
		SenderID:       "@user:hs",
		Text:           "!context clear",
	}); err != nil {
		t.Fatalf("HandleInbound(clear) error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() == 2
	})
	if got := fake.lastSent().Text; got != dmContextClearedReply {
		t.Fatalf("clear acknowledgement = %q, want %q", got, dmContextClearedReply)
	}

	reboundSessionID, ok := pipeline.conversations.Get("!dm:clear")
	if !ok {
		t.Fatalf("expected rebound conversation mapping")
	}
	if reboundSessionID == initialSessionID {
		t.Fatalf("expected clear command to replace session")
	}

	loaded, err := manager.GetSession(ctx, initialSessionID)
	if err != nil {
		t.Fatalf("GetSession(initial) error: %v", err)
	}
	if loaded.Status != sessionrt.SessionPaused {
		t.Fatalf("initial session status = %v, want paused", loaded.Status)
	}
}

func TestDMPipelineSummarizeCommandDispatchesSummaryPrompt(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store: store,
		Executor: &dmPromptExecutor{
			defaultText: "ack",
			responses: map[string]string{
				dmSummarizeCommandPrompt: "summary reply",
			},
		},
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
		ConversationID: "!dm:summary",
		SenderID:       "@user:hs",
		Text:           "!context summarize",
	}); err != nil {
		t.Fatalf("HandleInbound(summarize) error: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() == 1
	})
	if got := fake.lastSent().Text; got != "summary reply" {
		t.Fatalf("summary reply = %q, want summary reply", got)
	}
}

func TestDMPipelineTraceOffStopsPublishAndTraceOnResumes(t *testing.T) {
	store := sessionrt.NewInMemoryEventStore(sessionrt.InMemoryEventStoreOptions{})
	manager, err := sessionrt.NewManager(sessionrt.ManagerOptions{
		Store:    store,
		Executor: &dmTraceExecutor{},
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	fake := &fakeTransport{}
	tracePublisher := &fakeTracePublisher{}
	traceProvisioner := &fakeTraceProvisioner{
		result: TraceConversationBinding{
			ConversationID:   "!trace:one",
			ConversationName: "trace-room",
			Mode:             TraceModeReadOnly,
			Render:           TraceRenderCards,
		},
	}
	pipeline, err := NewDMPipeline(DMPipelineOptions{
		Manager:          manager,
		Transport:        fake,
		AgentID:          "agent:a",
		TracePublisher:   tracePublisher,
		TraceProvisioner: traceProvisioner,
	})
	if err != nil {
		t.Fatalf("NewDMPipeline() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:trace-toggle",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		EventID:        "$evt-1",
		Text:           "hello",
	}); err != nil {
		t.Fatalf("HandleInbound(initial) error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return tracePublisher.publishedCount() >= 3
	})
	initialPublished := tracePublisher.publishedCount()

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:trace-toggle",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		EventID:        "$evt-off",
		Text:           "!trace off",
	}); err != nil {
		t.Fatalf("HandleInbound(trace off) error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return fake.sentCount() >= 3
	})
	foundOff := false
	for _, message := range fake.sentMessages() {
		if message.Text == "Trace is now off for this conversation." {
			foundOff = true
			if message.ThreadRootEventID != "$evt-off" {
				t.Fatalf("trace off thread root = %q, want $evt-off", message.ThreadRootEventID)
			}
		}
	}
	if !foundOff {
		t.Fatalf("missing trace off acknowledgement")
	}

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:trace-toggle",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		EventID:        "$evt-2",
		Text:           "while trace is off",
	}); err != nil {
		t.Fatalf("HandleInbound(while off) error: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if tracePublisher.publishedCount() != initialPublished {
		t.Fatalf("published trace count = %d, want unchanged %d", tracePublisher.publishedCount(), initialPublished)
	}

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:trace-toggle",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		EventID:        "$evt-on",
		Text:           "!trace on",
	}); err != nil {
		t.Fatalf("HandleInbound(trace on) error: %v", err)
	}
	foundOn := false
	for _, message := range fake.sentMessages() {
		if strings.Contains(message.Text, "Trace channel (read-only): ") && message.ThreadRootEventID == "$evt-on" {
			foundOn = true
		}
	}
	if !foundOn {
		t.Fatalf("missing trace link reply for trace on command")
	}
	if traceProvisioner.callCount() != 1 {
		t.Fatalf("trace provisioner call count = %d, want 1", traceProvisioner.callCount())
	}

	if err := pipeline.HandleInbound(ctx, transport.InboundMessage{
		ConversationID: "!dm:trace-toggle",
		SenderID:       "@user:hs",
		RecipientID:    "@milo:hs",
		EventID:        "$evt-3",
		Text:           "trace resumed",
	}); err != nil {
		t.Fatalf("HandleInbound(after on) error: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return tracePublisher.publishedCount() > initialPublished
	})
}

func TestFallbackReplyForErrorSanitizesSensitiveDetails(t *testing.T) {
	reply := fallbackReplyForError("upstream failure token=supersecrettokenvalue0123456789 Authorization: Bearer sk-test-123")
	if !strings.Contains(reply, "Details:") {
		t.Fatalf("fallback reply missing details: %q", reply)
	}
	if strings.Contains(strings.ToLower(reply), "supersecrettokenvalue0123456789") {
		t.Fatalf("expected token to be redacted, got %q", reply)
	}
	if strings.Contains(strings.ToLower(reply), "sk-test-123") {
		t.Fatalf("expected bearer secret to be redacted, got %q", reply)
	}
}

func TestTraceConversationReadyMessageIncludesConversationID(t *testing.T) {
	got := traceConversationReadyMessage("!trace:example.com")
	if !strings.Contains(got, "Trace channel (read-only): ") {
		t.Fatalf("trace notice = %q", got)
	}
	if !strings.Contains(got, "!trace:example.com") {
		t.Fatalf("trace notice missing conversation id: %q", got)
	}
	if empty := traceConversationReadyMessage("   "); empty != "" {
		t.Fatalf("empty trace notice = %q, want empty", empty)
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
