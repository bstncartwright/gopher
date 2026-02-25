package node

type AdminAction string

const (
	AdminActionConfigure AdminAction = "configure"
	AdminActionRestart   AdminAction = "restart"
	AdminActionUpdate    AdminAction = "update"
)

type AdminConfigureNATS struct {
	URL            *string `json:"url,omitempty"`
	ConnectTimeout *string `json:"connect_timeout,omitempty"`
	ReconnectWait  *string `json:"reconnect_wait,omitempty"`
}

type AdminConfigureRuntime struct {
	HeartbeatInterval *string `json:"heartbeat_interval,omitempty"`
}

type AdminConfigureRequest struct {
	NodeID       *string                `json:"node_id,omitempty"`
	NATS         *AdminConfigureNATS    `json:"nats,omitempty"`
	Runtime      *AdminConfigureRuntime `json:"runtime,omitempty"`
	Capabilities *[]string              `json:"capabilities,omitempty"`
}

type AdminUpdateRequest struct {
	TargetVersion *string `json:"target_version,omitempty"`
}

type AdminRequest struct {
	Action    AdminAction            `json:"action"`
	Configure *AdminConfigureRequest `json:"configure,omitempty"`
	Update    *AdminUpdateRequest    `json:"update,omitempty"`
}

type AdminResponse struct {
	OK               bool     `json:"ok"`
	Error            string   `json:"error,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	PersistedPath    string   `json:"persisted_path,omitempty"`
	RestartRequested bool     `json:"restart_requested,omitempty"`
	UpdateRequested  bool     `json:"update_requested,omitempty"`
}

type AdminHandler interface {
	HandleAdmin(req AdminRequest) AdminResponse
}
