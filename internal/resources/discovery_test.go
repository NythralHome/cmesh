package resources

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/config"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/runtimes"
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
	if !installed[0].Ready {
		t.Fatalf("expected fallback scanned model to be ready")
	}
	if installed[0].Error != "manifest missing" {
		t.Fatalf("expected manifest warning, got %q", installed[0].Error)
	}
}

func TestDiscoverInstalledModelsReadsManifestMetadata(t *testing.T) {
	cacheDir := t.TempDir()
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	modelPath := ModelFilePath(cacheDir, model)
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	installedAt := time.Date(2026, 6, 17, 12, 30, 0, 0, time.UTC)
	if err := WriteModelManifest(cacheDir, model, modelPath, 5, installedAt); err != nil {
		t.Fatal(err)
	}

	installed := DiscoverInstalledModels(cacheDir)
	if len(installed) != 1 {
		t.Fatalf("expected one installed model, got %#v", installed)
	}
	if installed[0].Runtime != string(model.Runtime) || installed[0].Family != model.Family {
		t.Fatalf("expected manifest metadata, got %#v", installed[0])
	}
	if !installed[0].Ready || installed[0].Error != "" {
		t.Fatalf("expected clean ready inventory, got %#v", installed[0])
	}
	if installed[0].Bytes != 5 {
		t.Fatalf("expected manifest bytes, got %d", installed[0].Bytes)
	}
	if !installed[0].InstalledAt.Equal(installedAt) {
		t.Fatalf("expected installed_at %s, got %s", installedAt, installed[0].InstalledAt)
	}
}

func TestDiscoverInstalledModelsReportsManifestSizeMismatch(t *testing.T) {
	cacheDir := t.TempDir()
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	modelPath := ModelFilePath(cacheDir, model)
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteModelManifest(cacheDir, model, modelPath, 123, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	installed := DiscoverInstalledModels(cacheDir)
	if len(installed) != 1 {
		t.Fatalf("expected one installed model, got %#v", installed)
	}
	if !installed[0].Ready {
		t.Fatalf("expected model file to remain ready, got %#v", installed[0])
	}
	if installed[0].Bytes != 5 {
		t.Fatalf("expected actual file size to win, got %d", installed[0].Bytes)
	}
	if installed[0].Error != "manifest size does not match model file" {
		t.Fatalf("expected manifest mismatch warning, got %q", installed[0].Error)
	}
}

func TestDiscoverCMeshStorageUsageReportsPartialAndOrphanModels(t *testing.T) {
	cacheDir := t.TempDir()
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Join(cacheDir, "models", model.ID)
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, model.File+".tmp"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanDir := filepath.Join(cacheDir, "models", "unknown-model")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "unknown.gguf"), []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	usage := discoverCMeshStorageUsage(cacheDir)
	if usage.PartialModelFiles != 1 || usage.PartialModelBytes != uint64(len("partial")) {
		t.Fatalf("expected partial model usage, got %#v", usage)
	}
	if usage.OrphanModelDirs != 1 || usage.OrphanModelBytes != uint64(len("orphan")) {
		t.Fatalf("expected orphan model usage, got %#v", usage)
	}
}

func TestDiscoverRuntimesIncludesLlamaCPPStageProbe(t *testing.T) {
	items := DiscoverRuntimes(t.TempDir())
	if len(items) != 1 {
		t.Fatalf("expected one runtime, got %#v", items)
	}
	if items[0].Name != runtimes.LlamaCPPName {
		t.Fatalf("expected llama.cpp runtime, got %#v", items[0])
	}
	if len(items[0].StageRuntimes) != 1 {
		t.Fatalf("expected stage runtime probe, got %#v", items[0])
	}
	if len(items[0].RPCRuntimes) != 1 {
		t.Fatalf("expected rpc runtime probe, got %#v", items[0])
	}
	if items[0].RPCRuntimes[0].Name != runtimes.LlamaCPPRPCRuntimeName {
		t.Fatalf("expected llama.cpp rpc probe, got %#v", items[0].RPCRuntimes[0])
	}
	probe := items[0].StageRuntimes[0]
	if probe.Name != runtimes.LlamaCPPStageRuntimeName {
		t.Fatalf("expected llama.cpp stage probe, got %#v", probe)
	}
	if probe.Ready {
		t.Fatalf("llama.cpp stage runtime should remain blocked until hooks exist: %#v", probe)
	}
}
