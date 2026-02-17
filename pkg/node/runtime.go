package node

import (
	"context"
	"encoding/json"
	"fmt"
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
		return nil, fmt.Errorf("node ID is required")
	}
	if opts.Fabric == nil {
		return nil, fmt.Errorf("fabric is required")
	}
	if opts.Executor == nil {
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
		return nil
	}

	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.subs = nil

	controlSub, err := r.fabric.Subscribe(fabricts.NodeControlSubject(r.nodeID), r.handleControlMessage)
	if err != nil {
		r.cancel()
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
	return nil
}

func (r *Runtime) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return
	}
	for _, sub := range r.subs {
		sub.Unsubscribe()
	}
	r.subs = nil
	if r.cancel != nil {
		r.cancel()
	}
	r.running = false
}

func (r *Runtime) heartbeatLoop() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			_ = r.publishHeartbeat(context.Background())
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
		return fmt.Errorf("marshal capabilities: %w", err)
	}
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
		r.respond(ctx, message.Reply, ExecutionResponse{Error: fmt.Sprintf("decode execution request: %v", err)})
		return
	}

	output, err := r.executor.Step(r.ctx, sessionrt.AgentInput{
		SessionID: request.SessionID,
		ActorID:   request.ActorID,
		History:   request.History,
	})
	if err != nil {
		r.respond(ctx, message.Reply, ExecutionResponse{Error: err.Error()})
		return
	}
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
