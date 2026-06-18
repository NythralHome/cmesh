package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
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
		Plan DistributedModelPlan `json:"plan"`
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
}
