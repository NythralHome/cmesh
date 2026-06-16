package manager

import (
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
)

func TestModelSummariesHideLegacyUnsupportedJobError(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test"}}
	summaries := modelSummaries(catalog, []jobs.Job{
		{
			ID:        "job-old",
			Type:      models.JobInstall,
			Status:    jobs.StatusFailed,
			Input:     `{"model_id":"qwen-test"}`,
			Error:     `unsupported job type "model.install"`,
			UpdatedAt: time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC),
		},
	}, nil)

	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	if summaries[0].LastError != "" {
		t.Fatalf("expected legacy unsupported job error to be hidden, got %q", summaries[0].LastError)
	}
	if summaries[0].LastJobID != "job-old" {
		t.Fatalf("expected last job id to remain visible, got %q", summaries[0].LastJobID)
	}
}

func TestModelSummariesKeepActionableJobError(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test"}}
	summaries := modelSummaries(catalog, []jobs.Job{
		{
			ID:        "job-new",
			Type:      models.JobInstall,
			Status:    jobs.StatusFailed,
			Input:     `{"model_id":"qwen-test"}`,
			Error:     "download failed",
			UpdatedAt: time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC),
		},
	}, nil)

	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	if summaries[0].LastError != "download failed" {
		t.Fatalf("expected actionable error to stay visible, got %q", summaries[0].LastError)
	}
}
