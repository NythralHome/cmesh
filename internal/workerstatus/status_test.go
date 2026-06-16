package workerstatus

import (
	"testing"
	"time"
)

func TestMarkIdleClearsPreviousJobDetails(t *testing.T) {
	cacheDir := t.TempDir()
	startedAt := time.Now().UTC().Add(-time.Minute)
	finishedAt := time.Now().UTC()

	if err := Write(cacheDir, JobStatus{
		State:      "succeeded",
		NodeID:     "node-old",
		JobID:      "job-old",
		Type:       "compute.matrix_multiply",
		Input:      `{"size":32,"iterations":1}`,
		Result:     `{"gflops":1.23}`,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
	}); err != nil {
		t.Fatal(err)
	}

	if err := MarkIdle(cacheDir, "node-new"); err != nil {
		t.Fatal(err)
	}

	status, ok := Read(cacheDir)
	if !ok {
		t.Fatal("expected worker status")
	}
	if status.State != "idle" {
		t.Fatalf("expected idle state, got %q", status.State)
	}
	if status.NodeID != "node-new" {
		t.Fatalf("expected node-new, got %q", status.NodeID)
	}
	if status.JobID != "" || status.Type != "" || status.Result != "" || status.StartedAt != nil || status.FinishedAt != nil {
		t.Fatalf("expected idle status to clear previous job details, got %#v", status)
	}
}
