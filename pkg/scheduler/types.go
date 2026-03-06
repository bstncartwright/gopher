package scheduler

import "time"

type ExecutionLocation int

const (
	ExecGateway ExecutionLocation = iota
	ExecNode
)

type CapabilityKind int

const (
	CapabilityAgent CapabilityKind = iota
	CapabilityTool
	CapabilitySystem
)

type Capability struct {
	Kind CapabilityKind `json:"kind"`
	Name string         `json:"name"`
}

type NodeInfo struct {
	NodeID        string       `json:"node_id"`
	IsGateway     bool         `json:"is_gateway"`
	Version       string       `json:"version,omitempty"`
	Capabilities  []Capability `json:"capabilities"`
	Agents        []string     `json:"agents,omitempty"`
	LastHeartbeat time.Time    `json:"last_heartbeat"`
}

type SelectionRequest struct {
	RequiredCapabilities []Capability
}

type Selection struct {
	Location ExecutionLocation
	NodeID   string
}
