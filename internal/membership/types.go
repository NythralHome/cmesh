package membership

import (
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
)

type JoinRequest struct {
	NodeName  string                   `json:"node_name"`
	Role      cluster.NodeRole         `json:"role"`
	Endpoint  string                   `json:"endpoint"`
	JoinToken string                   `json:"join_token"`
	Resources cluster.ResourceSnapshot `json:"resources"`
}

type JoinResponse struct {
	NodeID         string        `json:"node_id"`
	NodeAuthToken  string        `json:"node_auth_token,omitempty"`
	ManagerPeers   []string      `json:"manager_peers"`
	HeartbeatEvery time.Duration `json:"heartbeat_every"`
}

type Heartbeat struct {
	NodeID    string                   `json:"node_id"`
	At        time.Time                `json:"at"`
	Resources cluster.ResourceSnapshot `json:"resources"`
}

type LeaveRequest struct {
	NodeID string    `json:"node_id"`
	At     time.Time `json:"at"`
}
