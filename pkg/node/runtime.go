package node

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/scheduler"
	sessionrt "github.com/bstncartwright/gopher/pkg/session"
)

const defaultHeartbeatInterval = 2 * time.Second

type ExecutionRequest struct {
	SessionID sessionrt.SessionID `json:"session_id"`
	ActorID   sessionrt.ActorID   `json:"actor_id"`
	History   []sessionrt.Event   `json:"history"`
}

type ExecutionResponse struct {
	Events []sessionrt.Event `json:"events,omitempty"`
	Error  string            `json:"error,omitempty"`
}

type CapabilityAnnouncement struct {
	NodeID       string                 `json:"node_id"`
	IsGateway    bool                   `json:"is_gateway"`
	Capabilities []scheduler.Capability `json:"capabilities"`
	Timestamp    time.Time              `json:"timestamp"`
}

type Heartbeat struct {
	NodeID       string                 `json:"node_id"`
	IsGateway    bool                   `json:"is_gateway"`
	Capabilities []scheduler.Capability `json:"capabilities"`
	Timestamp    time.Time              `json:"timestamp"`
}

type RuntimeOptions struct {
	NodeID            string
	IsGateway         bool
	Capabilities      []scheduler.Capability
	Fabric            fabricts.Fabric
	Executor          sessionrt.AgentExecutor
	HeartbeatInterval time.Duration
	Now               func() time.Time
}

type Runtime struct {
	nodeID       string
	isGateway    bool
	capabilities []scheduler.Capability
	fabric       fabricts.Fabric
	executor     sessionrt.AgentExecutor
	interval     time.Duration
	now          func() time.Time

	mu      sync.Mutex
	running bool
	ctx     context.Context
	cancel  context.CancelFunc
	subs    []fabricts.Subscription
}

func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	nodeID := strings.TrimSpace(opts.NodeID)
	if nodeID == "" {
		slog.Error("node_runtime: node ID is required")
		return nil, fmt.Errorf("node ID is required")
	}
	if opts.Fabric == nil {
		slog.Error("node_runtime: fabric is required", "node_id", nodeID)
		return nil, fmt.Errorf("fabric is required")
	}
	if opts.Executor == nil {
		slog.Error("node_runtime: executor is required", "node_id", nodeID)
		return nil, fmt.Errorf("executor is required")
	}
	interval := opts.HeartbeatInterval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	slog.Info("node_runtime: creating runtime",
		"node_id", nodeID,
		"is_gateway", opts.IsGateway,
		"capabilities_count", len(opts.Capabilities),
		"heartbeat_interval", interval,
	)
	return &Runtime{
		nodeID:       nodeID,
		isGateway:    opts.IsGateway,
		capabilities: append([]scheduler.Capability(nil), opts.Capabilities...),
		fabric:       opts.Fabric,
		executor:     opts.Executor,
		interval:     interval,
		now:          nowFn,
	}, nil
}

func (r *Runtime) NodeID() string {
	return r.nodeID
}

func (r *Runtime) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		slog.Debug("node_runtime: already running", "node_id", r.nodeID)
		return nil
	}

	slog.Info("node_runtime: starting", "node_id", r.nodeID)
	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.subs = nil

	controlSub, err := r.fabric.Subscribe(fabricts.NodeControlSubject(r.nodeID), r.handleControlMessage)
	if err != nil {
		r.cancel()
		slog.Error("node_runtime: failed to subscribe to control", "node_id", r.nodeID, "error", err)
		return fmt.Errorf("subscribe control: %w", err)
	}
	r.subs = append(r.subs, controlSub)

	if err := r.publishCapabilities(ctx); err != nil {
		r.cancel()
		return err
	}
	if err := r.publishHeartbeat(ctx); err != nil {
		r.cancel()
		return err
	}

	go r.heartbeatLoop()
	r.running = true
	slog.Info("node_runtime: started", "node_id", r.nodeID)
	return nil
}

func (r *Runtime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return
	}
	slog.Info("node_runtime: stopping", "node_id", r.nodeID)
	for _, sub := range r.subs {
		sub.Unsubscribe()
	}
	r.subs = nil
	if r.cancel != nil {
		r.cancel()
	}
	r.running = false
	slog.Info("node_runtime: stopped", "node_id", r.nodeID)
}

func (r *Runtime) heartbeatLoop() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	heartbeatCount := 0
	for {
		select {
		case <-r.ctx.Done():
			slog.Debug("node_runtime: heartbeat loop stopped", "node_id", r.nodeID, "heartbeats_sent", heartbeatCount)
			return
		case <-ticker.C:
			heartbeatCount++
			if err := r.publishHeartbeat(context.Background()); err != nil {
				slog.Warn("node_runtime: heartbeat failed", "node_id", r.nodeID, "error", err)
			}
		}
	}
}

func (r *Runtime) publishCapabilities(ctx context.Context) error {
	announcement := CapabilityAnnouncement{
		NodeID:       r.nodeID,
		IsGateway:    r.isGateway,
		Capabilities: append([]scheduler.Capability(nil), r.capabilities...),
		Timestamp:    r.now().UTC(),
	}
	blob, err := json.Marshal(announcement)
	if err != nil {
		slog.Error("node_runtime: failed to marshal capabilities", "node_id", r.nodeID, "error", err)
		return fmt.Errorf("marshal capabilities: %w", err)
	}
	slog.Debug("node_runtime: publishing capabilities",
		"node_id", r.nodeID,
		"is_gateway", r.isGateway,
		"capabilities_count", len(r.capabilities),
	)
	return r.fabric.Publish(ctx, fabricts.Message{
		Subject: fabricts.NodeCapabilitiesSubject(r.nodeID),
		Data:    blob,
	})
}

func (r *Runtime) publishHeartbeat(ctx context.Context) error {
	heartbeat := Heartbeat{
		NodeID:       r.nodeID,
		IsGateway:    r.isGateway,
		Capabilities: append([]scheduler.Capability(nil), r.capabilities...),
		Timestamp:    r.now().UTC(),
	}
	blob, err := json.Marshal(heartbeat)
	if err != nil {
		slog.Error("node_runtime: failed to marshal heartbeat", "node_id", r.nodeID, "error", err)
		return fmt.Errorf("marshal heartbeat: %w", err)
	}
	return r.fabric.Publish(ctx, fabricts.Message{
		Subject: fabricts.NodeStatusSubject(r.nodeID),
		Data:    blob,
	})
}

func (r *Runtime) handleControlMessage(ctx context.Context, message fabricts.Message) {
	var request ExecutionRequest
	if err := json.Unmarshal(message.Data, &request); err != nil {
		slog.Error("node_runtime: failed to decode execution request", "node_id", r.nodeID, "error", err)
		r.respond(ctx, message.Reply, ExecutionResponse{Error: fmt.Sprintf("decode execution request: %v", err)})
		return
	}

	slog.Info("node_runtime: handling execution request",
		"node_id", r.nodeID,
		"session_id", request.SessionID,
		"actor_id", request.ActorID,
		"history_count", len(request.History),
	)

	output, err := r.executor.Step(r.ctx, sessionrt.AgentInput{
		SessionID: request.SessionID,
		ActorID:   request.ActorID,
		History:   request.History,
	})
	if err != nil {
		slog.Error("node_runtime: execution failed",
			"node_id", r.nodeID,
			"session_id", request.SessionID,
			"error", err,
		)
		r.respond(ctx, message.Reply, ExecutionResponse{Error: err.Error()})
		return
	}

	slog.Info("node_runtime: execution complete",
		"node_id", r.nodeID,
		"session_id", request.SessionID,
		"events_count", len(output.Events),
	)
	r.respond(ctx, message.Reply, ExecutionResponse{Events: output.Events})
}

func (r *Runtime) respond(ctx context.Context, reply string, response ExecutionResponse) {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return
	}
	blob, err := json.Marshal(response)
	if err != nil {
		return
	}
	_ = r.fabric.Publish(ctx, fabricts.Message{Subject: reply, Data: blob})
}
