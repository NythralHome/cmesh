package resources

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cmesh/cmesh/internal/config"
)

func TestDiscoverLocalAppliesLimits(t *testing.T) {
	snapshot := DiscoverLocal(DiscoveryOptions{
		CacheDir: ".",
		Limits: config.ResourceLimits{
			CPUCores:    2,
			MemoryBytes: 1024,
			DiskBytes:   2048,
		},
	})

	if snapshot.CPU.CoresTotal != runtime.NumCPU() {
		t.Fatalf("expected %d total CPU cores, got %d", runtime.NumCPU(), snapshot.CPU.CoresTotal)
	}
	if snapshot.CPU.CoresAllowed != 2 {
		t.Fatalf("expected 2 allowed CPU cores, got %d", snapshot.CPU.CoresAllowed)
	}
	if snapshot.Memory.AllowedBytes != 1024 {
		t.Fatalf("expected memory limit to be applied")
	}
	if snapshot.Storage.AllowedBytes != 2048 {
		t.Fatalf("expected disk limit to be applied")
	}
}

func TestDiscoverInstalledModelsScansCache(t *testing.T) {
	cacheDir := t.TempDir()
	modelDir := filepath.Join(cacheDir, "models", "qwen2.5-0.5b-instruct-q4-k-m")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(modelDir, "qwen2.5-0.5b-instruct-q4_k_m.gguf")
	if err := os.WriteFile(modelPath, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}

	installed := DiscoverInstalledModels(cacheDir)
	if len(installed) != 1 {
		t.Fatalf("expected one installed model, got %#v", installed)
	}
	if installed[0].ID != "qwen2.5-0.5b-instruct-q4-k-m" {
		t.Fatalf("unexpected model id %q", installed[0].ID)
	}
	if installed[0].Bytes != 5 {
		t.Fatalf("unexpected model size %d", installed[0].Bytes)
	}
}
