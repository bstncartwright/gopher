package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
)

const defaultPruneInterval = 3 * time.Second

type NodeRegistrySyncOptions struct {
	Fabric        fabricts.Fabric
	Registry      *scheduler.Registry
	Now           func() time.Time
	PruneInterval time.Duration
}

type NodeRegistrySync struct {
	fabric        fabricts.Fabric
	registry      *scheduler.Registry
	now           func() time.Time
	pruneInterval time.Duration

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	subs    []fabricts.Subscription
}

func NewNodeRegistrySync(opts NodeRegistrySyncOptions) (*NodeRegistrySync, error) {
	if opts.Fabric == nil {
		return nil, fmt.Errorf("fabric is required")
	}
	if opts.Registry == nil {
		return nil, fmt.Errorf("registry is required")
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	interval := opts.PruneInterval
	if interval <= 0 {
		interval = defaultPruneInterval
	}
	return &NodeRegistrySync{
		fabric:        opts.Fabric,
		registry:      opts.Registry,
		now:           nowFn,
		pruneInterval: interval,
	}, nil
}

func (s *NodeRegistrySync) Start(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	localCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.subs = nil

	capSub, err := s.fabric.Subscribe("node.*.capabilities", s.handleCapabilities)
	if err != nil {
		cancel()
		return fmt.Errorf("subscribe capabilities: %w", err)
	}
	s.subs = append(s.subs, capSub)

	statusSub, err := s.fabric.Subscribe("node.*.status", s.handleHeartbeat)
	if err != nil {
		for _, sub := range s.subs {
			sub.Unsubscribe()
		}
		s.subs = nil
		cancel()
		return fmt.Errorf("subscribe status: %w", err)
	}
	s.subs = append(s.subs, statusSub)

	go s.pruneLoop(localCtx)
	s.running = true
	return nil
}

func (s *NodeRegistrySync) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	for _, sub := range s.subs {
		sub.Unsubscribe()
	}
	s.subs = nil
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
}

func (s *NodeRegistrySync) pruneLoop(ctx context.Context) {
	ticker := time.NewTicker(s.pruneInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.registry.PruneExpired(s.now().UTC())
		}
	}
}

func (s *NodeRegistrySync) handleCapabilities(ctx context.Context, message fabricts.Message) {
	var announcement node.CapabilityAnnouncement
	if err := json.Unmarshal(message.Data, &announcement); err != nil {
		return
	}
	at := announcement.Timestamp
	if at.IsZero() {
		at = s.now().UTC()
	}
	s.registry.SetCapabilities(announcement.NodeID, announcement.Capabilities, announcement.Agents, announcement.IsGateway, announcement.Version, at)
	s.registry.RecordHeartbeat(announcement.NodeID, announcement.Agents, announcement.IsGateway, announcement.Version, at)
	_ = ctx
}

func (s *NodeRegistrySync) handleHeartbeat(ctx context.Context, message fabricts.Message) {
	var heartbeat node.Heartbeat
	if err := json.Unmarshal(message.Data, &heartbeat); err != nil {
		return
	}
	at := heartbeat.Timestamp
	if at.IsZero() {
		at = s.now().UTC()
	}
	if len(heartbeat.Capabilities) > 0 {
		s.registry.SetCapabilities(heartbeat.NodeID, heartbeat.Capabilities, heartbeat.Agents, heartbeat.IsGateway, heartbeat.Version, at)
	} else {
		s.registry.RecordHeartbeat(heartbeat.NodeID, heartbeat.Agents, heartbeat.IsGateway, heartbeat.Version, at)
	}
	_ = ctx
}
