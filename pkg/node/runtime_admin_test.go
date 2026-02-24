package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

type noopExecutor struct{}

func (n *noopExecutor) Step(context.Context, sessionrt.AgentInput) (sessionrt.AgentOutput, error) {
	return sessionrt.AgentOutput{}, nil
}

type recordingAdminHandler struct {
	lastRequest AdminRequest
}

func (h *recordingAdminHandler) HandleAdmin(req AdminRequest) AdminResponse {
	h.lastRequest = req
	if req.Action == AdminActionRestart {
		return AdminResponse{OK: true, RestartRequested: true}
	}
	return AdminResponse{
		OK:            true,
		PersistedPath: "/tmp/node.toml",
	}
}

func TestRuntimeAdminConfigureRequest(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	handler := &recordingAdminHandler{}
	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:       "node-admin",
		Fabric:       fabric,
		Executor:     &noopExecutor{},
		AdminHandler: handler,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime.Start() error: %v", err)
	}
	defer runtime.Stop()

	request := AdminRequest{
		Action: AdminActionConfigure,
		Configure: &AdminConfigureRequest{
			NodeID: ptrString("node-admin-next"),
		},
	}
	blob, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	responseBlob, err := fabric.Request(ctx, fabricts.NodeAdminSubject("node-admin"), blob)
	if err != nil {
		t.Fatalf("fabric.Request() error: %v", err)
	}
	var response AdminResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if !response.OK {
		t.Fatalf("expected OK response, got %#v", response)
	}
	if response.PersistedPath != "/tmp/node.toml" {
		t.Fatalf("persisted path = %q, want /tmp/node.toml", response.PersistedPath)
	}
	if handler.lastRequest.Action != AdminActionConfigure {
		t.Fatalf("handler action = %q, want configure", handler.lastRequest.Action)
	}
	if handler.lastRequest.Configure == nil || handler.lastRequest.Configure.NodeID == nil || *handler.lastRequest.Configure.NodeID != "node-admin-next" {
		t.Fatalf("handler configure payload = %#v", handler.lastRequest.Configure)
	}
}

func TestRuntimeAdminRestartRequest(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	handler := &recordingAdminHandler{}
	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:       "node-admin-restart",
		Fabric:       fabric,
		Executor:     &noopExecutor{},
		AdminHandler: handler,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime.Start() error: %v", err)
	}
	defer runtime.Stop()

	blob, err := json.Marshal(AdminRequest{Action: AdminActionRestart})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	responseBlob, err := fabric.Request(ctx, fabricts.NodeAdminSubject("node-admin-restart"), blob)
	if err != nil {
		t.Fatalf("fabric.Request() error: %v", err)
	}
	var response AdminResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if !response.OK || !response.RestartRequested {
		t.Fatalf("unexpected restart response: %#v", response)
	}
}

func TestRuntimeAdminRejectsMalformedRequest(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	handler := &recordingAdminHandler{}
	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:       "node-admin-malformed",
		Fabric:       fabric,
		Executor:     &noopExecutor{},
		AdminHandler: handler,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime.Start() error: %v", err)
	}
	defer runtime.Stop()

	responseBlob, err := fabric.Request(ctx, fabricts.NodeAdminSubject("node-admin-malformed"), []byte("{"))
	if err != nil {
		t.Fatalf("fabric.Request() error: %v", err)
	}
	var response AdminResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if response.OK {
		t.Fatalf("expected error response, got %#v", response)
	}
	if !strings.Contains(response.Error, "decode admin request") {
		t.Fatalf("error = %q, want decode admin request", response.Error)
	}
}

func TestRuntimeWithoutAdminHandlerDoesNotSubscribeAdmin(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:   "node-no-admin",
		Fabric:   fabric,
		Executor: &noopExecutor{},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime.Start() error: %v", err)
	}
	defer runtime.Stop()

	requestCtx, requestCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer requestCancel()
	blob, _ := json.Marshal(AdminRequest{Action: AdminActionRestart})
	_, err = fabric.Request(requestCtx, fabricts.NodeAdminSubject("node-no-admin"), blob)
	if err == nil {
		t.Fatalf("expected request timeout when admin handler is absent")
	}
	if requestCtx.Err() == nil {
		t.Fatalf("expected context timeout, got err=%v", err)
	}
}

func ptrString(value string) *string {
	return &value
}

type strictAdminHandler struct{}

func (h *strictAdminHandler) HandleAdmin(req AdminRequest) AdminResponse {
	switch req.Action {
	case AdminActionConfigure, AdminActionRestart:
		return AdminResponse{OK: true}
	default:
		return AdminResponse{OK: false, Error: "unsupported action"}
	}
}

func TestRuntimeAdminUnknownActionReturnsStructuredError(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:       "node-admin-unknown",
		Fabric:       fabric,
		Executor:     &noopExecutor{},
		AdminHandler: &strictAdminHandler{},
	})
	if err != nil {
		t.Fatalf("NewRuntime() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime.Start() error: %v", err)
	}
	defer runtime.Stop()

	blob, err := json.Marshal(AdminRequest{Action: AdminAction("unknown")})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	responseBlob, err := fabric.Request(ctx, fabricts.NodeAdminSubject("node-admin-unknown"), blob)
	if err != nil {
		t.Fatalf("fabric.Request() error: %v", err)
	}
	var response AdminResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		t.Fatalf("decode admin response: %v", err)
	}
	if response.OK {
		t.Fatalf("expected non-OK response for unknown action")
	}
	if !strings.Contains(response.Error, "unsupported action") {
		t.Fatalf("error = %q, want unsupported action", response.Error)
	}
}
