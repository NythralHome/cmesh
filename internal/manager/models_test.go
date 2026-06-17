package manager

import (
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
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

func TestModelSummariesUseWorkerReportedInventory(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test"}}
	nodes := []cluster.Node{
		{
			ID:     "node-online",
			Name:   "worker-a",
			Role:   cluster.NodeRoleWorker,
			Status: cluster.NodeStatusOnline,
			Resources: cluster.ResourceSnapshot{
				Models: []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Bytes: 123}},
				Runtimes: []cluster.RuntimeResource{{
					Name:  "llama.cpp",
					Ready: true,
				}},
			},
		},
	}

	summaries := modelSummaries(catalog, nil, nodes)
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	if summaries[0].Status != "installed" {
		t.Fatalf("expected installed status, got %q", summaries[0].Status)
	}
	if len(summaries[0].InstalledOn) != 1 || summaries[0].InstalledOn[0] != "node-online" {
		t.Fatalf("expected installed node from inventory, got %#v", summaries[0].InstalledOn)
	}
	if len(summaries[0].GeneratableOn) != 1 || summaries[0].GeneratableOn[0] != "node-online" {
		t.Fatalf("expected ready runtime node from inventory, got %#v", summaries[0].GeneratableOn)
	}
}

func TestModelSummariesDoNotGenerateWithoutReadyRuntime(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test", Runtime: "llama.cpp"}}
	nodes := []cluster.Node{
		{
			ID:     "node-online",
			Name:   "worker-a",
			Role:   cluster.NodeRoleWorker,
			Status: cluster.NodeStatusOnline,
			Resources: cluster.ResourceSnapshot{
				Models: []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Bytes: 123}},
				Runtimes: []cluster.RuntimeResource{{
					Name:  "llama.cpp",
					Ready: false,
					Error: "llama-cli missing",
				}},
			},
		},
	}

	summaries := modelSummaries(catalog, nil, nodes)
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	if len(summaries[0].InstalledOn) != 1 {
		t.Fatalf("expected installed node, got %#v", summaries[0].InstalledOn)
	}
	if len(summaries[0].GeneratableOn) != 0 {
		t.Fatalf("expected no generatable nodes without runtime, got %#v", summaries[0].GeneratableOn)
	}
}

func TestModelSummariesIgnoreStaleInstallJobWithoutInventory(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test"}}
	summaries := modelSummaries(catalog, []jobs.Job{
		{
			ID:         "job-install",
			Type:       models.JobInstall,
			Status:     jobs.StatusSucceeded,
			Input:      `{"model_id":"qwen-test"}`,
			AssignedTo: "old-node",
			UpdatedAt:  time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC),
		},
	}, []cluster.Node{
		{
			ID:     "old-node",
			Name:   "worker-a",
			Role:   cluster.NodeRoleWorker,
			Status: cluster.NodeStatusOffline,
		},
	})

	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	if summaries[0].Status != "available" {
		t.Fatalf("expected available status without live inventory, got %q", summaries[0].Status)
	}
	if len(summaries[0].InstalledOn) != 0 {
		t.Fatalf("expected no installed nodes, got %#v", summaries[0].InstalledOn)
	}
}

func TestModelCapabilitiesReportBusyJobSlots(t *testing.T) {
	catalog := []models.Model{{
		ID:          "qwen-test",
		Name:        "Qwen Test",
		MemoryBytes: 1 * gb,
		DiskBytes:   1 * gb,
	}}
	nodes := []cluster.Node{
		{
			ID:     "node-busy",
			Name:   "busy-worker",
			Role:   cluster.NodeRoleWorker,
			Status: cluster.NodeStatusOnline,
			Resources: cluster.ResourceSnapshot{
				CPU:      cluster.CPUResources{CoresAllowed: 4},
				Memory:   cluster.MemoryResources{AllowedBytes: 8 * gb},
				Storage:  cluster.StorageResources{AllowedBytes: 8 * gb},
				JobSlots: 1,
			},
		},
	}
	runningJobs := []jobs.Job{{
		ID:         "job-running",
		Status:     jobs.StatusRunning,
		AssignedTo: "node-busy",
	}}

	summaries := modelSummaries(catalog, runningJobs, nodes)
	if summaries[0].CapableNodes != 0 {
		t.Fatalf("expected busy worker not to count as capable, got %d", summaries[0].CapableNodes)
	}
	if len(summaries[0].Capabilities) != 1 {
		t.Fatalf("expected one capability, got %#v", summaries[0].Capabilities)
	}
	capability := summaries[0].Capabilities[0]
	if capability.Capable {
		t.Fatalf("expected busy worker to be blocked")
	}
	if capability.ActiveJobs != 1 || capability.JobSlots != 1 {
		t.Fatalf("expected active slot metadata, got %#v", capability)
	}
	if len(capability.Reasons) != 1 || capability.Reasons[0] != "all job slots busy (1/1)" {
		t.Fatalf("expected busy slot reason, got %#v", capability.Reasons)
	}
}

func TestModelCapabilitiesReportFreeDiskShortage(t *testing.T) {
	catalog := []models.Model{{
		ID:          "large-test",
		Name:        "Large Test",
		MemoryBytes: 1 * gb,
		DiskBytes:   8 * gb,
	}}
	nodes := []cluster.Node{{
		ID:     "node-low-free",
		Name:   "low-free-worker",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Memory:   cluster.MemoryResources{AllowedBytes: 16 * gb},
			Storage:  cluster.StorageResources{AllowedBytes: 20 * gb, FreeBytes: 2 * gb},
			JobSlots: 1,
		},
	}}

	summaries := modelSummaries(catalog, nil, nodes)
	capability := summaries[0].Capabilities[0]
	if capability.Capable {
		t.Fatalf("expected low free disk worker to be blocked")
	}
	if capability.FreeStorageBytes != 2*gb {
		t.Fatalf("expected free storage metadata, got %#v", capability)
	}
	if len(capability.Reasons) != 1 || capability.Reasons[0] != "free disk short by 6.0 GB" {
		t.Fatalf("expected free disk reason, got %#v", capability.Reasons)
	}
}
