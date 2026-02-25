package scheduler

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultHeartbeatTTL = 15 * time.Second

type Registry struct {
	mu  sync.RWMutex
	ttl time.Duration

	nodes map[string]NodeInfo
}

func NewRegistry(ttl time.Duration) *Registry {
	if ttl <= 0 {
		ttl = defaultHeartbeatTTL
	}
	return &Registry{
		ttl:   ttl,
		nodes: make(map[string]NodeInfo),
	}
}

func (r *Registry) Upsert(node NodeInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	node.NodeID = strings.TrimSpace(node.NodeID)
	if node.NodeID == "" {
		return
	}
	node.Version = strings.TrimSpace(node.Version)
	if node.LastHeartbeat.IsZero() {
		node.LastHeartbeat = time.Now().UTC()
	}
	node.Capabilities = normalizeCapabilities(node.Capabilities)
	r.nodes[node.NodeID] = node
}

func (r *Registry) SetCapabilities(nodeID string, capabilities []Capability, isGateway bool, version string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	node := r.nodes[nodeID]
	node.NodeID = nodeID
	node.IsGateway = isGateway
	node.Version = strings.TrimSpace(version)
	node.Capabilities = normalizeCapabilities(capabilities)
	if at.IsZero() {
		at = time.Now().UTC()
	}
	node.LastHeartbeat = maxTime(node.LastHeartbeat, at)
	r.nodes[nodeID] = node
}

func (r *Registry) RecordHeartbeat(nodeID string, isGateway bool, version string, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return
	}
	node := r.nodes[nodeID]
	node.NodeID = nodeID
	node.IsGateway = isGateway
	node.Version = strings.TrimSpace(version)
	if at.IsZero() {
		at = time.Now().UTC()
	}
	node.LastHeartbeat = maxTime(node.LastHeartbeat, at)
	r.nodes[nodeID] = node
}

func (r *Registry) PruneExpired(now time.Time) []string {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	removed := []string{}
	for nodeID, node := range r.nodes {
		if node.IsGateway {
			continue
		}
		if node.LastHeartbeat.IsZero() {
			delete(r.nodes, nodeID)
			removed = append(removed, nodeID)
			continue
		}
		if now.Sub(node.LastHeartbeat) > r.ttl {
			delete(r.nodes, nodeID)
			removed = append(removed, nodeID)
		}
	}
	sort.Strings(removed)
	return removed
}

func (r *Registry) Get(nodeID string) (NodeInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	node, ok := r.nodes[strings.TrimSpace(nodeID)]
	if !ok {
		return NodeInfo{}, false
	}
	node.Capabilities = append([]Capability(nil), node.Capabilities...)
	return node, true
}

func (r *Registry) Snapshot() []NodeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodes := make([]NodeInfo, 0, len(r.nodes))
	for _, node := range r.nodes {
		node.Capabilities = append([]Capability(nil), node.Capabilities...)
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].NodeID < nodes[j].NodeID
	})
	return nodes
}

func (r *Registry) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.nodes, strings.TrimSpace(nodeID))
}

func normalizeCapabilities(capabilities []Capability) []Capability {
	if len(capabilities) == 0 {
		return []Capability{}
	}
	set := make(map[string]Capability, len(capabilities))
	for _, capability := range capabilities {
		name := strings.TrimSpace(capability.Name)
		if name == "" {
			continue
		}
		key := capabilityKey(capability)
		set[key] = Capability{Kind: capability.Kind, Name: name}
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]Capability, 0, len(keys))
	for _, key := range keys {
		out = append(out, set[key])
	}
	return out
}

func capabilityKey(capability Capability) string {
	return strings.TrimSpace(capability.Name) + "::" + strconv.Itoa(int(capability.Kind))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
