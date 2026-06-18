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
	CPU      CPUResources      `json:"cpu"`
	Memory   MemoryResources   `json:"memory"`
	GPU      []GPUResources    `json:"gpu"`
	Storage  StorageResources  `json:"storage"`
	JobSlots int               `json:"job_slots,omitempty"`
	Models   []ModelResource   `json:"models,omitempty"`
	Runtimes []RuntimeResource `json:"runtimes,omitempty"`
}

type ModelResource struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Family      string    `json:"family,omitempty"`
	Runtime     string    `json:"runtime,omitempty"`
	Path        string    `json:"path"`
	Bytes       uint64    `json:"bytes"`
	Ready       bool      `json:"ready"`
	Error       string    `json:"error,omitempty"`
	InstalledAt time.Time `json:"installed_at,omitempty"`
}

type RuntimeResource struct {
	Name         string   `json:"name"`
	Ready        bool     `json:"ready"`
	Version      string   `json:"version,omitempty"`
	Platform     string   `json:"platform,omitempty"`
	BinaryPath   string   `json:"binary_path,omitempty"`
	Source       string   `json:"source,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Error        string   `json:"error,omitempty"`
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
	TotalBytes          uint64 `json:"total_bytes"`
	AllowedBytes        uint64 `json:"allowed_bytes"`
	FreeBytes           uint64 `json:"free_bytes"`
	UsedByModelsBytes   uint64 `json:"used_by_models_bytes,omitempty"`
	UsedByRuntimesBytes uint64 `json:"used_by_runtimes_bytes,omitempty"`
	UsedByCacheBytes    uint64 `json:"used_by_cache_bytes,omitempty"`
	PartialModelBytes   uint64 `json:"partial_model_bytes,omitempty"`
	PartialModelFiles   int    `json:"partial_model_files,omitempty"`
	OrphanModelBytes    uint64 `json:"orphan_model_bytes,omitempty"`
	OrphanModelDirs     int    `json:"orphan_model_dirs,omitempty"`
}
