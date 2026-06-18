package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
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
		t.Fatalf("distributed execution should remain disabled until worker runtime protocol exists: %#v", plan)
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
	if plan.EstimatedLatency.PerOutputTokenMS <= 0 || plan.Network.InterStageHops != 1 {
		t.Fatalf("expected latency and network estimates, got %#v", plan)
	}
	if !strings.Contains(strings.Join(plan.Blockers, " "), "distributed runtime protocol") {
		t.Fatalf("expected protocol blocker, got %#v", plan.Blockers)
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
	for _, expected := range []string{"at least 2 online workers", "aggregate RAM short", "aggregate disk short"} {
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
				Stage           struct {
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
		readyBody := strings.NewReader(`{"node_id":"` + stageJob.AssignedTo + `","result":"{\"kind\":\"cdip.stage_ready\"}"}`)
		readyRec := httptest.NewRecorder()
		srv.ServeHTTP(readyRec, httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageJob.ID+"/complete", readyBody))
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
	}
}

func TestCDIPStageReadyCompletionUpdatesLifecycle(t *testing.T) {
	state := NewState()
	srv := NewServer(":0", state)
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

	req := httptest.NewRequest(http.MethodPost, "/v1/jobs/"+stageJob.ID+"/complete", strings.NewReader(`{"node_id":"node-a","result":"{\"kind\":\"cdip.stage_ready\"}"}`))
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
	if updated.Status == jobs.StatusSucceeded {
		t.Fatalf("stage ready must not complete the stage job before prefill/decode: %#v", updated)
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
