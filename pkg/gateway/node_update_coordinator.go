package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	updatepkg "github.com/bstncartwright/gopher/pkg/update"
)

const (
	defaultNodeUpdateRequestTimeout = 5 * time.Second
	defaultNodeUpdateRetryInterval  = 30 * time.Second
)

type NodeUpdateCoordinatorOptions struct {
	Fabric         fabricts.Fabric
	GatewayNodeID  string
	GatewayVersion string
	RequestTimeout time.Duration
	RetryInterval  time.Duration
	Now            func() time.Time
}

type NodeUpdateCoordinator struct {
	fabric         fabricts.Fabric
	gatewayNodeID  string
	gatewayVersion string
	requestTimeout time.Duration
	retryInterval  time.Duration
	now            func() time.Time

	mu          sync.Mutex
	running     bool
	cancel      context.CancelFunc
	subs        []fabricts.Subscription
	lastAttempt map[string]time.Time
	requested   map[string]string
}

func NewNodeUpdateCoordinator(opts NodeUpdateCoordinatorOptions) (*NodeUpdateCoordinator, error) {
	if opts.Fabric == nil {
		return nil, fmt.Errorf("fabric is required")
	}
	requestTimeout := opts.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = defaultNodeUpdateRequestTimeout
	}
	retryInterval := opts.RetryInterval
	if retryInterval <= 0 {
		retryInterval = defaultNodeUpdateRetryInterval
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	return &NodeUpdateCoordinator{
		fabric:         opts.Fabric,
		gatewayNodeID:  strings.TrimSpace(opts.GatewayNodeID),
		gatewayVersion: strings.TrimSpace(opts.GatewayVersion),
		requestTimeout: requestTimeout,
		retryInterval:  retryInterval,
		now:            nowFn,
		lastAttempt:    make(map[string]time.Time),
		requested:      make(map[string]string),
	}, nil
}

func (c *NodeUpdateCoordinator) Start(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return nil
	}
	_, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.subs = nil

	statusSub, err := c.fabric.Subscribe("node.*.status", c.handleHeartbeat)
	if err != nil {
		cancel()
		return fmt.Errorf("subscribe status: %w", err)
	}
	c.subs = append(c.subs, statusSub)

	capSub, err := c.fabric.Subscribe("node.*.capabilities", c.handleCapabilities)
	if err != nil {
		statusSub.Unsubscribe()
		c.subs = nil
		cancel()
		return fmt.Errorf("subscribe capabilities: %w", err)
	}
	c.subs = append(c.subs, capSub)
	c.running = true
	return nil
}

func (c *NodeUpdateCoordinator) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return
	}
	for _, sub := range c.subs {
		sub.Unsubscribe()
	}
	c.subs = nil
	if c.cancel != nil {
		c.cancel()
	}
	c.running = false
}

func (c *NodeUpdateCoordinator) handleHeartbeat(ctx context.Context, message fabricts.Message) {
	var heartbeat node.Heartbeat
	if err := json.Unmarshal(message.Data, &heartbeat); err != nil {
		return
	}
	c.maybeRequestUpdate(ctx, heartbeat.NodeID, heartbeat.IsGateway, heartbeat.Version)
}

func (c *NodeUpdateCoordinator) handleCapabilities(ctx context.Context, message fabricts.Message) {
	var announcement node.CapabilityAnnouncement
	if err := json.Unmarshal(message.Data, &announcement); err != nil {
		return
	}
	c.maybeRequestUpdate(ctx, announcement.NodeID, announcement.IsGateway, announcement.Version)
}

func (c *NodeUpdateCoordinator) maybeRequestUpdate(_ context.Context, nodeID string, isGateway bool, nodeVersion string) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" || isGateway {
		return
	}
	if c.gatewayNodeID != "" && nodeID == c.gatewayNodeID {
		return
	}
	if !c.needsUpdate(nodeVersion) {
		c.clearNodeState(nodeID)
		return
	}
	if !c.markAttempt(nodeID) {
		return
	}
	target := c.gatewayVersion
	request := node.AdminRequest{Action: node.AdminActionUpdate}
	if target != "" {
		request.Update = &node.AdminUpdateRequest{TargetVersion: &target}
	}
	if err := c.sendUpdateRequest(nodeID, request); err != nil {
		slog.Warn("gateway_node_update: request failed",
			"node_id", nodeID,
			"node_version", strings.TrimSpace(nodeVersion),
			"gateway_version", c.gatewayVersion,
			"error", err,
		)
		return
	}
	c.markRequested(nodeID)
}

func (c *NodeUpdateCoordinator) needsUpdate(nodeVersion string) bool {
	gatewayVersion := strings.TrimSpace(c.gatewayVersion)
	if gatewayVersion == "" || !strings.HasPrefix(gatewayVersion, "v") {
		return false
	}
	nodeVersion = strings.TrimSpace(nodeVersion)
	if nodeVersion == "" {
		return true
	}
	if nodeVersion == gatewayVersion {
		return false
	}
	if strings.HasPrefix(nodeVersion, "v") {
		cmp, err := updatepkg.CompareVersions(nodeVersion, gatewayVersion)
		if err == nil {
			return cmp < 0
		}
	}
	return true
}

func (c *NodeUpdateCoordinator) markAttempt(nodeID string) bool {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.requested[nodeID] == c.gatewayVersion {
		return false
	}
	last := c.lastAttempt[nodeID]
	if !last.IsZero() && now.Sub(last) < c.retryInterval {
		return false
	}
	c.lastAttempt[nodeID] = now
	return true
}

func (c *NodeUpdateCoordinator) markRequested(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requested[nodeID] = c.gatewayVersion
}

func (c *NodeUpdateCoordinator) clearNodeState(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.lastAttempt, nodeID)
	delete(c.requested, nodeID)
}

func (c *NodeUpdateCoordinator) sendUpdateRequest(nodeID string, request node.AdminRequest) error {
	blob, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal node update request: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), c.requestTimeout)
	defer cancel()
	responseBlob, err := c.fabric.Request(requestCtx, fabricts.NodeAdminSubject(nodeID), blob)
	if err != nil {
		return fmt.Errorf("request node admin update: %w", err)
	}
	var response node.AdminResponse
	if err := json.Unmarshal(responseBlob, &response); err != nil {
		return fmt.Errorf("decode node admin update response: %w", err)
	}
	if !response.OK {
		message := strings.TrimSpace(response.Error)
		if message == "" {
			message = "node update request rejected"
		}
		return errors.New(message)
	}
	slog.Info("gateway_node_update: update requested",
		"node_id", nodeID,
		"gateway_version", c.gatewayVersion,
	)
	return nil
}
