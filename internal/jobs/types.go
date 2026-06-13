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
	ID          string
	Type        string
	Status      Status
	RequestedBy string
	AssignedTo  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
