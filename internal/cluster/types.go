package cluster

import "time"

type NodeRole string

const (
	NodeRoleManager NodeRole = "manager"
	NodeRoleWorker  NodeRole = "worker"
)

type NodeStatus string

const (
	NodeStatusJoining  NodeStatus = "joining"
	NodeStatusOnline   NodeStatus = "online"
	NodeStatusOffline  NodeStatus = "offline"
	NodeStatusDraining NodeStatus = "draining"
)

type Node struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Role      NodeRole         `json:"role"`
	Status    NodeStatus       `json:"status"`
	Endpoint  string           `json:"endpoint"`
	Resources ResourceSnapshot `json:"resources"`
	JoinedAt  time.Time        `json:"joined_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

type ResourceSnapshot struct {
	CPU      CPUResources     `json:"cpu"`
	Memory   MemoryResources  `json:"memory"`
	GPU      []GPUResources   `json:"gpu"`
	Storage  StorageResources `json:"storage"`
	JobSlots int              `json:"job_slots,omitempty"`
}

type CPUResources struct {
	CoresTotal   int `json:"cores_total"`
	CoresAllowed int `json:"cores_allowed"`
}

type MemoryResources struct {
	TotalBytes   uint64 `json:"total_bytes"`
	AllowedBytes uint64 `json:"allowed_bytes"`
}

type GPUResources struct {
	Name              string `json:"name"`
	Vendor            string `json:"vendor"`
	TotalVRAMBytes    uint64 `json:"total_vram_bytes"`
	AllowedVRAMBytes  uint64 `json:"allowed_vram_bytes"`
	ComputeCompatible bool   `json:"compute_compatible"`
}

type StorageResources struct {
	TotalBytes   uint64 `json:"total_bytes"`
	AllowedBytes uint64 `json:"allowed_bytes"`
	FreeBytes    uint64 `json:"free_bytes"`
}
