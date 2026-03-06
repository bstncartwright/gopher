package node

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
)

func TestRuntimePublishesAdvertisedAgents(t *testing.T) {
	fabric := fabricts.NewInMemoryBus()
	capabilitiesCh := make(chan CapabilityAnnouncement, 1)
	heartbeatCh := make(chan Heartbeat, 1)

	_, err := fabric.Subscribe(fabricts.NodeCapabilitiesSubject("node-browser"), func(_ context.Context, message fabricts.Message) {
		var announcement CapabilityAnnouncement
		if err := json.Unmarshal(message.Data, &announcement); err != nil {
			t.Errorf("unmarshal capability announcement: %v", err)
			return
		}
		capabilitiesCh <- announcement
	})
	if err != nil {
		t.Fatalf("subscribe capabilities: %v", err)
	}
	_, err = fabric.Subscribe(fabricts.NodeStatusSubject("node-browser"), func(_ context.Context, message fabricts.Message) {
		var heartbeat Heartbeat
		if err := json.Unmarshal(message.Data, &heartbeat); err != nil {
			t.Errorf("unmarshal heartbeat: %v", err)
			return
		}
		heartbeatCh <- heartbeat
	})
	if err != nil {
		t.Fatalf("subscribe heartbeat: %v", err)
	}

	runtime, err := NewRuntime(RuntimeOptions{
		NodeID:   "node-browser",
		Agents:   []string{"main", "browser", "browser"},
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

	select {
	case announcement := <-capabilitiesCh:
		if len(announcement.Agents) != 2 || announcement.Agents[0] != "browser" || announcement.Agents[1] != "main" {
			t.Fatalf("announcement agents = %#v, want [browser main]", announcement.Agents)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for capability announcement: %v", ctx.Err())
	}

	select {
	case heartbeat := <-heartbeatCh:
		if len(heartbeat.Agents) != 2 || heartbeat.Agents[0] != "browser" || heartbeat.Agents[1] != "main" {
			t.Fatalf("heartbeat agents = %#v, want [browser main]", heartbeat.Agents)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for heartbeat: %v", ctx.Err())
	}
}
