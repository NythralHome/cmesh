package manager

import (
	"path/filepath"
	"testing"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/resources"
)

func TestFileStorePersistsState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cmesh-state.json")

	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	joinResp := store.RegisterWorker(membership.JoinRequest{
		NodeName: "persisted-worker",
		Role:     cluster.NodeRoleWorker,
		Resources: cluster.ResourceSnapshot{
			CPU: cluster.CPUResources{
				CoresTotal:   4,
				CoresAllowed: 2,
			},
		},
	})
	if ok := store.PutBenchmark(resources.BenchmarkResult{
		NodeID: joinResp.NodeID,
		Kind:   resources.BenchmarkCPU,
		Score:  12.5,
		Unit:   "ops/sec",
	}); !ok {
		t.Fatalf("expected benchmark to be stored")
	}

	restored, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	nodes := restored.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("expected 1 restored node, got %d", len(nodes))
	}
	if nodes[0].Name != "persisted-worker" {
		t.Fatalf("expected restored worker name, got %q", nodes[0].Name)
	}
	if nodes[0].Resources.CPU.CoresAllowed != 2 {
		t.Fatalf("expected restored CPU resources")
	}

	summary := restored.BenchmarkSummaryByNode()[joinResp.NodeID]
	if summary.TotalScore != 12.5 {
		t.Fatalf("expected restored benchmark score, got %f", summary.TotalScore)
	}
}
