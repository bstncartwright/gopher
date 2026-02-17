package scheduler

import (
	"testing"
	"time"
)

func TestSchedulerPrefersGatewayByDefault(t *testing.T) {
	registry := NewRegistry(time.Minute)
	registry.Upsert(NodeInfo{
		NodeID:       "gateway",
		IsGateway:    true,
		Capabilities: []Capability{{Kind: CapabilityAgent, Name: "agent"}},
	})
	registry.Upsert(NodeInfo{
		NodeID:       "node-a",
		Capabilities: []Capability{{Kind: CapabilityAgent, Name: "agent"}},
	})

	s := NewScheduler("gateway", registry)
	selection, err := s.Select(SelectionRequest{})
	if err != nil {
		t.Fatalf("Select() error: %v", err)
	}
	if selection.Location != ExecGateway {
		t.Fatalf("expected gateway location, got %v", selection.Location)
	}
	if selection.NodeID != "gateway" {
		t.Fatalf("expected gateway node, got %s", selection.NodeID)
	}
}

func TestSchedulerSelectsRemoteWhenGatewayLacksRequiredCapability(t *testing.T) {
	registry := NewRegistry(time.Minute)
	registry.Upsert(NodeInfo{
		NodeID:       "gateway",
		IsGateway:    true,
		Capabilities: []Capability{{Kind: CapabilityAgent, Name: "agent"}},
	})
	registry.Upsert(NodeInfo{
		NodeID: "node-gpu",
		Capabilities: []Capability{
			{Kind: CapabilityAgent, Name: "agent"},
			{Kind: CapabilityTool, Name: "gpu"},
		},
	})

	s := NewScheduler("gateway", registry)
	selection, err := s.Select(SelectionRequest{RequiredCapabilities: []Capability{{Kind: CapabilityTool, Name: "gpu"}}})
	if err != nil {
		t.Fatalf("Select() error: %v", err)
	}
	if selection.Location != ExecNode {
		t.Fatalf("expected remote node selection, got %v", selection.Location)
	}
	if selection.NodeID != "node-gpu" {
		t.Fatalf("expected node-gpu, got %s", selection.NodeID)
	}
}

func TestRegistryPruneKeepsGatewayAndRemovesExpiredNodes(t *testing.T) {
	registry := NewRegistry(10 * time.Second)
	now := time.Now().UTC()
	registry.Upsert(NodeInfo{NodeID: "gateway", IsGateway: true, LastHeartbeat: now.Add(-time.Hour)})
	registry.Upsert(NodeInfo{NodeID: "node-a", LastHeartbeat: now.Add(-time.Hour)})

	removed := registry.PruneExpired(now)
	if len(removed) != 1 || removed[0] != "node-a" {
		t.Fatalf("expected node-a to be pruned, got %v", removed)
	}
	if _, ok := registry.Get("gateway"); !ok {
		t.Fatalf("expected gateway to remain registered")
	}
}
