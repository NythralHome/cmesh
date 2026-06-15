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

type Job struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	Status      Status    `json:"status"`
	RequestedBy string    `json:"requested_by"`
	AssignedTo  string    `json:"assigned_to"`
	Input       string    `json:"input"`
	Result      string    `json:"result"`
	Error       string    `json:"error"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
}

type CreateRequest struct {
	Type        string `json:"type"`
	Input       string `json:"input"`
	RequestedBy string `json:"requested_by"`
	AssignedTo  string `json:"assigned_to,omitempty"`
}

type CompleteRequest struct {
	NodeID string `json:"node_id"`
	Result string `json:"result"`
	Error  string `json:"error"`
}
