package workerstatus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const FileName = "worker-job-status.json"

type JobStatus struct {
	State      string     `json:"state"`
	NodeID     string     `json:"node_id,omitempty"`
	JobID      string     `json:"job_id,omitempty"`
	Type       string     `json:"type,omitempty"`
	Input      string     `json:"input,omitempty"`
	Result     string     `json:"result,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

func Path(cacheDir string) string {
	if cacheDir == "" {
		cacheDir = "."
	}
	return filepath.Join(cacheDir, FileName)
}

func Read(cacheDir string) (JobStatus, bool) {
	body, err := os.ReadFile(Path(cacheDir))
	if errors.Is(err, os.ErrNotExist) {
		return JobStatus{}, false
	}
	if err != nil {
		return JobStatus{}, false
	}
	var status JobStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return JobStatus{}, false
	}
	return status, true
}

func Write(cacheDir string, status JobStatus) error {
	path := Path(cacheDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	status.UpdatedAt = time.Now().UTC()
	body, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func MarkIdle(cacheDir string, nodeID string) error {
	status := JobStatus{
		State:  "idle",
		NodeID: nodeID,
	}
	return Write(cacheDir, status)
}
