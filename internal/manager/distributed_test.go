package manager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/runtimes"
	"github.com/cmesh/cmesh/internal/transport"
)

func TestDistributedModelPlanBuildsPipelineStages(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	installedAt := time.Now().UTC()
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 10, CoresAllowed: 6},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 12 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
				Capabilities: []string{
					runtimes.CapabilityPipelineStagePrepare,
					runtimes.CapabilityPipelineDecode,
					runtimes.ActivationStreamV1,
					runtimes.CapabilityLogicalStageRuntime,
				},
			}},
			Models: []cluster.ModelResource{{
				ID:          "qwen2.5-14b-instruct-q4-k-m",
				Name:        "Qwen2.5 14B Instruct",
				Runtime:     string(models.RuntimeLlamaCPP),
				Bytes:       5 * gb,
				Ready:       true,
				InstalledAt: installedAt,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlan(model, state.Nodes())
	if !plan.Feasible {
		t.Fatalf("expected feasible distributed plan, got %#v", plan)
	}
	if plan.ExecutableNow {
		t.Fatalf("distributed execution should remain disabled until tensor runtime adapter exists: %#v", plan)
	}
	if len(plan.Stages) != 2 {
		t.Fatalf("expected two stages, got %#v", plan.Stages)
	}
	if plan.Stages[0].LayerStart != 0 || plan.Stages[1].LayerEnd != plan.TotalLayers-1 {
		t.Fatalf("expected contiguous layer ranges, got %#v", plan.Stages)
	}
	if !plan.Stages[0].RuntimeReady || !plan.Stages[0].Installed {
		t.Fatalf("expected stage readiness metadata, got %#v", plan.Stages[0])
	}
	if !plan.Stages[0].StageRuntimeReady || plan.Stages[0].StageRuntime != "logical-stage" {
		t.Fatalf("expected logical stage runtime readiness, got %#v", plan.Stages[0])
	}
	if plan.StageRuntimeDiagnostics.CandidateWorkers != 2 || plan.StageRuntimeDiagnostics.StageReadyWorkers != 2 || plan.StageRuntimeDiagnostics.LogicalStageWorkers != 2 {
		t.Fatalf("expected stage runtime diagnostics, got %#v", plan.StageRuntimeDiagnostics)
	}
	if plan.EstimatedLatency.PerOutputTokenMS <= 0 || plan.Network.InterStageHops != 1 {
		t.Fatalf("expected latency and network estimates, got %#v", plan)
	}
	if !strings.Contains(strings.Join(plan.Blockers, " "), "resident stage daemon endpoint") {
		t.Fatalf("expected resident daemon blocker, got %#v", plan.Blockers)
	}
}

func TestDistributedModelPlanExecutableWithResidentStageDaemonsAndInstalledShards(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for index, name := range []string{"worker-a", "worker-b", "worker-c"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 10 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 12 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/opt/cmesh/llama-cli",
				Capabilities: []string{
					runtimes.CapabilityPipelineStagePrepare,
					runtimes.CapabilityPipelinePrefill,
					runtimes.CapabilityPipelineDecode,
					runtimes.ActivationStreamV1,
					runtimes.CapabilityLogicalStageRuntime,
				},
				StageRuntimes: []cluster.StageRuntimeResource{{
					Name:     "cmesh-stage-daemon",
					Ready:    true,
					Endpoint: fmt.Sprintf("http://10.0.10.%d:19781", index+10),
					Protocol: runtimes.StageSessionV1,
				}},
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-14b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Path:    fmt.Sprintf("/var/lib/cmesh/stage-shards/stage-%d.gguf", index),
				Layers:  48,
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 48)
	if !plan.Feasible || !plan.ExecutableNow {
		t.Fatalf("expected executable resident stage plan, got %#v", plan)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("expected no blockers, got %#v", plan.Blockers)
	}
	if plan.StageRuntimeDiagnostics.ResidentStageWorkers != 3 || plan.StageRuntimeDiagnostics.StageReadyWorkers != 3 {
		t.Fatalf("expected resident stage diagnostics, got %#v", plan.StageRuntimeDiagnostics)
	}
	if strings.Contains(strings.Join(plan.Blockers, " "), "distributed tensor runtime adapter") {
		t.Fatalf("stale runtime adapter blocker must not be reported: %#v", plan.Blockers)
	}
	for _, stage := range plan.Stages {
		if stage.StageDaemonURL == "" || !stage.Installed || stage.MemoryBytes > stage.AllowedMemoryBytes {
			t.Fatalf("expected ready installed resident stage, got %#v", stage)
		}
	}
}

func TestDistributedModelPlanCarriesStageDaemonEndpoint(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for index, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 12 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 80 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:         string(models.RuntimeLlamaCPP),
				Ready:        true,
				Capabilities: runtimes.LogicalStageCapabilities(),
				StageRuntimes: []cluster.StageRuntimeResource{{
					Name:     "cmesh-stage-daemon",
					Ready:    true,
					Endpoint: fmt.Sprintf("http://10.0.0.%d:19781", index+10),
					Protocol: runtimes.StageSessionV1,
				}},
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-0.5b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 28)
	if len(plan.Stages) != 2 {
		t.Fatalf("expected two stages, got %#v", plan.Stages)
	}
	for index, stage := range plan.Stages {
		expected := fmt.Sprintf("http://10.0.0.%d:19781", index+10)
		if stage.StageDaemonURL != expected {
			t.Fatalf("expected stage %d daemon endpoint %q, got %#v", index, expected, stage)
		}
	}
	input := models.DistributedGenerateInput{
		ModelID:     model.ID,
		Prompt:      "hello",
		Stages:      distributedStageInputs(plan.Stages),
		Shards:      cdipShardManifest(model, plan).Shards,
		MaxTokens:   3,
		Temperature: "0.1",
	}
	requests, err := distributedStageJobRequests(jobs.Job{ID: "job-parent"}, input)
	if err != nil {
		t.Fatal(err)
	}
	for index, req := range requests {
		var stageInput models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(req.Input), &stageInput); err != nil {
			t.Fatal(err)
		}
		expected := fmt.Sprintf("http://10.0.0.%d:19781", index+10)
		if stageInput.StageDaemonURL != expected {
			t.Fatalf("expected stage job %d daemon endpoint %q, got %#v", index, expected, stageInput)
		}
	}
}

func TestDistributedStageJobRequestsUsePerStageModelPaths(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b", "worker-c"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 10 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 12 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:         string(models.RuntimeLlamaCPP),
				Ready:        true,
				Capabilities: runtimes.LogicalStageCapabilities(),
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-14b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 48)
	if got, want := len(plan.Stages), 3; got != want {
		t.Fatalf("expected %d stages, got %#v", want, plan.Stages)
	}
	paths := []string{"/tmp/stage-0.gguf", "/tmp/stage-1.gguf", "/tmp/stage-2.gguf"}
	requests, err := distributedStageJobRequests(jobs.Job{ID: "job-parent"}, models.DistributedGenerateInput{
		ModelID:         model.ID,
		Prompt:          "hello",
		Stages:          distributedStageInputs(plan.Stages),
		Shards:          cdipShardManifest(model, plan).Shards,
		StageModelPaths: paths,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, req := range requests {
		var stageInput models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(req.Input), &stageInput); err != nil {
			t.Fatal(err)
		}
		if stageInput.ModelPath != paths[index] {
			t.Fatalf("expected stage %d model path %q, got %#v", index, paths[index], stageInput)
		}
	}
}

func TestPinDistributedPlanStageNodesPreservesPhysicalShardOrder(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b", "worker-c"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 10 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 50 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:         string(models.RuntimeLlamaCPP),
				Ready:        true,
				Capabilities: runtimes.LogicalStageCapabilities(),
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-14b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 24)
	if got, want := len(plan.Stages), 3; got != want {
		t.Fatalf("expected %d stages, got %#v", want, plan.Stages)
	}
	desired := []string{plan.Stages[2].NodeID, plan.Stages[0].NodeID, plan.Stages[1].NodeID}
	pinned, err := pinDistributedPlanStageNodes(plan, desired)
	if err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range desired {
		if pinned.Stages[index].NodeID != nodeID {
			t.Fatalf("expected stage %d on node %q, got %#v", index, nodeID, pinned.Stages[index])
		}
		if pinned.Stages[index].Index != index {
			t.Fatalf("expected pinned stage index %d, got %#v", index, pinned.Stages[index])
		}
		expectedStart := index * 8
		expectedEnd := expectedStart + 7
		if pinned.Stages[index].LayerStart != expectedStart || pinned.Stages[index].LayerEnd != expectedEnd {
			t.Fatalf("expected stage %d layers %d-%d, got %#v", index, expectedStart, expectedEnd, pinned.Stages[index])
		}
	}
}

func TestDistributedStageJobRequestsRejectStageModelPathCountMismatch(t *testing.T) {
	_, err := distributedStageJobRequests(jobs.Job{ID: "job-parent"}, models.DistributedGenerateInput{
		ModelID: "qwen2.5-0.5b-instruct-q4-k-m",
		Stages: []models.DistributedStageInput{{
			Index:      0,
			NodeID:     "node-a",
			LayerStart: 0,
			LayerEnd:   3,
		}, {
			Index:      1,
			NodeID:     "node-b",
			LayerStart: 4,
			LayerEnd:   7,
		}},
		Shards: []cdip.ModelShard{{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 3},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		}, {
			Stage:           cdip.Stage{Index: 1, NodeID: "node-b", LayerStart: 4, LayerEnd: 7},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		}},
		StageModelPaths: []string{"/tmp/stage-0.gguf"},
	})
	if err == nil || !strings.Contains(err.Error(), "stage_model_paths must match stage count") {
		t.Fatalf("expected stage_model_paths count error, got %v", err)
	}
}

func TestDistributedModelPlanFitsSixteenGBModelAcrossThreeSmallWorkers(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b", "worker-c"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 10 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 12 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
				Capabilities: []string{
					runtimes.CapabilityPipelineStagePrepare,
					runtimes.CapabilityPipelineDecode,
					runtimes.ActivationStreamV1,
					runtimes.CapabilityLogicalStageRuntime,
				},
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-14b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 48)
	if !plan.Feasible {
		t.Fatalf("expected 16GB model to fit across three 8GB workers, got %#v", plan)
	}
	if got, want := len(plan.Stages), 3; got != want {
		t.Fatalf("expected %d stages, got %#v", want, plan.Stages)
	}
	if plan.AggregateMemoryBytes != 24*gb {
		t.Fatalf("expected raw aggregate memory to be 24GB, got %d", plan.AggregateMemoryBytes)
	}
	if plan.AggregateStageMemoryBytes != 24*gb-3*distributedStageOverheadBytes {
		t.Fatalf("expected overhead-aware stage memory, got %d", plan.AggregateStageMemoryBytes)
	}
	for _, stage := range plan.Stages {
		if stage.Layers != 16 || stage.ModelMemoryBytes == 0 || stage.OverheadMemoryBytes != distributedStageOverheadBytes {
			t.Fatalf("expected balanced overhead-aware stage, got %#v", stage)
		}
		if stage.MemoryBytes > stage.AllowedMemoryBytes {
			t.Fatalf("stage exceeds allowed memory: %#v", stage)
		}
	}
}

func TestDistributedModelPlanReportsMemoryWeightedPlacement(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, worker := range []struct {
		name     string
		memoryGB uint64
		diskGB   uint64
	}{
		{name: "worker-large", memoryGB: 9, diskGB: 24},
		{name: "worker-medium", memoryGB: 7, diskGB: 24},
		{name: "worker-small", memoryGB: 7, diskGB: 24},
	} {
		joinWorkerWithResourcesForTest(t, srv, worker.name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: worker.memoryGB * gb, AllowedBytes: worker.memoryGB * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: worker.diskGB * gb, FreeBytes: worker.diskGB * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:         string(models.RuntimeLlamaCPP),
				Ready:        true,
				Version:      "test",
				Capabilities: runtimes.LogicalStageCapabilities(),
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-14b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Layers:  48,
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 48)
	if !plan.Feasible {
		t.Fatalf("expected uneven workers to produce feasible plan, got %#v", plan)
	}
	if plan.Placement.Strategy != "memory_disk_weighted_layers" || plan.Placement.TotalLayers != 48 {
		t.Fatalf("unexpected placement metadata: %#v", plan.Placement)
	}
	if got, want := len(plan.Placement.Candidates), 3; got != want {
		t.Fatalf("expected %d placement candidates, got %#v", want, plan.Placement.Candidates)
	}
	assigned := 0
	for _, candidate := range plan.Placement.Candidates {
		if !candidate.Selected {
			t.Fatalf("expected all three workers selected, got %#v", candidate)
		}
		if candidate.AssignedLayers < 1 || candidate.AssignedLayers > candidate.LayerCapacity {
			t.Fatalf("candidate assignment exceeds capacity: %#v", candidate)
		}
		if candidate.AssignedMemoryBytes == 0 || candidate.AssignedMemoryBytes > candidate.AllowedMemoryBytes {
			t.Fatalf("candidate memory assignment is not bounded: %#v", candidate)
		}
		if candidate.AssignedDiskBytes == 0 || candidate.AssignedDiskBytes > candidate.EffectiveStorageBytes {
			t.Fatalf("candidate disk assignment is not bounded: %#v", candidate)
		}
		assigned += candidate.AssignedLayers
	}
	if assigned != plan.TotalLayers {
		t.Fatalf("expected placement to assign all layers, assigned %d of %d", assigned, plan.TotalLayers)
	}
	if !(plan.Stages[0].Layers > plan.Stages[1].Layers && plan.Stages[1].Layers >= plan.Stages[2].Layers) {
		t.Fatalf("expected larger worker to receive more layers, got %#v", plan.Stages)
	}
}

func TestDistributedModelPlanRejectsSixteenGBModelAcrossTwoEightGBWorkers(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 10 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 12 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
				Capabilities: []string{
					runtimes.CapabilityPipelineStagePrepare,
					runtimes.CapabilityPipelineDecode,
					runtimes.ActivationStreamV1,
					runtimes.CapabilityLogicalStageRuntime,
				},
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 48)
	if plan.Feasible {
		t.Fatalf("expected two 8GB workers to be blocked after stage overhead, got %#v", plan)
	}
	if plan.AggregateMemoryBytes != 16*gb {
		t.Fatalf("expected raw aggregate memory to be 16GB, got %d", plan.AggregateMemoryBytes)
	}
	if plan.AggregateStageMemoryBytes >= model.MemoryBytes {
		t.Fatalf("expected overhead-aware stage memory to be below model requirement, got %#v", plan)
	}
	if !strings.Contains(strings.Join(plan.Blockers, " "), "aggregate stage RAM short") {
		t.Fatalf("expected actionable stage RAM blocker, got %#v", plan.Blockers)
	}
}

func TestDistributedModelPlanRejectsPlacementWhenMinimumLayerExceedsWorkerBudget(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, worker := range []struct {
		name     string
		memoryGB uint64
		diskGB   uint64
	}{
		{name: "tiny-a", memoryGB: 2, diskGB: 12},
		{name: "tiny-b", memoryGB: 2, diskGB: 12},
		{name: "large-c", memoryGB: 24, diskGB: 64},
	} {
		joinWorkerWithResourcesForTest(t, srv, worker.name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: worker.memoryGB * gb, AllowedBytes: worker.memoryGB * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: worker.diskGB * gb, FreeBytes: worker.diskGB * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:         string(models.RuntimeLlamaCPP),
				Ready:        true,
				Version:      "test",
				Capabilities: runtimes.LogicalStageCapabilities(),
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 10)
	if plan.Feasible {
		t.Fatalf("expected placement to be rejected when a minimum layer exceeds tiny worker RAM, got %#v", plan)
	}
	if plan.AggregateStageMemoryBytes < model.MemoryBytes {
		t.Fatalf("test setup expected aggregate stage RAM to be sufficient, got %#v", plan)
	}
	if !strings.Contains(strings.Join(plan.Blockers, " "), "memory-aware layer placement") {
		t.Fatalf("expected memory-aware placement blocker, got %#v", plan.Blockers)
	}
}

func TestDistributedModelPlanHonorsTotalLayerOverride(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 10, CoresAllowed: 6},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 12 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 80 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:         string(models.RuntimeLlamaCPP),
				Ready:        true,
				Capabilities: runtimes.LogicalStageCapabilities(),
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-0.5b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlanWithTotalLayers(model, state.Nodes(), 28)
	if !plan.Feasible || plan.TotalLayers != 28 || len(plan.Stages) != 2 {
		t.Fatalf("expected feasible override plan, got %#v", plan)
	}
	if plan.Stages[0].LayerStart != 0 || plan.Stages[1].LayerEnd != 27 {
		t.Fatalf("expected override layer range 0-27, got %#v", plan.Stages)
	}
}

func TestDistributedModelPlanInfersLayersFromWorkerInventory(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 10, CoresAllowed: 6},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 12 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 80 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:         string(models.RuntimeLlamaCPP),
				Ready:        true,
				Capabilities: runtimes.LogicalStageCapabilities(),
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-0.5b-instruct-q4-k-m",
				Runtime: string(models.RuntimeLlamaCPP),
				Layers:  28,
				Ready:   true,
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlan(model, state.Nodes())
	if !plan.Feasible || plan.TotalLayers != 28 {
		t.Fatalf("expected worker-inferred total layers, got %#v", plan)
	}
	if plan.Stages[0].LayerStart != 0 || plan.Stages[1].LayerEnd != 27 {
		t.Fatalf("expected inferred layer range 0-27, got %#v", plan.Stages)
	}
}

func TestDistributedModelPlanReportsMissingStageRuntimeCapability(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 10, CoresAllowed: 6},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 12 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 80 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
			}},
		})
	}
	model, err := models.MustFind("qwen2.5-14b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlan(model, state.Nodes())
	if len(plan.Stages) != 2 {
		t.Fatalf("expected two stages, got %#v", plan.Stages)
	}
	if plan.Stages[0].StageRuntimeReady {
		t.Fatalf("expected stage runtime to be blocked without capability, got %#v", plan.Stages[0])
	}
	if !strings.Contains(plan.Stages[0].StageRuntimeReason, "capability not reported") {
		t.Fatalf("expected capability reason, got %#v", plan.Stages[0])
	}
	if !strings.Contains(strings.Join(plan.Warnings, " "), "distributed stage runtime capability") {
		t.Fatalf("expected capability warning, got %#v", plan.Warnings)
	}
	if plan.StageRuntimeDiagnostics.CandidateWorkers != 2 || plan.StageRuntimeDiagnostics.RuntimeReadyWorkers != 2 || plan.StageRuntimeDiagnostics.StageReadyWorkers != 0 {
		t.Fatalf("expected blocked stage diagnostics, got %#v", plan.StageRuntimeDiagnostics)
	}
	if len(plan.StageRuntimeDiagnostics.MissingStageCapability) != 2 {
		t.Fatalf("expected missing stage capability details, got %#v", plan.StageRuntimeDiagnostics)
	}
}

func TestDistributedModelPlanReportsResourceBlockers(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "small-worker", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 4, CoresAllowed: 2},
		Memory:  cluster.MemoryResources{TotalBytes: 4 * gb, AllowedBytes: 3 * gb},
		Storage: cluster.StorageResources{TotalBytes: 32 * gb, AllowedBytes: 2 * gb, FreeBytes: 2 * gb},
	})
	model, err := models.MustFind("gemma-3-12b-it-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}

	plan := distributedModelPlan(model, state.Nodes())
	if plan.Feasible {
		t.Fatalf("expected blocked distributed plan, got %#v", plan)
	}
	body := strings.Join(plan.Blockers, " ")
	for _, expected := range []string{"at least 2 online workers", "aggregate stage RAM short", "aggregate disk short"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("expected blocker %q in %#v", expected, plan.Blockers)
		}
	}
}

func TestModelDistributedPlanEndpoint(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
	})
	joinWorkerWithResourcesForTest(t, srv, "worker-b", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
		Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models/qwen2.5-7b-instruct-q4-k-m/distributed-plan", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Plan         DistributedModelPlan `json:"plan"`
		CDIPProposal struct {
			Protocol string `json:"protocol"`
			Version  string `json:"version"`
			Type     string `json:"type"`
			ModelID  string `json:"model_id"`
			Stages   []struct {
				Index      int    `json:"index"`
				NodeID     string `json:"node_id"`
				LayerStart int    `json:"layer_start"`
				LayerEnd   int    `json:"layer_end"`
			} `json:"stages"`
		} `json:"cdip_proposal"`
		CDIPShardManifest struct {
			Protocol        string `json:"protocol"`
			Version         string `json:"version"`
			Type            string `json:"type"`
			Mode            string `json:"mode"`
			TotalLayers     int    `json:"total_layers"`
			Materialization string `json:"materialization"`
			Model           struct {
				ModelID string `json:"model_id"`
				Runtime string `json:"runtime"`
			} `json:"model"`
			Shards []struct {
				Runtime         string `json:"runtime"`
				SourceArtifact  string `json:"source_artifact"`
				TargetArtifact  string `json:"target_artifact"`
				Materialization string `json:"materialization"`
				Artifact        struct {
					Protocol              string `json:"protocol"`
					Status                string `json:"status"`
					LayerStart            int    `json:"layer_start"`
					LayerEnd              int    `json:"layer_end"`
					ExpectedBytes         uint64 `json:"expected_bytes"`
					PhysicalArtifactReady bool   `json:"physical_artifact_ready"`
				} `json:"artifact"`
				Stage struct {
					Index      int    `json:"index"`
					NodeID     string `json:"node_id"`
					LayerStart int    `json:"layer_start"`
					LayerEnd   int    `json:"layer_end"`
				} `json:"stage"`
			} `json:"shards"`
		} `json:"cdip_shard_manifest"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Plan.ModelID != "qwen2.5-7b-instruct-q4-k-m" || payload.Plan.Mode != "pipeline_layers" {
		t.Fatalf("unexpected distributed plan payload: %#v", payload.Plan)
	}
	if len(payload.Plan.Stages) != 2 {
		t.Fatalf("expected two planned stages, got %#v", payload.Plan.Stages)
	}
	if payload.Plan.StageRuntimeDiagnostics.CandidateWorkers != 2 {
		t.Fatalf("expected distributed runtime diagnostics in payload, got %#v", payload.Plan.StageRuntimeDiagnostics)
	}
	if payload.CDIPProposal.Protocol != "cdip" || payload.CDIPProposal.Version != "0.1" || payload.CDIPProposal.Type != "plan.proposal" {
		t.Fatalf("expected cdip plan proposal, got %#v", payload.CDIPProposal)
	}
	if payload.CDIPProposal.ModelID != payload.Plan.ModelID || len(payload.CDIPProposal.Stages) != len(payload.Plan.Stages) {
		t.Fatalf("expected cdip proposal to mirror plan, got %#v", payload.CDIPProposal)
	}
	if payload.CDIPShardManifest.Protocol != "cdip" || payload.CDIPShardManifest.Version != "0.1" || payload.CDIPShardManifest.Type != "shard.manifest" {
		t.Fatalf("expected cdip shard manifest, got %#v", payload.CDIPShardManifest)
	}
	if payload.CDIPShardManifest.Model.ModelID != payload.Plan.ModelID || payload.CDIPShardManifest.Model.Runtime != payload.Plan.Runtime {
		t.Fatalf("expected shard manifest model to mirror plan, got %#v", payload.CDIPShardManifest.Model)
	}
	if payload.CDIPShardManifest.Mode != payload.Plan.Mode || payload.CDIPShardManifest.TotalLayers != payload.Plan.TotalLayers {
		t.Fatalf("expected shard manifest placement metadata to mirror plan, got %#v", payload.CDIPShardManifest)
	}
	if payload.CDIPShardManifest.Materialization != "logical_layers" || len(payload.CDIPShardManifest.Shards) != len(payload.Plan.Stages) {
		t.Fatalf("expected logical shards for every plan stage, got %#v", payload.CDIPShardManifest)
	}
	for i, shard := range payload.CDIPShardManifest.Shards {
		stage := payload.Plan.Stages[i]
		if shard.Stage.Index != stage.Index || shard.Stage.NodeID != stage.NodeID || shard.Stage.LayerStart != stage.LayerStart || shard.Stage.LayerEnd != stage.LayerEnd {
			t.Fatalf("expected shard %d to mirror plan stage, got %#v vs %#v", i, shard.Stage, stage)
		}
		if shard.Runtime == "" || shard.SourceArtifact == "" || shard.TargetArtifact == "" {
			t.Fatalf("expected shard %d runtime and artifacts, got %#v", i, shard)
		}
		if shard.Artifact.Protocol != "cdip.shard-artifact-v1" || shard.Artifact.Status != "planned" || shard.Artifact.PhysicalArtifactReady {
			t.Fatalf("expected planned shard artifact metadata for shard %d, got %#v", i, shard.Artifact)
		}
		if shard.Artifact.LayerStart != stage.LayerStart || shard.Artifact.LayerEnd != stage.LayerEnd || shard.Artifact.ExpectedBytes != stage.DiskBytes {
			t.Fatalf("expected shard %d artifact to mirror stage disk/range, got %#v vs %#v", i, shard.Artifact, stage)
		}
	}
}

func TestDistributedGenerateEndpointCreatesPlannedJobGraph(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-7b-instruct-q4-k-m",
				Name:    "Qwen2.5 7B Instruct",
				Runtime: string(models.RuntimeLlamaCPP),
				Bytes:   5 * gb,
				Ready:   true,
			}},
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-7b-instruct-q4-k-m/distributed-generate", strings.NewReader(`{"prompt":"hello from distributed cluster"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var payload distributedGenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Job.Type != models.JobGenerateDistributed || payload.Job.Status != jobs.StatusQueued || payload.Job.AssignedTo != "" {
		t.Fatalf("expected queued unassigned coordinator job, got %#v", payload.Job)
	}
	if !payload.Plan.Feasible || payload.Plan.ExecutableNow || payload.ExecutableNow {
		t.Fatalf("expected feasible but non-executable plan, got %#v", payload)
	}
	if len(payload.StageJobs) != 2 {
		t.Fatalf("expected two planned stage jobs, got %#v", payload.StageJobs)
	}
	for _, stageJob := range payload.StageJobs {
		if stageJob.Type != models.JobGenerateStage || stageJob.Status != jobs.StatusQueued || stageJob.AssignedTo == "" {
			t.Fatalf("expected queued assigned stage job, got %#v", stageJob)
		}
		if stageJob.CDIPState != cdip.StagePlanned || stageJob.CDIPParentJobID != payload.Job.ID {
			t.Fatalf("expected planned CDIP stage metadata, got %#v", stageJob)
		}
		if stageJob.LastFailure != "waiting for coordinator" {
			t.Fatalf("expected stage job to wait for coordinator, got %#v", stageJob)
		}
		var stageInput models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &stageInput); err != nil {
			t.Fatal(err)
		}
		if stageInput.Shard.Stage.Index != stageInput.Stage.Index || stageInput.Shard.Stage.NodeID != stageInput.Stage.NodeID {
			t.Fatalf("expected stage job input to include matching shard contract, got %#v", stageInput)
		}
		if stageInput.Shard.Runtime != string(models.RuntimeLlamaCPP) || stageInput.Shard.SourceArtifact == "" || stageInput.Shard.TargetArtifact == "" {
			t.Fatalf("expected stage job input to include runtime and shard artifacts, got %#v", stageInput.Shard)
		}
	}
	if len(state.Jobs()) != 3 {
		t.Fatalf("expected parent plus two stage jobs, got %#v", state.Jobs())
	}
}

func TestCDIPDistributedGenerateEndToEndControlPlane(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-7b-instruct-q4-k-m",
				Name:    "Qwen2.5 7B Instruct",
				Runtime: string(models.RuntimeLlamaCPP),
				Bytes:   5 * gb,
				Ready:   true,
			}},
		})
	}

	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-7b-instruct-q4-k-m/distributed-generate", strings.NewReader(`{"prompt":"hello distributed cluster"}`)))
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("expected distributed generate 202, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created distributedGenerateResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.StageJobs) != 2 {
		t.Fatalf("expected two stage jobs, got %#v", created.StageJobs)
	}

	prepareRec := httptest.NewRecorder()
	srv.ServeHTTP(prepareRec, httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/prepare", nil))
	if prepareRec.Code != http.StatusAccepted {
		t.Fatalf("expected prepare 202, got %d: %s", prepareRec.Code, prepareRec.Body.String())
	}
	var prepared CDIPPrepareResult
	if err := json.Unmarshal(prepareRec.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if len(prepared.Messages) != len(created.StageJobs) {
		t.Fatalf("expected prepare messages for each stage, got %#v", prepared)
	}

	for _, stageJob := range prepared.StageJobs {
		if stageJob.Status != jobs.StatusScheduled || stageJob.LastFailure != "" {
			t.Fatalf("expected prepared stage to be scheduled for worker polling, got %#v", stageJob)
		}
		nextRec := httptest.NewRecorder()
		srv.ServeHTTP(nextRec, withWorkerAuthForTest(t, srv, httptest.NewRequest(http.MethodGet, "/v1/workers/"+stageJob.AssignedTo+"/jobs/next", nil), stageJob.AssignedTo))
		if nextRec.Code != http.StatusOK {
			t.Fatalf("expected worker next job 200, got %d: %s", nextRec.Code, nextRec.Body.String())
		}
		var nextPayload struct {
			Job *jobs.Job `json:"job"`
		}
		if err := json.Unmarshal(nextRec.Body.Bytes(), &nextPayload); err != nil {
			t.Fatal(err)
		}
		if nextPayload.Job == nil || nextPayload.Job.ID != stageJob.ID || nextPayload.Job.Status != jobs.StatusRunning {
			t.Fatalf("expected worker to receive running stage job, got %#v", nextPayload.Job)
		}
		readyBody := strings.NewReader(`{"node_id":"` + nextPayload.Job.AssignedTo + `","result":"{\"kind\":\"cdip.stage_ready\"}"}`)
		readyRec := httptest.NewRecorder()
		srv.ServeHTTP(readyRec, withWorkerAuthForTest(t, srv, httptest.NewRequest(http.MethodPost, "/v1/jobs/"+nextPayload.Job.ID+"/complete", readyBody), nextPayload.Job.AssignedTo))
		if readyRec.Code != http.StatusOK {
			t.Fatalf("expected stage ready 200, got %d: %s", readyRec.Code, readyRec.Body.String())
		}
		var readyJob jobs.Job
		if err := json.Unmarshal(readyRec.Body.Bytes(), &readyJob); err != nil {
			t.Fatal(err)
		}
		if readyJob.CDIPState != cdip.StageReady {
			t.Fatalf("expected stage ready, got %#v", readyJob)
		}
	}

	prefillRec := httptest.NewRecorder()
	srv.ServeHTTP(prefillRec, httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/prefill", nil))
	if prefillRec.Code != http.StatusAccepted {
		t.Fatalf("expected prefill 202, got %d: %s", prefillRec.Code, prefillRec.Body.String())
	}
	var prefilled CDIPCommandResult
	if err := json.Unmarshal(prefillRec.Body.Bytes(), &prefilled); err != nil {
		t.Fatal(err)
	}
	for _, stageJob := range prefilled.StageJobs {
		if stageJob.CDIPState != cdip.StagePrefill {
			t.Fatalf("expected prefill state, got %#v", stageJob)
		}
	}

	decodeRec := httptest.NewRecorder()
	srv.ServeHTTP(decodeRec, httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/decode", strings.NewReader(`{"step":1}`)))
	if decodeRec.Code != http.StatusAccepted {
		t.Fatalf("expected decode 202, got %d: %s", decodeRec.Code, decodeRec.Body.String())
	}
	var decoded CDIPCommandResult
	if err := json.Unmarshal(decodeRec.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.ActivationFrames) != len(decoded.StageJobs)-1 {
		t.Fatalf("expected activation frames between stages, got %#v", decoded)
	}
	for _, stageJob := range decoded.StageJobs {
		if stageJob.CDIPState != cdip.StageDecode {
			t.Fatalf("expected decode state, got %#v", stageJob)
		}
	}

	completeRec := httptest.NewRecorder()
	srv.ServeHTTP(completeRec, httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/complete", strings.NewReader(`{"output":"e2e distributed answer"}`)))
	if completeRec.Code != http.StatusAccepted {
		t.Fatalf("expected complete 202, got %d: %s", completeRec.Code, completeRec.Body.String())
	}
	var completed CDIPCommandResult
	if err := json.Unmarshal(completeRec.Body.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.ParentJob.Status != jobs.StatusSucceeded || !strings.Contains(completed.ParentJob.Result, "e2e distributed answer") {
		t.Fatalf("expected completed parent result, got %#v", completed.ParentJob)
	}
	for _, stageJob := range completed.StageJobs {
		if stageJob.CDIPState != cdip.StageCompleted || stageJob.Status != jobs.StatusSucceeded {
			t.Fatalf("expected completed stage, got %#v", stageJob)
		}
	}
}

func TestCDIPAdvanceEndpointDrivesCoordinatorBoundaries(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-7b-instruct-q4-k-m",
				Name:    "Qwen2.5 7B Instruct",
				Runtime: string(models.RuntimeLlamaCPP),
				Bytes:   5 * gb,
				Ready:   true,
			}},
		})
	}

	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-7b-instruct-q4-k-m/distributed-generate", strings.NewReader(`{"prompt":"hello distributed cluster"}`)))
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("expected distributed generate 202, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created distributedGenerateResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	advanceRec := httptest.NewRecorder()
	srv.ServeHTTP(advanceRec, httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/advance", nil))
	if advanceRec.Code != http.StatusAccepted {
		t.Fatalf("expected advance prepare 202, got %d: %s", advanceRec.Code, advanceRec.Body.String())
	}
	var advanced CDIPAdvanceResult
	if err := json.Unmarshal(advanceRec.Body.Bytes(), &advanced); err != nil {
		t.Fatal(err)
	}
	if advanced.Action != "prepare" || len(advanced.PrepareMessages) != len(created.StageJobs) {
		t.Fatalf("expected prepare advance, got %#v", advanced)
	}

	waitRec := httptest.NewRecorder()
	srv.ServeHTTP(waitRec, httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/advance", nil))
	if waitRec.Code != http.StatusAccepted {
		t.Fatalf("expected waiting advance 202, got %d: %s", waitRec.Code, waitRec.Body.String())
	}
	var waiting CDIPAdvanceResult
	if err := json.Unmarshal(waitRec.Body.Bytes(), &waiting); err != nil {
		t.Fatal(err)
	}
	if !waiting.Waiting || waiting.Action != "wait" {
		t.Fatalf("expected waiting advance, got %#v", waiting)
	}

	for _, stageJob := range advanced.StageJobs {
		nextRec := httptest.NewRecorder()
		srv.ServeHTTP(nextRec, withWorkerAuthForTest(t, srv, httptest.NewRequest(http.MethodGet, "/v1/workers/"+stageJob.AssignedTo+"/jobs/next", nil), stageJob.AssignedTo))
		if nextRec.Code != http.StatusOK {
			t.Fatalf("expected worker next job 200, got %d: %s", nextRec.Code, nextRec.Body.String())
		}
		var nextPayload struct {
			Job *jobs.Job `json:"job"`
		}
		if err := json.Unmarshal(nextRec.Body.Bytes(), &nextPayload); err != nil {
			t.Fatal(err)
		}
		if nextPayload.Job == nil {
			t.Fatal("expected worker stage job")
		}
		readyBody := strings.NewReader(`{"node_id":"` + nextPayload.Job.AssignedTo + `","result":"{\"kind\":\"cdip.stage_ready\"}"}`)
		readyRec := httptest.NewRecorder()
		srv.ServeHTTP(readyRec, withWorkerAuthForTest(t, srv, httptest.NewRequest(http.MethodPost, "/v1/jobs/"+nextPayload.Job.ID+"/complete", readyBody), nextPayload.Job.AssignedTo))
		if readyRec.Code != http.StatusOK {
			t.Fatalf("expected stage ready 200, got %d: %s", readyRec.Code, readyRec.Body.String())
		}
	}

	for _, expected := range []string{"prefill", "decode", "complete"} {
		body := strings.NewReader(`{"step":2,"output":"advanced distributed answer"}`)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/advance", body))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("expected advance %s 202, got %d: %s", expected, rec.Code, rec.Body.String())
		}
		var result CDIPAdvanceResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Action != expected {
			t.Fatalf("expected advance action %s, got %#v", expected, result)
		}
		if expected == "complete" && (result.ParentJob.Status != jobs.StatusSucceeded || !strings.Contains(result.ParentJob.Result, "advanced distributed answer")) {
			t.Fatalf("expected completed parent job, got %#v", result.ParentJob)
		}
	}
}

func TestBackgroundCDIPAdvanceDispatchesAndCompletes(t *testing.T) {
	state := NewState()
	srv := NewServerWithOptions(ServerOptions{BackgroundCDIPAdvance: true}, state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-7b-instruct-q4-k-m",
				Name:    "Qwen2.5 7B Instruct",
				Runtime: string(models.RuntimeLlamaCPP),
				Bytes:   5 * gb,
				Ready:   true,
			}},
		})
	}

	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-7b-instruct-q4-k-m/distributed-generate", strings.NewReader(`{"prompt":"hello distributed cluster"}`)))
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("expected distributed generate 202, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created distributedGenerateResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	srv.advanceActiveCDIPJobs()
	stageJobs := cdipStageJobsForParent(state.Jobs(), created.Job.ID)
	for _, stageJob := range stageJobs {
		if stageJob.Status != jobs.StatusScheduled || stageJob.CDIPState != cdip.StagePreparing {
			t.Fatalf("expected background prepare to schedule stage job, got %#v", stageJob)
		}
	}

	srv.advanceActiveCDIPJobs()
	waitingStages := cdipStageJobsForParent(state.Jobs(), created.Job.ID)
	for _, stageJob := range waitingStages {
		if stageJob.CDIPState != cdip.StagePreparing {
			t.Fatalf("background loop should wait for worker readiness, got %#v", stageJob)
		}
	}

	for _, stageJob := range waitingStages {
		nextRec := httptest.NewRecorder()
		srv.ServeHTTP(nextRec, withWorkerAuthForTest(t, srv, httptest.NewRequest(http.MethodGet, "/v1/workers/"+stageJob.AssignedTo+"/jobs/next", nil), stageJob.AssignedTo))
		if nextRec.Code != http.StatusOK {
			t.Fatalf("expected worker next job 200, got %d: %s", nextRec.Code, nextRec.Body.String())
		}
		var nextPayload struct {
			Job *jobs.Job `json:"job"`
		}
		if err := json.Unmarshal(nextRec.Body.Bytes(), &nextPayload); err != nil {
			t.Fatal(err)
		}
		if nextPayload.Job == nil {
			t.Fatal("expected worker stage job")
		}
		readyBody := strings.NewReader(`{"node_id":"` + nextPayload.Job.AssignedTo + `","result":"{\"kind\":\"cdip.stage_ready\"}"}`)
		readyRec := httptest.NewRecorder()
		srv.ServeHTTP(readyRec, withWorkerAuthForTest(t, srv, httptest.NewRequest(http.MethodPost, "/v1/jobs/"+nextPayload.Job.ID+"/complete", readyBody), nextPayload.Job.AssignedTo))
		if readyRec.Code != http.StatusOK {
			t.Fatalf("expected stage ready 200, got %d: %s", readyRec.Code, readyRec.Body.String())
		}
	}

	srv.advanceActiveCDIPJobs()
	srv.advanceActiveCDIPJobs()
	srv.advanceActiveCDIPJobs()
	parent, ok := state.Job(created.Job.ID)
	if !ok {
		t.Fatal("parent job not found")
	}
	if parent.Status != jobs.StatusSucceeded || !strings.Contains(parent.Result, "CDIP distributed inference completed") {
		t.Fatalf("expected background loop to complete parent, got %#v", parent)
	}
}

func TestCDIPStageLifecycleEndpoint(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	stageJob, err := state.CreateJob(jobs.CreateRequest{
		Type:            models.JobGenerateStage,
		Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
		RequestedBy:     "test",
		AssignedTo:      "node-a",
		NoAutoAssign:    true,
		CDIPState:       cdip.StagePlanned,
		CDIPParentJobID: "job-parent",
		CDIPStageIndex:  0,
		MaxAttempts:     1,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/stages/"+stageJob.ID+"/prepare", strings.NewReader(`{"detail":"loading shard"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected prepare status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, ok := state.Job(stageJob.ID)
	if !ok || updated.CDIPState != cdip.StagePreparing || !strings.Contains(updated.Result, "loading shard") {
		t.Fatalf("expected preparing stage, got %#v", updated)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/cdip/stages/"+stageJob.ID+"/ready", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected ready status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, _ = state.Job(stageJob.ID)
	if updated.CDIPState != cdip.StageReady {
		t.Fatalf("expected ready stage, got %#v", updated)
	}
}

func TestCDIPStageLifecycleRejectsInvalidTransition(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	stageJob, err := state.CreateJob(jobs.CreateRequest{
		Type:         models.JobGenerateStage,
		Input:        `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
		RequestedBy:  "test",
		AssignedTo:   "node-a",
		NoAutoAssign: true,
		CDIPState:    cdip.StagePlanned,
		MaxAttempts:  1,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/stages/"+stageJob.ID+"/decode", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected conflict for invalid transition, got %d: %s", rec.Code, rec.Body.String())
	}
	updated, _ := state.Job(stageJob.ID)
	if updated.CDIPState != cdip.StagePlanned {
		t.Fatalf("invalid transition changed state: %#v", updated)
	}
}

func TestCDIPMockCoordinatorCompletesPlannedGraph(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-7b-instruct-q4-k-m",
				Name:    "Qwen2.5 7B Instruct",
				Runtime: string(models.RuntimeLlamaCPP),
				Bytes:   5 * gb,
				Ready:   true,
			}},
		})
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-7b-instruct-q4-k-m/distributed-generate", strings.NewReader(`{"prompt":"hello from distributed cluster"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected graph creation status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var created distributedGenerateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/mock-run", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected mock-run status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPMockRunResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ParentJob.Status != jobs.StatusSucceeded || !strings.Contains(result.Output, "CDIP mock") {
		t.Fatalf("expected succeeded parent mock result, got %#v", result)
	}
	if len(result.StageJobs) != 2 {
		t.Fatalf("expected two stage jobs, got %#v", result.StageJobs)
	}
	for _, stage := range result.StageJobs {
		if stage.Status != jobs.StatusSucceeded || stage.CDIPState != cdip.StageCompleted {
			t.Fatalf("expected completed stage, got %#v", stage)
		}
	}
}

func TestCDIPPrepareEndpointBuildsStagePrepareMessages(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	for _, name := range []string{"worker-a", "worker-b"} {
		joinWorkerWithResourcesForTest(t, srv, name, cluster.ResourceSnapshot{
			CPU:     cluster.CPUResources{CoresTotal: 8, CoresAllowed: 4},
			Memory:  cluster.MemoryResources{TotalBytes: 16 * gb, AllowedBytes: 8 * gb},
			Storage: cluster.StorageResources{TotalBytes: 128 * gb, AllowedBytes: 8 * gb, FreeBytes: 64 * gb},
			Runtimes: []cluster.RuntimeResource{{
				Name:       string(models.RuntimeLlamaCPP),
				Ready:      true,
				Version:    "test",
				BinaryPath: "/tmp/llama-cli",
			}},
			Models: []cluster.ModelResource{{
				ID:      "qwen2.5-7b-instruct-q4-k-m",
				Name:    "Qwen2.5 7B Instruct",
				Runtime: string(models.RuntimeLlamaCPP),
				Bytes:   5 * gb,
				Ready:   true,
			}},
		})
	}

	createReq := httptest.NewRequest(http.MethodPost, "/v1/models/qwen2.5-7b-instruct-q4-k-m/distributed-generate", strings.NewReader(`{"prompt":"hello from distributed cluster"}`))
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created distributedGenerateResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	prepareReq := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+created.Job.ID+"/prepare", nil)
	prepareRec := httptest.NewRecorder()
	srv.ServeHTTP(prepareRec, prepareReq)
	if prepareRec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", prepareRec.Code, prepareRec.Body.String())
	}
	var prepared CDIPPrepareResult
	if err := json.Unmarshal(prepareRec.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.ParentJob.ID != created.Job.ID || len(prepared.Messages) != len(created.StageJobs) {
		t.Fatalf("unexpected prepare result: %#v", prepared)
	}
	for i, msg := range prepared.Messages {
		if err := msg.Validate(); err != nil {
			t.Fatal(err)
		}
		if msg.Type != cdip.MessageStagePrepare || msg.ParentJobID != created.Job.ID || msg.Stage.Index != i {
			t.Fatalf("unexpected stage.prepare %d: %#v", i, msg)
		}
		if prepared.StageJobs[i].CDIPState != cdip.StagePreparing {
			t.Fatalf("expected stage %d preparing, got %#v", i, prepared.StageJobs[i])
		}
		if prepared.StageJobs[i].Status != jobs.StatusScheduled || prepared.StageJobs[i].LastFailure != "" {
			t.Fatalf("expected stage %d scheduled for worker after prepare, got %#v", i, prepared.StageJobs[i])
		}
	}
}

func TestCDIPStageReadyCompletionUpdatesLifecycle(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	joinWorkerWithResourcesForTest(t, srv, "worker-a", cluster.ResourceSnapshot{
		CPU:     cluster.CPUResources{CoresTotal: 4, CoresAllowed: 4},
		Memory:  cluster.MemoryResources{AllowedBytes: 8 * 1024 * 1024 * 1024},
		Storage: cluster.StorageResources{AllowedBytes: 8 * 1024 * 1024 * 1024},
	})
	parentJob, err := state.CreateJob(jobs.CreateRequest{
		Type:         models.JobGenerateDistributed,
		Input:        `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
		RequestedBy:  "test",
		MaxAttempts:  1,
		NoAutoAssign: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageJob, err := state.CreateJob(jobs.CreateRequest{
		Type:            models.JobGenerateStage,
		Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
		RequestedBy:     "distributed-coordinator:job-parent",
		AssignedTo:      "node-a",
		CDIPState:       cdip.StagePreparing,
		CDIPParentJobID: "job-parent",
		CDIPStageIndex:  0,
		NoAutoAssign:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageJob.ID+"/complete", strings.NewReader(`{"node_id":"node-a","result":"{\"kind\":\"cdip.stage_ready\",\"artifact\":{\"protocol\":\"cdip.shard-artifact-v1\",\"status\":\"selected_tensor_manifest_ready\",\"uri\":\"file:///var/lib/cmesh/stage-work/stage-0/stage-0-materialization-plan.json\",\"checksum\":\"sha256:abc\",\"expected_bytes\":96,\"physical_artifact_ready\":false}}"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.CDIPState != cdip.StageReady {
		t.Fatalf("expected CDIP stage ready, got %#v", updated)
	}
	if updated.Status != jobs.StatusSucceeded || updated.FinishedAt.IsZero() {
		t.Fatalf("stage ready should complete the worker prepare attempt before prefill/decode dispatch: %#v", updated)
	}
	var stageState struct {
		Kind         string `json:"kind"`
		WorkerResult struct {
			Kind     string `json:"kind"`
			Artifact struct {
				URI      string `json:"uri"`
				Checksum string `json:"checksum"`
			} `json:"artifact"`
		} `json:"worker_result"`
	}
	if err := json.Unmarshal([]byte(updated.Result), &stageState); err != nil {
		t.Fatal(err)
	}
	if stageState.Kind != "cdip.stage.state" || stageState.WorkerResult.Kind != "cdip.stage_ready" || stageState.WorkerResult.Artifact.URI == "" || stageState.WorkerResult.Artifact.Checksum == "" {
		t.Fatalf("expected stage state to preserve worker prepare artifact, got %s", updated.Result)
	}
	parent, ok := state.Job(parentJob.ID)
	if !ok {
		t.Fatal("parent job missing")
	}
	if parent.Status != jobs.StatusQueued || parent.AssignedTo != "" {
		t.Fatalf("distributed parent job must remain coordinator-owned and unscheduled, got %#v", parent)
	}
}

func TestCDIPTerminalDecodeCompletionCompletesParentJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageJob, err := state.CreateJob(jobs.CreateRequest{
		Type:            models.JobGenerateStage,
		Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
		RequestedBy:     "distributed-coordinator:" + parent.ID,
		AssignedTo:      "node-c",
		CDIPState:       cdip.StageDecode,
		CDIPParentJobID: parent.ID,
		CDIPStageIndex:  2,
		NoAutoAssign:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalResult := `{
		"kind":"cdip.stage_terminal_decode",
		"parent_job_id":"` + parent.ID + `",
		"upstream_stage_job_id":"job-stage-1",
		"stage_job_id":"` + stageJob.ID + `",
		"stage_index":2,
		"next_token_id":42,
		"next_token_text":" hello",
		"tokens":[42,43,44],
		"output":" hello world"
	}`
	body, err := json.Marshal(jobs.CompleteRequest{
		NodeID: "node-c",
		Result: terminalResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageJob.ID+"/complete", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var completed jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.ID != parent.ID || completed.Status != jobs.StatusSucceeded {
		t.Fatalf("expected completed parent job, got %#v", completed)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(completed.Result), &result); err != nil {
		t.Fatal(err)
	}
	if result["kind"] != "cdip.distributed_terminal_result" || result["output"] != " hello world" || int(result["token_count"].(float64)) != 3 {
		t.Fatalf("unexpected parent result: %#v", result)
	}
	updatedStage, ok := state.Job(stageJob.ID)
	if !ok || updatedStage.CDIPState != cdip.StageDecode {
		t.Fatalf("expected terminal stage to remain decode, got %#v ok=%v", updatedStage, ok)
	}
}

func TestCDIPTerminalDecodePartialDoesNotCompleteParentJob(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageJob, err := state.CreateJob(jobs.CreateRequest{
		Type:            models.JobGenerateStage,
		Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
		RequestedBy:     "distributed-coordinator:" + parent.ID,
		AssignedTo:      "node-c",
		CDIPState:       cdip.StageDecode,
		CDIPParentJobID: parent.ID,
		CDIPStageIndex:  2,
		NoAutoAssign:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	partialResult := `{
		"kind":"cdip.stage_terminal_decode",
		"parent_job_id":"` + parent.ID + `",
		"upstream_stage_job_id":"job-stage-1",
		"stage_job_id":"` + stageJob.ID + `",
		"stage_index":2,
		"next_token_id":42,
		"next_token_text":" hello",
		"tokens":[42],
		"output":" hello",
		"final":false
	}`
	body, err := json.Marshal(jobs.CompleteRequest{NodeID: "node-c", Result: partialResult})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageJob.ID+"/complete", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var updated jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != stageJob.ID || updated.CDIPState != cdip.StageDecode {
		t.Fatalf("expected updated terminal stage, got %#v", updated)
	}
	stillParent, ok := state.Job(parent.ID)
	if !ok {
		t.Fatal("parent not found")
	}
	if stillParent.Status == jobs.StatusSucceeded || stillParent.FinishedAt.IsZero() == false {
		t.Fatalf("partial terminal result must not complete parent: %#v", stillParent)
	}
}

func TestCDIPTerminalDecodeParentResultReportsResidentDaemonKV(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageJob, err := state.CreateJob(jobs.CreateRequest{
		Type:            models.JobGenerateStage,
		Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
		RequestedBy:     "distributed-coordinator:" + parent.ID,
		AssignedTo:      "node-c",
		CDIPState:       cdip.StageDecode,
		CDIPParentJobID: parent.ID,
		CDIPStageIndex:  2,
		NoAutoAssign:    true,
	})
	if err != nil {
		t.Fatal(err)
	}

	terminalResult := `{
		"kind":"cdip.stage_terminal_decode",
		"runner_mode":"llama.cpp-stage-daemon",
		"parent_job_id":"` + parent.ID + `",
		"upstream_stage_job_id":"job-stage-1",
		"stage_job_id":"` + stageJob.ID + `",
		"stage_index":2,
		"step":3,
		"kv_cache_key":"cdip-session-` + parent.ID + `:kv",
		"next_token_id":40,
		"next_token_text":"I",
		"tokens":[40],
		"output":"I",
		"final":true,
		"stage_daemon_decode":{
			"session_id":"stage-2-test",
			"ready":true,
			"persistent_model":true,
			"persistent_kv_in_memory":true,
			"decode_steps":2
		}
	}`
	body, err := json.Marshal(jobs.CompleteRequest{
		NodeID: "node-c",
		Result: terminalResult,
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageJob.ID+"/complete", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var completed jobs.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(completed.Result), &result); err != nil {
		t.Fatal(err)
	}
	if result["execution_mode"] != "resident-stage-daemon" || result["resident_kv_in_memory"] != true {
		t.Fatalf("expected resident daemon execution result, got %#v", result)
	}
	if !strings.Contains(result["guardrail"].(string), "resident stage daemon kept persistent model and KV session in memory") {
		t.Fatalf("expected resident daemon guardrail, got %#v", result["guardrail"])
	}
}

func TestCDIPTerminalDecodePartialDispatchesNextLoopStep(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello","max_tokens":3}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageIDs := make([]string, 0, 2)
	for index, nodeID := range []string{"node-a", "node-b"} {
		stage := models.DistributedStageInput{Index: index, NodeID: nodeID, LayerStart: index * 12, LayerEnd: index*12 + 11, Layers: 12}
		input, err := json.Marshal(models.DistributedStageJobInput{
			ParentJobID: parent.ID,
			ModelID:     "qwen2.5-7b-instruct-q4-k-m",
			Stage:       stage,
			Shard: cdip.ModelShard{
				Stage:           cdip.Stage{Index: stage.Index, NodeID: stage.NodeID, LayerStart: stage.LayerStart, LayerEnd: stage.LayerEnd},
				Runtime:         string(models.RuntimeLlamaCPP),
				Materialization: cdip.ShardLogicalLayers,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		job, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           string(input),
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StageDecode,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		stageIDs = append(stageIDs, job.ID)
	}
	sourceResult := `{
		"kind":"cdip.stage_source_decode",
		"parent_job_id":"` + parent.ID + `",
		"stage_job_id":"` + stageIDs[0] + `",
		"stage_index":0,
		"step":2,
		"kv_cache_key":"cdip-session-` + parent.ID + `:kv"
	}`
	sourceBody, err := json.Marshal(jobs.CompleteRequest{NodeID: "node-a", Result: sourceResult})
	if err != nil {
		t.Fatal(err)
	}
	sourceReq := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageIDs[0]+"/complete", strings.NewReader(string(sourceBody)))
	sourceRec := httptest.NewRecorder()
	srv.ServeHTTP(sourceRec, sourceReq)
	if sourceRec.Code != http.StatusOK {
		t.Fatalf("expected source status 200, got %d: %s", sourceRec.Code, sourceRec.Body.String())
	}
	partialResult := `{
		"kind":"cdip.stage_terminal_decode",
		"parent_job_id":"` + parent.ID + `",
		"upstream_stage_job_id":"` + stageIDs[0] + `",
		"stage_job_id":"` + stageIDs[1] + `",
		"stage_index":1,
		"step":2,
		"kv_cache_key":"cdip-session-` + parent.ID + `:kv",
		"next_token_id":42,
		"next_token_text":" hello",
		"tokens":[41,42],
		"output":" hi hello",
		"final":false
	}`
	body, err := json.Marshal(jobs.CompleteRequest{NodeID: "node-b", Result: partialResult})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageIDs[1]+"/complete", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}
	stillParent, ok := state.Job(parent.ID)
	if !ok {
		t.Fatal("parent not found")
	}
	if stillParent.Status == jobs.StatusSucceeded || !stillParent.FinishedAt.IsZero() {
		t.Fatalf("partial terminal result must not complete parent: %#v", stillParent)
	}
	for _, stageID := range stageIDs {
		stageJob, ok := state.Job(stageID)
		if !ok {
			t.Fatalf("missing stage job %s", stageID)
		}
		if stageJob.Status != jobs.StatusScheduled || stageJob.CDIPState != cdip.StageDecode {
			t.Fatalf("expected next loop step to be scheduled, got %#v", stageJob)
		}
		var input models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &input); err != nil {
			t.Fatal(err)
		}
		if input.Step != 3 || input.KVCacheKey != "cdip-session-"+parent.ID+":kv" {
			t.Fatalf("expected next step 3 with KV cache key, got %#v", input)
		}
	}
	source, _ := state.Job(stageIDs[0])
	var sourceInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(source.Input), &sourceInput); err != nil {
		t.Fatal(err)
	}
	if sourceInput.StageCommand != "source_decode" || sourceInput.DownstreamStageID != stageIDs[1] {
		t.Fatalf("unexpected next source input: %#v", sourceInput)
	}
	if sourceInput.PreviousTokenID == nil || *sourceInput.PreviousTokenID != 42 || sourceInput.PreviousTokenText != " hello" {
		t.Fatalf("expected next source step to receive terminal token feedback, got %#v", sourceInput)
	}
	terminal, _ := state.Job(stageIDs[1])
	var terminalInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(terminal.Input), &terminalInput); err != nil {
		t.Fatal(err)
	}
	if terminalInput.StageCommand != "terminal_decode" || terminalInput.UpstreamStageID != stageIDs[0] {
		t.Fatalf("unexpected next terminal input: %#v", terminalInput)
	}
	if terminalInput.PreviousTokenID != nil || terminalInput.PreviousTokenText != "" {
		t.Fatalf("terminal input must not receive source token feedback: %#v", terminalInput)
	}
}

func TestCDIPPartialDecodeWaitsForAllStagesBeforeNextLoopStep(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello","max_tokens":3}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageIDs := make([]string, 0, 3)
	for index, nodeID := range []string{"node-a", "node-b", "node-c"} {
		stage := models.DistributedStageInput{Index: index, NodeID: nodeID, LayerStart: index * 8, LayerEnd: index*8 + 7, Layers: 8}
		input, err := json.Marshal(models.DistributedStageJobInput{
			ParentJobID:  parent.ID,
			StageJobID:   "pending",
			StageCommand: "decode",
			Step:         2,
			KVCacheKey:   "cdip-session-" + parent.ID + ":kv",
			ModelID:      "qwen2.5-7b-instruct-q4-k-m",
			Stage:        stage,
			Shard: cdip.ModelShard{
				Stage:           cdip.Stage{Index: stage.Index, NodeID: stage.NodeID, LayerStart: stage.LayerStart, LayerEnd: stage.LayerEnd},
				Runtime:         string(models.RuntimeLlamaCPP),
				Materialization: cdip.ShardLogicalLayers,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		job, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           string(input),
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StageDecode,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		stageIDs = append(stageIDs, job.ID)
	}

	completeStage := func(stageID string, nodeID string, result string) {
		t.Helper()
		body, err := json.Marshal(jobs.CompleteRequest{NodeID: nodeID, Result: result})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageID+"/complete", strings.NewReader(string(body)))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}
	}

	sourceResult := `{
		"kind":"cdip.stage_source_decode",
		"parent_job_id":"` + parent.ID + `",
		"stage_job_id":"` + stageIDs[0] + `",
		"stage_index":0,
		"step":2,
		"kv_cache_key":"cdip-session-` + parent.ID + `:kv"
	}`
	middleResult := `{
		"kind":"cdip.stage_relay_decode",
		"parent_job_id":"` + parent.ID + `",
		"upstream_stage_job_id":"` + stageIDs[0] + `",
		"stage_job_id":"` + stageIDs[1] + `",
		"stage_index":1,
		"step":2,
		"kv_cache_key":"cdip-session-` + parent.ID + `:kv"
	}`
	terminalResult := `{
		"kind":"cdip.stage_terminal_decode",
		"parent_job_id":"` + parent.ID + `",
		"upstream_stage_job_id":"` + stageIDs[1] + `",
		"stage_job_id":"` + stageIDs[2] + `",
		"stage_index":2,
		"step":2,
		"kv_cache_key":"cdip-session-` + parent.ID + `:kv",
		"next_token_id":42,
		"next_token_text":" hello",
		"tokens":[41,42],
		"output":" hi hello",
		"final":false
	}`

	completeStage(stageIDs[0], "node-a", sourceResult)
	completeStage(stageIDs[2], "node-c", terminalResult)
	for _, stageID := range stageIDs {
		stageJob, ok := state.Job(stageID)
		if !ok {
			t.Fatalf("missing stage job %s", stageID)
		}
		var input models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &input); err != nil {
			t.Fatal(err)
		}
		if input.Step != 2 {
			t.Fatalf("next loop step must wait for middle stage completion, got step=%d job=%#v", input.Step, stageJob)
		}
	}

	completeStage(stageIDs[1], "node-b", middleResult)
	for _, stageID := range stageIDs {
		stageJob, ok := state.Job(stageID)
		if !ok {
			t.Fatalf("missing stage job %s", stageID)
		}
		if stageJob.Status != jobs.StatusScheduled || stageJob.CDIPState != cdip.StageDecode {
			t.Fatalf("expected synchronized next loop step to be scheduled, got %#v", stageJob)
		}
		var input models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &input); err != nil {
			t.Fatal(err)
		}
		if input.Step != 3 || input.KVCacheKey != "cdip-session-"+parent.ID+":kv" {
			t.Fatalf("expected synchronized step 3 with KV cache key, got %#v", input)
		}
	}
}

func TestCDIPDecodeLoopEndpointCompletesParentWithTokenSequence(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello","max_tokens":8}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range []string{"node-a", "node-b", "node-c"} {
		_, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StagePrefill,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode-loop", strings.NewReader(`{"max_tokens":3}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPDecodeLoopResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Final || result.Output != " token-1 token-2 token-3" || len(result.Chunks) != 3 || len(result.Messages) != 9 {
		t.Fatalf("unexpected decode loop result: %#v", result)
	}
	if result.Trace.Protocol != cdip.Protocol || result.Trace.Version != cdip.Version || result.Trace.StageCount != 3 || result.Trace.MaxTokens != 3 {
		t.Fatalf("unexpected decode loop trace: %#v", result.Trace)
	}
	if result.Trace.SessionID != "cdip-session-"+parent.ID || result.Trace.KVCacheKey != result.Trace.SessionID+":kv" || result.Trace.TerminalStageJobID == "" {
		t.Fatalf("unexpected decode loop session trace: %#v", result.Trace)
	}
	if !result.Chunks[0].Final && !result.Chunks[1].Final && result.Chunks[2].Final {
		// expected final flags
	} else {
		t.Fatalf("unexpected chunk final flags: %#v", result.Chunks)
	}
	for _, chunk := range result.Chunks {
		if chunk.KVCacheKey != result.Trace.KVCacheKey {
			t.Fatalf("expected chunk KV cache key %q, got %#v", result.Trace.KVCacheKey, chunk)
		}
	}
	if result.ParentJob.Status != jobs.StatusSucceeded {
		t.Fatalf("expected completed parent, got %#v", result.ParentJob)
	}
	var parentResult map[string]any
	if err := json.Unmarshal([]byte(result.ParentJob.Result), &parentResult); err != nil {
		t.Fatal(err)
	}
	if parentResult["kind"] != "cdip.distributed_decode_loop_result" || int(parentResult["token_count"].(float64)) != 3 || parentResult["output"] != result.Output {
		t.Fatalf("unexpected parent result: %#v", parentResult)
	}
	trace, ok := parentResult["trace"].(map[string]any)
	if !ok || trace["kv_cache_key"] != result.Trace.KVCacheKey || int(trace["stage_count"].(float64)) != 3 {
		t.Fatalf("unexpected parent trace: %#v", parentResult["trace"])
	}
}

func TestCDIPDecodeLoopDispatchEndpointSchedulesStageCommandsWithKVTrace(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello","max_tokens":8}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageIDs := make([]string, 0, 3)
	for index, nodeID := range []string{"node-a", "node-b", "node-c"} {
		stage := models.DistributedStageInput{Index: index, NodeID: nodeID, LayerStart: index * 4, LayerEnd: index*4 + 3, Layers: 4}
		input, err := json.Marshal(models.DistributedStageJobInput{
			ParentJobID: parent.ID,
			ModelID:     "qwen2.5-7b-instruct-q4-k-m",
			Stage:       stage,
			Shard: cdip.ModelShard{
				Stage:           cdip.Stage{Index: stage.Index, NodeID: stage.NodeID, LayerStart: stage.LayerStart, LayerEnd: stage.LayerEnd},
				Runtime:         string(models.RuntimeLlamaCPP),
				Materialization: cdip.ShardLogicalLayers,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		job, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           string(input),
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StagePrefill,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		stageIDs = append(stageIDs, job.ID)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode-loop", strings.NewReader(`{"mode":"dispatch","step":2,"max_tokens":5,"terminal_force_final":false}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPCommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Trace == nil || result.Trace.Mode != "worker-dispatch" || result.Trace.KVCacheKey != "cdip-session-"+parent.ID+":kv" || result.Trace.MaxTokens != 5 {
		t.Fatalf("unexpected dispatch trace: %#v", result.Trace)
	}
	if len(result.Messages) != 3 || result.Messages[0].Step != 2 || result.Messages[2].Step != 2 {
		t.Fatalf("unexpected dispatch messages: %#v", result.Messages)
	}
	for _, stageID := range stageIDs {
		stageJob, ok := state.Job(stageID)
		if !ok {
			t.Fatalf("missing stage job %s", stageID)
		}
		if stageJob.Status != jobs.StatusScheduled || stageJob.CDIPState != cdip.StageDecode {
			t.Fatalf("expected scheduled decode stage, got %#v", stageJob)
		}
		var input models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &input); err != nil {
			t.Fatal(err)
		}
		if input.Step != 2 || input.KVCacheKey != result.Trace.KVCacheKey {
			t.Fatalf("expected step/KV in stage input, got %#v", input)
		}
	}
	source, _ := state.Job(stageIDs[0])
	var sourceInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(source.Input), &sourceInput); err != nil {
		t.Fatal(err)
	}
	if sourceInput.StageCommand != "source_decode" || sourceInput.DownstreamStageID != stageIDs[1] || sourceInput.DownstreamNodeID != "node-b" {
		t.Fatalf("unexpected source input: %#v", sourceInput)
	}
	if sourceInput.TerminalForceFinal != nil {
		t.Fatalf("source input must not receive terminal force-final hook: %#v", sourceInput)
	}
	relay, _ := state.Job(stageIDs[1])
	var relayInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(relay.Input), &relayInput); err != nil {
		t.Fatal(err)
	}
	if relayInput.StageCommand != "relay_decode" || relayInput.UpstreamStageID != stageIDs[0] || relayInput.DownstreamStageID != stageIDs[2] || relayInput.DownstreamNodeID != "node-c" {
		t.Fatalf("unexpected relay input: %#v", relayInput)
	}
	if relayInput.TerminalForceFinal != nil {
		t.Fatalf("relay input must not receive terminal force-final hook: %#v", relayInput)
	}
	terminal, _ := state.Job(stageIDs[2])
	var terminalInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(terminal.Input), &terminalInput); err != nil {
		t.Fatal(err)
	}
	if terminalInput.StageCommand != "terminal_decode" || terminalInput.UpstreamStageID != stageIDs[1] || terminalInput.DownstreamStageID != "" || terminalInput.DownstreamNodeID != "" {
		t.Fatalf("unexpected terminal input: %#v", terminalInput)
	}
	if terminalInput.TerminalForceFinal == nil || *terminalInput.TerminalForceFinal {
		t.Fatalf("expected terminal force-final=false hook, got %#v", terminalInput.TerminalForceFinal)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode-loop", strings.NewReader(`{"mode":"dispatch","step":3,"max_tokens":5}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for step 3, got %d: %s", rec.Code, rec.Body.String())
	}
	terminal, _ = state.Job(stageIDs[2])
	terminalInput = models.DistributedStageJobInput{}
	if err := json.Unmarshal([]byte(terminal.Input), &terminalInput); err != nil {
		t.Fatal(err)
	}
	if terminalInput.Step != 3 || terminalInput.TerminalForceFinal == nil || *terminalInput.TerminalForceFinal {
		t.Fatalf("expected step 3 to keep terminal force-final=false before max tokens, got %#v", terminalInput)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode-loop", strings.NewReader(`{"mode":"dispatch","step":5,"max_tokens":5}`))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 for final step, got %d: %s", rec.Code, rec.Body.String())
	}
	terminal, _ = state.Job(stageIDs[2])
	terminalInput = models.DistributedStageJobInput{}
	if err := json.Unmarshal([]byte(terminal.Input), &terminalInput); err != nil {
		t.Fatal(err)
	}
	if terminalInput.Step != 5 || terminalInput.TerminalForceFinal == nil || !*terminalInput.TerminalForceFinal {
		t.Fatalf("expected final step to set terminal force-final=true, got %#v", terminalInput)
	}
	stillParent, ok := state.Job(parent.ID)
	if !ok {
		t.Fatal("parent not found")
	}
	if stillParent.Status == jobs.StatusSucceeded || !stillParent.FinishedAt.IsZero() {
		t.Fatalf("dispatch mode must not complete parent: %#v", stillParent)
	}
}

func TestCDIPPrefillEndpointRequiresReadyStages(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, stateValue := range []cdip.StageState{cdip.StageReady, cdip.StagePreparing} {
		_, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      []string{"node-a", "node-b"}[index],
			CDIPState:       stateValue,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/prefill", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCDIPPrefillEndpointBuildsStagePrefillMessages(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range []string{"node-a", "node-b"} {
		_, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StageReady,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/prefill", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPCommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 || len(result.StageJobs) != 2 {
		t.Fatalf("unexpected prefill result: %#v", result)
	}
	for i, msg := range result.Messages {
		if err := msg.Validate(cdip.MessageStagePrefill); err != nil {
			t.Fatal(err)
		}
		if msg.StageIndex != i || result.StageJobs[i].CDIPState != cdip.StagePrefill {
			t.Fatalf("expected stage %d prefill, got msg=%#v job=%#v", i, msg, result.StageJobs[i])
		}
	}
}

func TestCDIPDecodeEndpointRequiresPrefillStages(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, stateValue := range []cdip.StageState{cdip.StagePrefill, cdip.StageReady} {
		_, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      []string{"node-a", "node-b"}[index],
			CDIPState:       stateValue,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode", strings.NewReader(`{"step":1}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCDIPDecodeEndpointBuildsStageDecodeMessagesAndActivationFrames(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range []string{"node-a", "node-b", "node-c"} {
		_, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StagePrefill,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode", strings.NewReader(`{"step":3}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPCommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 3 || len(result.StageJobs) != 3 || len(result.ActivationFrames) != 2 {
		t.Fatalf("unexpected decode result: %#v", result)
	}
	for i, msg := range result.Messages {
		if err := msg.Validate(cdip.MessageStageDecode); err != nil {
			t.Fatal(err)
		}
		if msg.StageIndex != i || msg.Step != 3 || result.StageJobs[i].CDIPState != cdip.StageDecode {
			t.Fatalf("expected stage %d decode, got msg=%#v job=%#v", i, msg, result.StageJobs[i])
		}
	}
	for _, frame := range result.ActivationFrames {
		if err := frame.Validate(); err != nil {
			t.Fatal(err)
		}
		if frame.Type != cdip.MessageActivationChunk || frame.Sequence != 3 {
			t.Fatalf("unexpected activation frame: %#v", frame)
		}
	}
}

func TestCDIPRelayDecodeEndpointDispatchesWorkerStageCommands(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageIDs := make([]string, 0, 3)
	for index, nodeID := range []string{"node-a", "node-b", "node-c"} {
		stage := models.DistributedStageInput{Index: index, NodeID: nodeID, LayerStart: index * 4, LayerEnd: index*4 + 3, Layers: 4}
		stageInput, err := json.Marshal(models.DistributedStageJobInput{
			ParentJobID: parent.ID,
			ModelID:     "qwen2.5-7b-instruct-q4-k-m",
			Stage:       stage,
			Shard: cdip.ModelShard{
				Stage:           cdip.Stage{Index: stage.Index, NodeID: stage.NodeID, LayerStart: stage.LayerStart, LayerEnd: stage.LayerEnd},
				Runtime:         string(models.RuntimeLlamaCPP),
				Materialization: cdip.ShardLogicalLayers,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		job, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           string(stageInput),
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StagePrefill,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		stageIDs = append(stageIDs, job.ID)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode", strings.NewReader(`{"step":7,"mode":"relay_decode"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPCommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 3 || len(result.ActivationFrames) != 0 {
		t.Fatalf("unexpected relay decode result: %#v", result)
	}
	for _, stageJob := range result.StageJobs {
		if stageJob.CDIPState != cdip.StageDecode {
			t.Fatalf("expected decode state, got %#v", stageJob)
		}
	}
	source, ok := state.Job(stageIDs[0])
	if !ok {
		t.Fatal("source stage job not found")
	}
	if source.Status != jobs.StatusScheduled {
		t.Fatalf("expected source stage to be scheduled, got %#v", source)
	}
	var sourceInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(source.Input), &sourceInput); err != nil {
		t.Fatal(err)
	}
	if sourceInput.StageCommand != "source_decode" || sourceInput.StageJobID != stageIDs[0] || sourceInput.DownstreamStageID != stageIDs[1] || sourceInput.DownstreamNodeID != "node-b" {
		t.Fatalf("unexpected source stage input: %#v", sourceInput)
	}
	middle, ok := state.Job(stageIDs[1])
	if !ok {
		t.Fatal("middle stage job not found")
	}
	if middle.Status != jobs.StatusScheduled {
		t.Fatalf("expected middle relay stage to be scheduled, got %#v", middle)
	}
	var input models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(middle.Input), &input); err != nil {
		t.Fatal(err)
	}
	if input.StageCommand != "relay_decode" || input.StageJobID != stageIDs[1] || input.UpstreamStageID != stageIDs[0] || input.DownstreamStageID != stageIDs[2] || input.DownstreamNodeID != "node-c" {
		t.Fatalf("unexpected relay stage input: %#v", input)
	}
	terminal, ok := state.Job(stageIDs[2])
	if !ok {
		t.Fatal("terminal stage job not found")
	}
	if terminal.Status != jobs.StatusScheduled {
		t.Fatalf("expected terminal stage to be scheduled, got %#v", terminal)
	}
	var terminalInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(terminal.Input), &terminalInput); err != nil {
		t.Fatal(err)
	}
	if terminalInput.StageCommand != "terminal_decode" || terminalInput.StageJobID != stageIDs[2] || terminalInput.UpstreamStageID != stageIDs[1] || terminalInput.UpstreamNodeID != "node-b" || terminalInput.DownstreamStageID != "" {
		t.Fatalf("unexpected terminal stage input: %#v", terminalInput)
	}
	next, ok := state.NextJobForWorker("node-a")
	if !ok || next.ID != source.ID || next.Status != jobs.StatusRunning {
		t.Fatalf("expected source worker to receive source decode job, got %#v ok=%v", next, ok)
	}
	next, ok = state.NextJobForWorker("node-b")
	if !ok || next.ID != middle.ID || next.Status != jobs.StatusRunning {
		t.Fatalf("expected middle worker to receive relay decode job, got %#v ok=%v", next, ok)
	}
	next, ok = state.NextJobForWorker("node-c")
	if !ok || next.ID != terminal.ID || next.Status != jobs.StatusRunning {
		t.Fatalf("expected terminal worker to receive terminal decode job, got %#v ok=%v", next, ok)
	}
}

func TestCDIPRelayDecodeEndpointSupportsTwoStageSourceTerminal(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageIDs := make([]string, 0, 2)
	for index, nodeID := range []string{"node-a", "node-b"} {
		stage := models.DistributedStageInput{Index: index, NodeID: nodeID, LayerStart: index * 16, LayerEnd: index*16 + 15, Layers: 16}
		stageInput, err := json.Marshal(models.DistributedStageJobInput{
			ParentJobID: parent.ID,
			ModelID:     "qwen2.5-7b-instruct-q4-k-m",
			Stage:       stage,
			Shard: cdip.ModelShard{
				Stage:           cdip.Stage{Index: stage.Index, NodeID: stage.NodeID, LayerStart: stage.LayerStart, LayerEnd: stage.LayerEnd},
				Runtime:         string(models.RuntimeLlamaCPP),
				Materialization: cdip.ShardLogicalLayers,
			},
			StageRunnerBin: "/tmp/cmesh-stage-runner",
			ModelPath:      "/tmp/model.gguf",
			WorkDir:        fmt.Sprintf("/tmp/cmesh-stage-run/stage-%d", index),
			TimeoutMS:      120000,
		})
		if err != nil {
			t.Fatal(err)
		}
		job, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           string(stageInput),
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StagePrefill,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
		stageIDs = append(stageIDs, job.ID)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/decode", strings.NewReader(`{"step":1,"mode":"relay_decode"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPCommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 || len(result.StageJobs) != 2 {
		t.Fatalf("unexpected two-stage relay result: %#v", result)
	}
	source, ok := state.Job(stageIDs[0])
	if !ok {
		t.Fatal("source stage job not found")
	}
	var sourceInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(source.Input), &sourceInput); err != nil {
		t.Fatal(err)
	}
	if sourceInput.StageCommand != "source_decode" || sourceInput.DownstreamStageID != stageIDs[1] || sourceInput.DownstreamNodeID != "node-b" {
		t.Fatalf("unexpected two-stage source input: %#v", sourceInput)
	}
	terminal, ok := state.Job(stageIDs[1])
	if !ok {
		t.Fatal("terminal stage job not found")
	}
	var terminalInput models.DistributedStageJobInput
	if err := json.Unmarshal([]byte(terminal.Input), &terminalInput); err != nil {
		t.Fatal(err)
	}
	if terminalInput.StageCommand != "terminal_decode" || terminalInput.UpstreamStageID != stageIDs[0] || terminalInput.UpstreamNodeID != "node-a" || terminalInput.DownstreamStageID != "" {
		t.Fatalf("unexpected two-stage terminal input: %#v", terminalInput)
	}
	if terminalInput.StageRunnerBin != "/tmp/cmesh-stage-runner" || terminalInput.ModelPath != "/tmp/model.gguf" || terminalInput.TimeoutMS != 120000 {
		t.Fatalf("expected runner metadata to survive decode dispatch: %#v", terminalInput)
	}
}

func TestCDIPActivationFrameRelayEndpoint(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-0.5b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := state.CreateJob(jobs.CreateRequest{
		Type:            models.JobGenerateStage,
		Input:           `{"model_id":"qwen2.5-0.5b-instruct-q4-k-m"}`,
		RequestedBy:     "test",
		AssignedTo:      "node-b",
		MaxAttempts:     1,
		CDIPState:       cdip.StageDecode,
		CDIPParentJobID: parent.ID,
		CDIPStageIndex:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := transport.ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  parent.ID,
			StageJobID:   stage.ID,
			Sequence:     4,
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     "raw",
			Shape:        []int{1, 1, 4},
			DType:        "f16",
			PayloadBytes: 4,
		},
		Payload: []byte{9, 8, 7, 6},
	}
	body, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/activations/"+parent.ID+"/"+stage.ID+"/frames", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/cdip/activations/"+parent.ID+"/"+stage.ID+"/frames?timeout_ms=100", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got transport.ActivationFrame
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Header.Sequence != frame.Header.Sequence || string(got.Payload) != string(frame.Payload) {
		t.Fatalf("unexpected relayed activation frame: %#v", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/cdip/activations/"+parent.ID+"/"+stage.ID+"/frames?timeout_ms=1", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 after queue drains, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCDIPActivationFrameRelayAllowsAssignedWorkersWithoutOperatorToken(t *testing.T) {
	state := NewState()
	srv := NewServerWithOptions(ServerOptions{OperatorToken: "operator-token"}, state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-0.5b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	stageInput, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID:      parent.ID,
		ModelID:          "qwen2.5-0.5b-instruct-q4-k-m",
		Stage:            models.DistributedStageInput{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 11},
		DownstreamNodeID: "node-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := state.CreateJob(jobs.CreateRequest{
		Type:            models.JobGenerateStage,
		Input:           string(stageInput),
		RequestedBy:     "test",
		AssignedTo:      "node-a",
		NoAutoAssign:    true,
		MaxAttempts:     1,
		CDIPState:       cdip.StageDecode,
		CDIPParentJobID: parent.ID,
		CDIPStageIndex:  0,
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := transport.ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  parent.ID,
			StageJobID:   stage.ID,
			Sequence:     1,
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     "raw",
			Shape:        []int{1, 1, 2},
			DType:        "u8",
			PayloadBytes: 2,
		},
		Payload: []byte{1, 2},
	}
	body, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1/cdip/activations/" + parent.ID + "/" + stage.ID + "/frames"

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without operator token or node id, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("X-CMesh-Node-ID", "node-x")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unrelated worker, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("X-CMesh-Node-ID", "node-a")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected upstream worker POST access, got %d: %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, path+"?timeout_ms=100", nil)
	req.Header.Set("X-CMesh-Node-ID", "node-b")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected downstream worker GET access, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCDIPCompleteEndpointRequiresDecodeStages(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, stateValue := range []cdip.StageState{cdip.StageDecode, cdip.StagePrefill} {
		_, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      []string{"node-a", "node-b"}[index],
			CDIPState:       stateValue,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/complete", strings.NewReader(`{"output":"done"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCDIPCompleteEndpointCompletesStagesAndParent(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
	parent, err := state.CreateJob(jobs.CreateRequest{
		Type:        models.JobGenerateDistributed,
		Input:       `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
		RequestedBy: "test",
		MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, nodeID := range []string{"node-a", "node-b", "node-c"} {
		_, err := state.CreateJob(jobs.CreateRequest{
			Type:            models.JobGenerateStage,
			Input:           `{"model_id":"qwen2.5-7b-instruct-q4-k-m"}`,
			RequestedBy:     "distributed-coordinator:" + parent.ID,
			AssignedTo:      nodeID,
			CDIPState:       cdip.StageDecode,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  index,
			NoAutoAssign:    true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cdip/jobs/"+parent.ID+"/complete", strings.NewReader(`{"output":"distributed answer"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var result CDIPCommandResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ParentJob.Status != jobs.StatusSucceeded || !strings.Contains(result.ParentJob.Result, "distributed answer") {
		t.Fatalf("expected completed parent job, got %#v", result.ParentJob)
	}
	if len(result.Messages) != 3 || len(result.StageJobs) != 3 {
		t.Fatalf("unexpected complete result: %#v", result)
	}
	for i, msg := range result.Messages {
		if err := msg.Validate(cdip.MessageStageComplete); err != nil {
			t.Fatal(err)
		}
		if msg.StageIndex != i || result.StageJobs[i].CDIPState != cdip.StageCompleted || result.StageJobs[i].Status != jobs.StatusSucceeded {
			t.Fatalf("expected stage %d completed, got msg=%#v job=%#v", i, msg, result.StageJobs[i])
		}
	}
}

func TestDistributedStageJobRequestsBuildPipelineTopology(t *testing.T) {
	parent := jobs.Job{ID: "job-parent", Type: models.JobGenerateDistributed}
	input := models.DistributedGenerateInput{
		ModelID:        "qwen2.5-7b-instruct-q4-k-m",
		Prompt:         "hello",
		ConversationID: "conv-1",
		SystemPrompt:   "system",
		MaxTokens:      128,
		Temperature:    "0.5",
		StageRunnerBin: "/tmp/cmesh-stage-runner",
		StageDaemonURL: "http://127.0.0.1:19781",
		ModelPath:      "/tmp/model.gguf",
		WorkDir:        "/tmp/cmesh-stage-run",
		TimeoutMS:      15000,
		Stages: []models.DistributedStageInput{
			{Index: 0, NodeID: "node-a", NodeName: "A", LayerStart: 0, LayerEnd: 10, Layers: 11},
			{Index: 1, NodeID: "node-b", NodeName: "B", LayerStart: 11, LayerEnd: 20, Layers: 10},
			{Index: 2, NodeID: "node-c", NodeName: "C", LayerStart: 21, LayerEnd: 31, Layers: 11},
		},
		Shards: []cdip.ModelShard{
			{Stage: cdip.Stage{Index: 0, NodeID: "node-a", NodeName: "A", LayerStart: 0, LayerEnd: 10}, Runtime: "llama.cpp", SourceArtifact: "https://example.test/model.gguf", TargetArtifact: "stage-0", Materialization: cdip.ShardLogicalLayers},
			{Stage: cdip.Stage{Index: 1, NodeID: "node-b", NodeName: "B", LayerStart: 11, LayerEnd: 20}, Runtime: "llama.cpp", SourceArtifact: "https://example.test/model.gguf", TargetArtifact: "stage-1", Materialization: cdip.ShardLogicalLayers},
			{Stage: cdip.Stage{Index: 2, NodeID: "node-c", NodeName: "C", LayerStart: 21, LayerEnd: 31}, Runtime: "llama.cpp", SourceArtifact: "https://example.test/model.gguf", TargetArtifact: "stage-2", Materialization: cdip.ShardLogicalLayers},
		},
	}

	requests, err := distributedStageJobRequests(parent, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected three stage job requests, got %#v", requests)
	}
	for index, req := range requests {
		if req.Type != models.JobGenerateStage || req.AssignedTo != input.Stages[index].NodeID {
			t.Fatalf("unexpected stage request %d: %#v", index, req)
		}
		if req.RequestedBy != "distributed-coordinator:job-parent" {
			t.Fatalf("expected coordinator requested_by, got %#v", req)
		}
		if !req.NoAutoAssign {
			t.Fatalf("stage request must not be auto-scheduled before distributed transport exists: %#v", req)
		}
		var stageInput models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(req.Input), &stageInput); err != nil {
			t.Fatal(err)
		}
		if stageInput.ParentJobID != parent.ID || stageInput.ModelID != input.ModelID || stageInput.Stage.Index != index {
			t.Fatalf("unexpected stage input %d: %#v", index, stageInput)
		}
		if stageInput.Shard.Stage.Index != index || stageInput.Shard.Runtime != "llama.cpp" || stageInput.Shard.TargetArtifact == "" {
			t.Fatalf("expected shard contract in stage input %d: %#v", index, stageInput.Shard)
		}
		if stageInput.StageRunnerBin != input.StageRunnerBin || stageInput.StageDaemonURL != input.StageDaemonURL || stageInput.ModelPath != input.ModelPath || stageInput.TimeoutMS != input.TimeoutMS {
			t.Fatalf("expected runner metadata in stage input %d: %#v", index, stageInput)
		}
		if stageInput.StageSessionID == "" || !strings.HasPrefix(stageInput.StageSessionID, fmt.Sprintf("stage-%d-", index)) {
			t.Fatalf("expected deterministic daemon stage session id in stage input %d: %#v", index, stageInput)
		}
		expectedWorkDir := fmt.Sprintf("/tmp/cmesh-stage-run/stage-%d", index)
		if stageInput.WorkDir != expectedWorkDir {
			t.Fatalf("expected stage-specific work dir %q, got %#v", expectedWorkDir, stageInput)
		}
		switch index {
		case 0:
			if stageInput.UpstreamNodeID != "" || stageInput.DownstreamNodeID != "node-b" {
				t.Fatalf("unexpected first-stage links: %#v", stageInput)
			}
		case 1:
			if stageInput.UpstreamNodeID != "node-a" || stageInput.DownstreamNodeID != "node-c" {
				t.Fatalf("unexpected middle-stage links: %#v", stageInput)
			}
		case 2:
			if stageInput.UpstreamNodeID != "node-b" || stageInput.DownstreamNodeID != "" {
				t.Fatalf("unexpected final-stage links: %#v", stageInput)
			}
		}
	}
}

func TestDistributedStageJobRequestsRejectsInvalidStageOrder(t *testing.T) {
	_, err := distributedStageJobRequests(jobs.Job{ID: "job-parent"}, models.DistributedGenerateInput{
		ModelID: "qwen2.5-7b-instruct-q4-k-m",
		Stages: []models.DistributedStageInput{
			{Index: 0, NodeID: "node-a"},
			{Index: 3, NodeID: "node-b"},
		},
		Shards: []cdip.ModelShard{
			{Stage: cdip.Stage{Index: 0, NodeID: "node-a"}, Runtime: "llama.cpp", Materialization: cdip.ShardLogicalLayers},
			{Stage: cdip.Stage{Index: 3, NodeID: "node-b"}, Runtime: "llama.cpp", Materialization: cdip.ShardLogicalLayers},
		},
	})
	if err == nil {
		t.Fatal("expected invalid stage order error")
	}
	if !strings.Contains(err.Error(), "stage index mismatch") {
		t.Fatalf("expected stage index mismatch error, got %v", err)
	}
}
