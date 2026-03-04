package agentcore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

type fakeMessageToolService struct {
	lastReq MessageSendRequest
}

func (s *fakeMessageToolService) SendMessage(_ context.Context, req MessageSendRequest) (MessageSendResult, error) {
	s.lastReq = req
	return MessageSendResult{
		Sent:            true,
		ConversationID:  "telegram:123",
		Text:            strings.TrimSpace(req.Text),
		AttachmentCount: len(req.Attachments),
	}, nil
}

type fakeStreamingMessageToolService struct {
	fakeMessageToolService
	lastDraftReq MessageDraftRequest
}

func (s *fakeStreamingMessageToolService) SendMessageDraft(_ context.Context, req MessageDraftRequest) (MessageDraftResult, error) {
	s.lastDraftReq = req
	draftID := req.DraftID
	if draftID <= 0 {
		draftID = 77
	}
	return MessageDraftResult{
		Drafted:        true,
		ConversationID: "telegram:123",
		Text:           strings.TrimSpace(req.Text),
		DraftID:        draftID,
	}, nil
}

func TestMessageToolSendTextAndAttachments(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"message"}
	workspace := createTestWorkspace(t, config, defaultPolicies())
	mustWriteFile(t, filepath.Join(workspace, "artifact.txt"), "hello")

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeMessageToolService{}
	agent.MessageService = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	session.ID = "sess-message"

	output, err := runner.Run(context.Background(), session, toolCall("message", map[string]any{
		"text": "done",
		"attachments": []any{
			map[string]any{
				"path":      "artifact.txt",
				"name":      "artifact.txt",
				"mime_type": "text/plain",
			},
		},
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastReq.SessionID != "sess-message" {
		t.Fatalf("session id = %q, want sess-message", fake.lastReq.SessionID)
	}
	if fake.lastReq.Text != "done" {
		t.Fatalf("text = %q, want done", fake.lastReq.Text)
	}
	if len(fake.lastReq.Attachments) != 1 {
		t.Fatalf("attachment count = %d, want 1", len(fake.lastReq.Attachments))
	}
	expectedPath := evalSymlinksOrAncestor(filepath.Join(workspace, "artifact.txt"))
	if got := strings.TrimSpace(fake.lastReq.Attachments[0].Path); got != expectedPath {
		t.Fatalf("attachment path = %q, want %q", got, expectedPath)
	}
}

func TestMessageToolRequiresTextOrAttachments(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"message"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.MessageService = &fakeMessageToolService{}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	_, err = runner.Run(context.Background(), session, toolCall("message", map[string]any{}))
	if err == nil {
		t.Fatalf("expected validation error for empty message payload")
	}
}

func TestMessageToolUnavailableWhenServiceMissing(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"message"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	_, err = runner.Run(context.Background(), session, toolCall("message", map[string]any{"text": "hello"}))
	if err == nil {
		t.Fatalf("expected unavailable tool error")
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %q, want tool not registered", err.Error())
	}
}

func TestMessageToolRejectsAttachmentOutsideWorkspace(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"message"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.MessageService = &fakeMessageToolService{}
	runner := NewToolRunner(agent)
	session := agent.NewSession()

	_, err = runner.Run(context.Background(), session, toolCall("message", map[string]any{
		"attachments": []any{map[string]any{"path": "../outside.txt"}},
	}))
	if err == nil {
		t.Fatalf("expected attachment policy error")
	}
}

func TestMessageToolStreamsDraftWhenRequested(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"message"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	fake := &fakeStreamingMessageToolService{}
	agent.MessageService = fake
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	session.ID = "sess-message-stream"

	output, err := runner.Run(context.Background(), session, toolCall("message", map[string]any{
		"text":     "streaming reply",
		"stream":   true,
		"draft_id": 42,
	}))
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if output.Status != ToolStatusOK {
		t.Fatalf("status = %q, want ok", output.Status)
	}
	if fake.lastDraftReq.SessionID != "sess-message-stream" {
		t.Fatalf("draft session id = %q, want sess-message-stream", fake.lastDraftReq.SessionID)
	}
	if fake.lastDraftReq.DraftID != 42 {
		t.Fatalf("draft id = %d, want 42", fake.lastDraftReq.DraftID)
	}
	result, ok := output.Result.(map[string]any)
	if !ok {
		t.Fatalf("output result type = %T, want map[string]any", output.Result)
	}
	if result["draft_id"] != int64(42) {
		t.Fatalf("tool output draft_id = %#v, want 42", result["draft_id"])
	}
}

func TestMessageToolStreamingRequiresServiceSupport(t *testing.T) {
	config := defaultConfig()
	config.EnabledTools = []string{"message"}
	workspace := createTestWorkspace(t, config, defaultPolicies())

	agent, err := LoadAgent(workspace)
	if err != nil {
		t.Fatalf("LoadAgent() error: %v", err)
	}
	agent.MessageService = &fakeMessageToolService{}
	runner := NewToolRunner(agent)
	session := agent.NewSession()
	session.ID = "sess-message-stream"

	_, err = runner.Run(context.Background(), session, toolCall("message", map[string]any{
		"text":   "streaming reply",
		"stream": true,
	}))
	if err == nil {
		t.Fatalf("expected streaming unsupported error")
	}
	if !strings.Contains(err.Error(), "streaming") {
		t.Fatalf("error = %q, want streaming unsupported text", err.Error())
	}
}
