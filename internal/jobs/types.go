package jobs

import "time"

type Status string

const (
	StatusQueued    Status = "queued"
	StatusScheduled Status = "scheduled"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

const DefaultMaxAttempts = 3

type Job struct {
	ID           string       `json:"id"`
	Type         string       `json:"type"`
	Status       Status       `json:"status"`
	RequestedBy  string       `json:"requested_by"`
	AssignedTo   string       `json:"assigned_to"`
	Input        string       `json:"input"`
	Requirements Requirements `json:"requirements"`
	Result       string       `json:"result"`
	Error        string       `json:"error"`
	Attempts     int          `json:"attempts"`
	MaxAttempts  int          `json:"max_attempts"`
	LastFailure  string       `json:"last_failure"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	StartedAt    time.Time    `json:"started_at,omitempty"`
	FinishedAt   time.Time    `json:"finished_at,omitempty"`
}

type CreateRequest struct {
	Type         string       `json:"type"`
	Input        string       `json:"input"`
	RequestedBy  string       `json:"requested_by"`
	AssignedTo   string       `json:"assigned_to,omitempty"`
	Requirements Requirements `json:"requirements,omitempty"`
	MaxAttempts  int          `json:"max_attempts,omitempty"`
	NoAutoAssign bool         `json:"no_auto_assign,omitempty"`
}

type CompleteRequest struct {
	NodeID string `json:"node_id"`
	Result string `json:"result"`
	Error  string `json:"error"`
}

type ProgressRequest struct {
	NodeID          string  `json:"node_id"`
	ProgressBytes   int64   `json:"progress_bytes,omitempty"`
	TotalBytes      int64   `json:"total_bytes,omitempty"`
	ProgressPercent float64 `json:"progress_percent,omitempty"`
	ProgressLabel   string  `json:"progress_label,omitempty"`
}

type Requirements struct {
	CPUCores    int    `json:"cpu_cores,omitempty"`
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
	DiskBytes   uint64 `json:"disk_bytes,omitempty"`
	GPURequired bool   `json:"gpu_required,omitempty"`
	VRAMBytes   uint64 `json:"vram_bytes,omitempty"`
}
