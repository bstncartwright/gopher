package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	fabricts "github.com/bstncartwright/gopher/pkg/fabric/nats"
	"github.com/bstncartwright/gopher/pkg/node"
)

func TestNodeUpdateCoordinatorRequestsUpdateForOutdatedNode(t *testing.T) {
	bus := fabricts.NewInMemoryBus()
	requests := make(chan node.AdminRequest, 4)
	_, err := bus.Subscribe(fabricts.NodeAdminSubject("node-a"), func(ctx context.Context, message fabricts.Message) {
		var request node.AdminRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		requests <- request
		response, _ := json.Marshal(node.AdminResponse{OK: true, UpdateRequested: true})
		_ = bus.Publish(ctx, fabricts.Message{Subject: message.Reply, Data: response})
	})
	if err != nil {
		t.Fatalf("subscribe admin subject: %v", err)
	}

	coordinator, err := NewNodeUpdateCoordinator(NodeUpdateCoordinatorOptions{
		Fabric:         bus,
		GatewayNodeID:  "gateway",
		GatewayVersion: "v1.2.3",
		RetryInterval:  time.Hour,
	})
	if err != nil {
		t.Fatalf("NewNodeUpdateCoordinator() error: %v", err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("coordinator.Start() error: %v", err)
	}
	defer coordinator.Stop()

	publishHeartbeat(t, bus, node.Heartbeat{NodeID: "node-a", Version: "v1.2.2"})
	first := waitAdminRequest(t, requests, "first stale heartbeat")
	if first.Action != node.AdminActionUpdate {
		t.Fatalf("action = %q, want update", first.Action)
	}
	if first.Update == nil || first.Update.TargetVersion == nil || *first.Update.TargetVersion != "v1.2.3" {
		t.Fatalf("update payload = %#v", first.Update)
	}

	publishHeartbeat(t, bus, node.Heartbeat{NodeID: "node-a", Version: "v1.2.2"})
	ensureNoAdminRequest(t, requests, 150*time.Millisecond)

	publishHeartbeat(t, bus, node.Heartbeat{NodeID: "node-a", Version: "v1.2.3"})
	publishHeartbeat(t, bus, node.Heartbeat{NodeID: "node-a", Version: "v1.2.2"})
	_ = waitAdminRequest(t, requests, "stale heartbeat after node was current")
}

func TestNodeUpdateCoordinatorSkipsNonReleaseGatewayVersion(t *testing.T) {
	bus := fabricts.NewInMemoryBus()
	requests := make(chan node.AdminRequest, 1)
	_, err := bus.Subscribe(fabricts.NodeAdminSubject("node-a"), func(ctx context.Context, message fabricts.Message) {
		var request node.AdminRequest
		if err := json.Unmarshal(message.Data, &request); err == nil {
			requests <- request
		}
	})
	if err != nil {
		t.Fatalf("subscribe admin subject: %v", err)
	}

	coordinator, err := NewNodeUpdateCoordinator(NodeUpdateCoordinatorOptions{
		Fabric:         bus,
		GatewayNodeID:  "gateway",
		GatewayVersion: "dev",
	})
	if err != nil {
		t.Fatalf("NewNodeUpdateCoordinator() error: %v", err)
	}
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("coordinator.Start() error: %v", err)
	}
	defer coordinator.Stop()

	publishHeartbeat(t, bus, node.Heartbeat{NodeID: "node-a", Version: "v1.2.2"})
	ensureNoAdminRequest(t, requests, 150*time.Millisecond)
}

func publishHeartbeat(t *testing.T, bus *fabricts.InMemoryBus, heartbeat node.Heartbeat) {
	t.Helper()
	blob, err := json.Marshal(heartbeat)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	if err := bus.Publish(context.Background(), fabricts.Message{
		Subject: fabricts.NodeStatusSubject(heartbeat.NodeID),
		Data:    blob,
	}); err != nil {
		t.Fatalf("publish heartbeat: %v", err)
	}
}

func waitAdminRequest(t *testing.T, requests <-chan node.AdminRequest, label string) node.AdminRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for admin request: %s", label)
		return node.AdminRequest{}
	}
}

func ensureNoAdminRequest(t *testing.T, requests <-chan node.AdminRequest, wait time.Duration) {
	t.Helper()
	select {
	case request := <-requests:
		t.Fatalf("unexpected admin request: %#v", request)
	case <-time.After(wait):
	}
}
