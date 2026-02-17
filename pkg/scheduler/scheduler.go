package scheduler

import (
	"fmt"
	"sort"
	"strings"
)

type Scheduler struct {
	registry      *Registry
	gatewayNodeID string
}

func NewScheduler(gatewayNodeID string, registry *Registry) *Scheduler {
	if registry == nil {
		registry = NewRegistry(0)
	}
	return &Scheduler{
		registry:      registry,
		gatewayNodeID: strings.TrimSpace(gatewayNodeID),
	}
}

func (s *Scheduler) Registry() *Registry {
	return s.registry
}

func (s *Scheduler) Select(request SelectionRequest) (Selection, error) {
	nodes := s.registry.Snapshot()
	if len(nodes) == 0 {
		return Selection{}, fmt.Errorf("no nodes available")
	}

	required := normalizeCapabilities(request.RequiredCapabilities)
	candidates := make([]NodeInfo, 0, len(nodes))
	for _, node := range nodes {
		if hasCapabilities(node.Capabilities, required) {
			candidates = append(candidates, node)
		}
	}
	if len(candidates) == 0 {
		return Selection{}, fmt.Errorf("no node satisfies requested capabilities")
	}

	// Phase 2 policy: default to gateway execution unless remote capabilities are required.
	if s.gatewayNodeID != "" {
		for _, node := range candidates {
			if node.NodeID == s.gatewayNodeID {
				return Selection{Location: ExecGateway, NodeID: node.NodeID}, nil
			}
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].NodeID < candidates[j].NodeID
	})
	selected := candidates[0]
	location := ExecNode
	if selected.NodeID == s.gatewayNodeID {
		location = ExecGateway
	}
	return Selection{Location: location, NodeID: selected.NodeID}, nil
}

func hasCapabilities(nodeCaps []Capability, required []Capability) bool {
	if len(required) == 0 {
		return true
	}
	available := make(map[string]struct{}, len(nodeCaps))
	for _, capability := range nodeCaps {
		available[capabilityKey(capability)] = struct{}{}
	}
	for _, capability := range required {
		if _, ok := available[capabilityKey(capability)]; !ok {
			return false
		}
	}
	return true
}
