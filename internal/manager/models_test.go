package manager

import (
	"strings"
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
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test", Runtime: models.RuntimeLlamaCPP}}
	installedAt := time.Date(2026, 6, 17, 12, 30, 0, 0, time.UTC)
	nodes := []cluster.Node{
		{
			ID:     "node-online",
			Name:   "worker-a",
			Role:   cluster.NodeRoleWorker,
			Status: cluster.NodeStatusOnline,
			Resources: cluster.ResourceSnapshot{
				Models:  []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Family: "Qwen", Runtime: "llama.cpp", Bytes: 123, Ready: true, Error: "manifest missing", InstalledAt: installedAt}},
				Storage: cluster.StorageResources{AllowedBytes: 10 * gb, FreeBytes: 7 * gb, UsedByModelsBytes: 3 * gb, UsedByCacheBytes: 4 * gb},
				Runtimes: []cluster.RuntimeResource{{
					Name:       "llama.cpp",
					Ready:      true,
					Version:    "b9672",
					BinaryPath: "/tmp/llama-cli",
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
	if len(summaries[0].Installed) != 1 {
		t.Fatalf("expected installed metadata, got %#v", summaries[0].Installed)
	}
	install := summaries[0].Installed[0]
	if install.NodeID != "node-online" || install.NodeName != "worker-a" || install.Bytes != 123 {
		t.Fatalf("unexpected installed metadata: %#v", install)
	}
	if install.Family != "Qwen" || install.Runtime != "llama.cpp" || !install.InstalledAt.Equal(installedAt) {
		t.Fatalf("expected inventory metadata on install, got %#v", install)
	}
	if !install.ModelReady || install.ModelError != "manifest missing" {
		t.Fatalf("expected model inventory status on install, got %#v", install)
	}
	if !install.Repairable || install.RepairReason != "manifest missing" {
		t.Fatalf("expected repair metadata on install, got %#v", install)
	}
	if !install.RuntimeReady || install.RuntimeStatus.Version != "b9672" || install.RuntimeStatus.BinaryPath != "/tmp/llama-cli" {
		t.Fatalf("expected runtime metadata on install, got %#v", install)
	}
	if install.AllowedStorageBytes != 10*gb || install.FreeStorageBytes != 7*gb || install.UsedByModelsBytes != 3*gb || install.UsedByCacheBytes != 4*gb {
		t.Fatalf("expected worker storage metadata on install, got %#v", install)
	}
}

func TestInstalledModelCanStillInstallOnAnotherWorker(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test", Runtime: models.RuntimeLlamaCPP, MemoryBytes: 1 * gb, DiskBytes: 1 * gb}}
	nodes := []cluster.Node{
		{
			ID:     "node-installed",
			Name:   "worker-a",
			Role:   cluster.NodeRoleWorker,
			Status: cluster.NodeStatusOnline,
			Resources: cluster.ResourceSnapshot{
				Memory:  cluster.MemoryResources{AllowedBytes: 8 * gb},
				Storage: cluster.StorageResources{AllowedBytes: 8 * gb, FreeBytes: 8 * gb},
				Models:  []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Bytes: 123, Ready: true}},
			},
		},
		{
			ID:     "node-empty",
			Name:   "worker-b",
			Role:   cluster.NodeRoleWorker,
			Status: cluster.NodeStatusOnline,
			Resources: cluster.ResourceSnapshot{
				Memory:  cluster.MemoryResources{AllowedBytes: 8 * gb},
				Storage: cluster.StorageResources{AllowedBytes: 8 * gb, FreeBytes: 8 * gb},
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
	if summaries[0].CapableNodes != 1 {
		t.Fatalf("expected one remaining install target, got %d: %#v", summaries[0].CapableNodes, summaries[0].Capabilities)
	}
	for _, capability := range summaries[0].Capabilities {
		switch capability.NodeID {
		case "node-installed":
			if capability.Capable || !capability.Installed || len(capability.Reasons) != 1 || capability.Reasons[0] != "already installed" {
				t.Fatalf("expected installed worker to be blocked as already installed, got %#v", capability)
			}
		case "node-empty":
			if !capability.Capable || capability.Installed {
				t.Fatalf("expected empty worker to remain installable, got %#v", capability)
			}
		default:
			t.Fatalf("unexpected capability %#v", capability)
		}
	}
}

func TestModelSummariesExposeActiveInstalledModelJob(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test", Runtime: models.RuntimeLlamaCPP, MemoryBytes: 1 * gb, DiskBytes: 1 * gb}}
	nodes := []cluster.Node{{
		ID:     "node-installed",
		Name:   "worker-a",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Memory:  cluster.MemoryResources{AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{AllowedBytes: 8 * gb, FreeBytes: 8 * gb},
			Models:  []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Bytes: 123, Ready: true}},
		},
	}}
	activeJobs := []jobs.Job{{
		ID:         "job-active",
		Type:       models.JobGenerate,
		Status:     jobs.StatusRunning,
		AssignedTo: "node-installed",
		Input:      `{"model_id":"qwen-test"}`,
	}}

	summaries := modelSummaries(catalog, activeJobs, nodes)
	if len(summaries) != 1 || len(summaries[0].Installed) != 1 {
		t.Fatalf("expected one installed model, got %#v", summaries)
	}
	if summaries[0].Installed[0].ActiveJobID != "job-active" {
		t.Fatalf("expected active job metadata, got %#v", summaries[0].Installed[0])
	}
}

func TestModelSummariesExposeRepairingStatus(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test", Runtime: models.RuntimeLlamaCPP, MemoryBytes: 1 * gb, DiskBytes: 1 * gb}}
	nodes := []cluster.Node{{
		ID:     "node-installed",
		Name:   "worker-a",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Memory:  cluster.MemoryResources{AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{AllowedBytes: 8 * gb, FreeBytes: 8 * gb},
			Models:  []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Bytes: 123, Ready: true}},
		},
	}}
	activeJobs := []jobs.Job{{
		ID:         "job-repair",
		Type:       models.JobRepair,
		Status:     jobs.StatusRunning,
		AssignedTo: "node-installed",
		Input:      `{"model_id":"qwen-test"}`,
	}}

	summaries := modelSummaries(catalog, activeJobs, nodes)
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %#v", summaries)
	}
	if summaries[0].Status != "repairing" || summaries[0].ActiveJobID != "job-repair" {
		t.Fatalf("expected repairing status, got %#v", summaries[0])
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
				Models: []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Bytes: 123, Ready: true}},
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

func TestModelSummariesBlockGenerationDuringActiveModelJob(t *testing.T) {
	catalog := []models.Model{{ID: "qwen-test", Name: "Qwen Test", Runtime: "llama.cpp"}}
	nodes := []cluster.Node{{
		ID:     "node-active",
		Name:   "active-worker",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Models: []cluster.ModelResource{{ID: "qwen-test", Name: "Qwen Test", Bytes: 123, Ready: true}},
			Runtimes: []cluster.RuntimeResource{{
				Name:  "llama.cpp",
				Ready: true,
			}},
		},
	}}
	runningJobs := []jobs.Job{{
		ID:         "job-active",
		Type:       models.JobGenerate,
		Status:     jobs.StatusRunning,
		AssignedTo: "node-active",
		Input:      `{"model_id":"qwen-test"}`,
	}}

	summaries := modelSummaries(catalog, runningJobs, nodes)
	if len(summaries[0].GeneratableOn) != 0 {
		t.Fatalf("expected no generatable nodes during active model job, got %#v", summaries[0].GeneratableOn)
	}
	if len(summaries[0].Installed) != 1 {
		t.Fatalf("expected installed model, got %#v", summaries[0].Installed)
	}
	install := summaries[0].Installed[0]
	if install.GenerateReady {
		t.Fatalf("expected generate blocked, got %#v", install)
	}
	if install.GenerateBlocked != "model has an active job on this worker: job-active" {
		t.Fatalf("expected active job blocked reason, got %#v", install)
	}
	if got := modelGenerateBlockedReason(summaries[0], "node-active"); got != install.GenerateBlocked {
		t.Fatalf("expected blocked reason %q, got %q", install.GenerateBlocked, got)
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

func TestModelCapabilitiesBlockActiveSameModelJob(t *testing.T) {
	catalog := []models.Model{{
		ID:          "qwen-test",
		Name:        "Qwen Test",
		MemoryBytes: 1 * gb,
		DiskBytes:   1 * gb,
	}}
	nodes := []cluster.Node{{
		ID:     "node-active",
		Name:   "active-worker",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			CPU:      cluster.CPUResources{CoresAllowed: 4},
			Memory:   cluster.MemoryResources{AllowedBytes: 8 * gb},
			Storage:  cluster.StorageResources{AllowedBytes: 8 * gb, FreeBytes: 8 * gb},
			JobSlots: 2,
		},
	}}
	runningJobs := []jobs.Job{{
		ID:         "job-active",
		Type:       models.JobInstall,
		Status:     jobs.StatusRunning,
		AssignedTo: "node-active",
		Input:      `{"model_id":"qwen-test"}`,
	}}

	summaries := modelSummaries(catalog, runningJobs, nodes)
	capability := summaries[0].Capabilities[0]
	if capability.Capable {
		t.Fatalf("expected active same-model job to block install target")
	}
	if capability.ActiveJobID != "job-active" {
		t.Fatalf("expected active model job id, got %#v", capability)
	}
	if len(capability.Reasons) != 1 || capability.Reasons[0] != "model job already active: job-active" {
		t.Fatalf("expected active model job reason, got %#v", capability.Reasons)
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

func TestModelCapabilitiesReportModelQuotaShortage(t *testing.T) {
	catalog := []models.Model{{
		ID:          "large-test",
		Name:        "Large Test",
		MemoryBytes: 1 * gb,
		DiskBytes:   8 * gb,
	}}
	nodes := []cluster.Node{{
		ID:     "node-quota-full",
		Name:   "quota-full-worker",
		Role:   cluster.NodeRoleWorker,
		Status: cluster.NodeStatusOnline,
		Resources: cluster.ResourceSnapshot{
			Memory: cluster.MemoryResources{AllowedBytes: 16 * gb},
			Storage: cluster.StorageResources{
				AllowedBytes:      20 * gb,
				FreeBytes:         30 * gb,
				UsedByModelsBytes: 16 * gb,
			},
			JobSlots: 1,
		},
	}}

	summaries := modelSummaries(catalog, nil, nodes)
	capability := summaries[0].Capabilities[0]
	if capability.Capable {
		t.Fatalf("expected quota-full worker to be blocked")
	}
	if len(capability.Reasons) != 1 || capability.Reasons[0] != "model quota short by 4.0 GB" {
		t.Fatalf("expected quota reason, got %#v", capability.Reasons)
	}
}

func TestModelFailureHintReportsCapabilityReasons(t *testing.T) {
	summary := ModelSummary{
		Capabilities: []ModelCapability{
			{Name: "small-worker", Reasons: []string{"RAM short by 2.0 GB", "disk short by 1.0 GB"}},
		},
	}

	got := modelFailureHint(summary)
	if !strings.Contains(got, "No capable worker yet") || !strings.Contains(got, "RAM short by 2.0 GB") {
		t.Fatalf("expected capability failure hint, got %q", got)
	}
}

func TestFilterAndSortModelSummaries(t *testing.T) {
	summaries := []ModelSummary{
		{
			Model: models.Model{
				ID:          "qwen-small",
				Name:        "Qwen Small",
				Family:      "Qwen",
				Parameters:  "0.5B",
				Quant:       "Q4_K_M",
				Runtime:     models.RuntimeLlamaCPP,
				Description: "small qwen model",
				MemoryBytes: 2 * gb,
				DiskBytes:   1 * gb,
			},
			Status:       "available",
			CapableNodes: 1,
		},
		{
			Model: models.Model{
				ID:          "gemma-large",
				Name:        "Gemma Large",
				Family:      "Gemma",
				Parameters:  "27B",
				Quant:       "Q4_K_M",
				Runtime:     models.RuntimeLlamaCPP,
				Description: "large gemma model",
				MemoryBytes: 32 * gb,
				DiskBytes:   18 * gb,
			},
			Status:       "available",
			CapableNodes: 0,
		},
		{
			Model: models.Model{
				ID:          "qwen-large",
				Name:        "Qwen Large",
				Family:      "Qwen",
				Parameters:  "32B",
				Quant:       "Q4_K_M",
				Runtime:     models.RuntimeLlamaCPP,
				Description: "large qwen model",
				MemoryBytes: 36 * gb,
				DiskBytes:   22 * gb,
			},
			Status:       "installed",
			CapableNodes: 2,
		},
	}

	filtered := filterAndSortModelSummaries(summaries, ModelCatalogFilters{
		Query:  "qwen",
		Family: "qwen",
		Sort:   "ram-desc",
	})
	if len(filtered) != 2 {
		t.Fatalf("expected 2 qwen models, got %d", len(filtered))
	}
	if filtered[0].Model.ID != "qwen-large" || filtered[1].Model.ID != "qwen-small" {
		t.Fatalf("expected qwen models sorted by RAM desc, got %s then %s", filtered[0].Model.ID, filtered[1].Model.ID)
	}

	capable := filterAndSortModelSummaries(summaries, ModelCatalogFilters{CapableOnly: true})
	if len(capable) != 2 {
		t.Fatalf("expected 2 capable models, got %d", len(capable))
	}
	for _, summary := range capable {
		if summary.CapableNodes == 0 {
			t.Fatalf("expected capable-only result, got %#v", summary)
		}
	}
}

func TestModelPlacementPlanSingleWorker(t *testing.T) {
	summary := ModelSummary{
		Model: models.Model{
			ID:          "qwen-small",
			MemoryBytes: 2 * gb,
			DiskBytes:   1 * gb,
		},
		Capabilities: []ModelCapability{{
			NodeID:              "node-a",
			Name:                "worker-a",
			Capable:             true,
			AllowedMemoryBytes:  8 * gb,
			AllowedStorageBytes: 16 * gb,
			FreeStorageBytes:    16 * gb,
			JobSlots:            1,
		}},
		CapableNodes: 1,
	}

	plan := modelPlacementPlan(summary)
	if plan.Mode != "single_worker" || !plan.Feasible || !plan.RunnableNow {
		t.Fatalf("expected runnable single-worker plan, got %#v", plan)
	}
	if len(plan.SingleNodeCandidates) != 1 || plan.SingleNodeCandidates[0].NodeID != "node-a" {
		t.Fatalf("expected single node candidate, got %#v", plan.SingleNodeCandidates)
	}
}

func TestModelPlacementPlanShardedEstimate(t *testing.T) {
	summary := ModelSummary{
		Model: models.Model{
			ID:          "large-model",
			MemoryBytes: 24 * gb,
			DiskBytes:   12 * gb,
		},
		Capabilities: []ModelCapability{
			{
				NodeID:              "node-a",
				Name:                "worker-a",
				AllowedMemoryBytes:  12 * gb,
				AllowedStorageBytes: 8 * gb,
				FreeStorageBytes:    8 * gb,
				JobSlots:            1,
				Reasons:             []string{"RAM short by 12.0 GB", "disk short by 4.0 GB"},
			},
			{
				NodeID:              "node-b",
				Name:                "worker-b",
				AllowedMemoryBytes:  12 * gb,
				AllowedStorageBytes: 8 * gb,
				FreeStorageBytes:    8 * gb,
				JobSlots:            1,
				Reasons:             []string{"RAM short by 12.0 GB", "disk short by 4.0 GB"},
			},
		},
	}

	plan := modelPlacementPlan(summary)
	if plan.Mode != "sharded_estimate" || !plan.Feasible || plan.RunnableNow {
		t.Fatalf("expected feasible sharded estimate that is not runnable now, got %#v", plan)
	}
	if len(plan.Shards) != 2 {
		t.Fatalf("expected two shards, got %#v", plan.Shards)
	}
	if len(plan.Warnings) == 0 {
		t.Fatalf("expected sharding warning")
	}
}

func TestModelPlacementPlanBlocked(t *testing.T) {
	summary := ModelSummary{
		Model: models.Model{
			ID:          "too-large",
			MemoryBytes: 64 * gb,
			DiskBytes:   32 * gb,
		},
		Capabilities: []ModelCapability{{
			NodeID:              "node-a",
			Name:                "worker-a",
			AllowedMemoryBytes:  8 * gb,
			AllowedStorageBytes: 8 * gb,
			FreeStorageBytes:    8 * gb,
			JobSlots:            1,
			Reasons:             []string{"RAM short by 56.0 GB", "disk short by 24.0 GB"},
		}},
	}

	plan := modelPlacementPlan(summary)
	if plan.Mode != "blocked" || plan.Feasible || plan.RunnableNow {
		t.Fatalf("expected blocked plan, got %#v", plan)
	}
	if len(plan.Blockers) == 0 {
		t.Fatalf("expected blockers, got %#v", plan)
	}
}
