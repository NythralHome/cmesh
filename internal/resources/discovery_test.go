package resources

import (
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
