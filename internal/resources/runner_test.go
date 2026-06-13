package resources

import "testing"

func TestRunLocalBenchmarks(t *testing.T) {
	results, err := RunLocalBenchmarks(BenchmarkOptions{
		NodeID:   "node-test",
		CacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 benchmark results, got %d", len(results))
	}

	for _, result := range results {
		if result.NodeID != "node-test" {
			t.Fatalf("expected node-test, got %s", result.NodeID)
		}
		if result.Score <= 0 {
			t.Fatalf("expected positive score for %s, got %f", result.Kind, result.Score)
		}
		if result.Unit == "" {
			t.Fatalf("expected unit for %s", result.Kind)
		}
	}
}
