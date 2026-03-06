package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
	"github.com/bstncartwright/gopher/pkg/scheduler"
)

func TestNodeRegistrySyncStoresAdvertisedAgents(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	registry := scheduler.NewRegistry(0)
	syncer, err := NewNodeRegistrySync(NodeRegistrySyncOptions{
		Fabric:   fabric,
		Registry: registry,
	})
	if err != nil {
		t.Fatalf("NewNodeRegistrySync() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := syncer.Start(ctx); err != nil {
		t.Fatalf("syncer.Start() error: %v", err)
	}
	defer syncer.Stop()

	blob, err := json.Marshal(node.CapabilityAnnouncement{
		NodeID:    "node-browser",
		Agents:    []string{"browser"},
		Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal announcement: %v", err)
	}
	if err := fabric.Publish(ctx, fabricts.Message{
		Subject: fabricts.NodeCapabilitiesSubject("node-browser"),
		Data:    blob,
	}); err != nil {
		t.Fatalf("publish announcement: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		nodeInfo, ok := registry.Get("node-browser")
		if ok {
			if len(nodeInfo.Agents) != 1 || nodeInfo.Agents[0] != "browser" {
				t.Fatalf("registry agents = %#v, want [browser]", nodeInfo.Agents)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for registry update")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
