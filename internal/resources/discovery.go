package resources

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/config"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/runtimes"
)

type DiscoveryOptions struct {
	Limits   config.ResourceLimits
	CacheDir string
}

type LlamaCPPRPCState struct {
	Running           bool      `json:"running"`
	Endpoint          string    `json:"endpoint"`
	BindEndpoint      string    `json:"bind_endpoint,omitempty"`
	AdvertiseEndpoint string    `json:"advertise_endpoint,omitempty"`
	PID               int       `json:"pid,omitempty"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func DiscoverLocal(options DiscoveryOptions) cluster.ResourceSnapshot {
	if options.CacheDir != "" {
		_ = os.MkdirAll(options.CacheDir, 0o755)
	}

	totalMemory := discoverTotalMemory()
	totalStorage, freeStorage := discoverDisk(options.CacheDir)
	storageUsage := discoverCMeshStorageUsage(options.CacheDir)

	return cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{
			CoresTotal:   runtime.NumCPU(),
			CoresAllowed: allowedInt(options.Limits.CPUCores, runtime.NumCPU()),
		},
		Memory: cluster.MemoryResources{
			TotalBytes:   totalMemory,
			AllowedBytes: allowedBytes(options.Limits.MemoryBytes, totalMemory),
		},
		GPU: discoverGPUs(options.Limits),
		Storage: cluster.StorageResources{
			TotalBytes:          totalStorage,
			AllowedBytes:        allowedBytes(options.Limits.DiskBytes, freeStorage),
			FreeBytes:           freeStorage,
			UsedByModelsBytes:   storageUsage.ModelsBytes,
			UsedByRuntimesBytes: storageUsage.RuntimesBytes,
			UsedByCacheBytes:    storageUsage.TotalBytes,
			PartialModelBytes:   storageUsage.PartialModelBytes,
			PartialModelFiles:   storageUsage.PartialModelFiles,
			OrphanModelBytes:    storageUsage.OrphanModelBytes,
			OrphanModelDirs:     storageUsage.OrphanModelDirs,
		},
		JobSlots: allowedInt(options.Limits.JobSlots, 1),
		Models:   DiscoverInstalledModels(options.CacheDir),
		Runtimes: DiscoverRuntimes(options.CacheDir),
	}
}

func LlamaCPPRPCStatePath(cacheDir string) string {
	return filepath.Join(cacheDir, "runtimes", "llama.cpp-rpc.json")
}

func WriteLlamaCPPRPCState(cacheDir string, state LlamaCPPRPCState) error {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return nil
	}
	state.Endpoint = strings.TrimSpace(state.Endpoint)
	state.BindEndpoint = strings.TrimSpace(state.BindEndpoint)
	state.AdvertiseEndpoint = strings.TrimSpace(state.AdvertiseEndpoint)
	if state.AdvertiseEndpoint == "" {
		state.AdvertiseEndpoint = state.Endpoint
	}
	if state.Endpoint == "" {
		state.Endpoint = state.AdvertiseEndpoint
	}
	state.UpdatedAt = time.Now().UTC()
	path := LlamaCPPRPCStatePath(cacheDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return os.WriteFile(path, body, 0o600)
}

func ClearLlamaCPPRPCState(cacheDir string) error {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return nil
	}
	err := os.Remove(LlamaCPPRPCStatePath(cacheDir))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func ReadLlamaCPPRPCState(cacheDir string) (LlamaCPPRPCState, bool) {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return LlamaCPPRPCState{}, false
	}
	body, err := os.ReadFile(LlamaCPPRPCStatePath(cacheDir))
	if err != nil {
		return LlamaCPPRPCState{}, false
	}
	var state LlamaCPPRPCState
	if err := json.Unmarshal(body, &state); err != nil {
		return LlamaCPPRPCState{}, false
	}
	state.Endpoint = strings.TrimSpace(state.Endpoint)
	state.BindEndpoint = strings.TrimSpace(state.BindEndpoint)
	state.AdvertiseEndpoint = strings.TrimSpace(state.AdvertiseEndpoint)
	if state.AdvertiseEndpoint == "" {
		state.AdvertiseEndpoint = state.Endpoint
	}
	if state.Endpoint == "" {
		state.Endpoint = state.AdvertiseEndpoint
	}
	if !state.Running || state.Endpoint == "" {
		return LlamaCPPRPCState{}, false
	}
	return state, true
}

type CMeshStorageUsage struct {
	ModelsBytes       uint64
	RuntimesBytes     uint64
	TotalBytes        uint64
	PartialModelBytes uint64
	PartialModelFiles int
	OrphanModelBytes  uint64
	OrphanModelDirs   int
}

func discoverCMeshStorageUsage(cacheDir string) CMeshStorageUsage {
	cacheDir = strings.TrimSpace(cacheDir)
	if cacheDir == "" {
		return CMeshStorageUsage{}
	}
	modelsBytes := directorySize(filepath.Join(cacheDir, "models"))
	runtimesBytes := directorySize(filepath.Join(cacheDir, "runtimes"))
	totalBytes := directorySize(cacheDir)
	partialBytes, partialFiles := discoverPartialModelDownloads(filepath.Join(cacheDir, "models"))
	orphanBytes, orphanDirs := discoverOrphanModelDirs(filepath.Join(cacheDir, "models"))
	return CMeshStorageUsage{
		ModelsBytes:       modelsBytes,
		RuntimesBytes:     runtimesBytes,
		TotalBytes:        totalBytes,
		PartialModelBytes: partialBytes,
		PartialModelFiles: partialFiles,
		OrphanModelBytes:  orphanBytes,
		OrphanModelDirs:   orphanDirs,
	}
}

func discoverPartialModelDownloads(modelsDir string) (uint64, int) {
	var total uint64
	count := 0
	_ = filepath.WalkDir(modelsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tmp") {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 {
			return nil
		}
		total += uint64(info.Size())
		count++
		_ = path
		return nil
	})
	return total, count
}

func discoverOrphanModelDirs(modelsDir string) (uint64, int) {
	entries, err := os.ReadDir(modelsDir)
	if err != nil {
		return 0, 0
	}
	catalogIDs := map[string]bool{}
	for _, model := range models.Catalog() {
		catalogIDs[model.ID] = true
	}
	var total uint64
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() || catalogIDs[entry.Name()] {
			continue
		}
		count++
		total += directorySize(filepath.Join(modelsDir, entry.Name()))
	}
	return total, count
}

func directorySize(path string) uint64 {
	var total uint64
	err := filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 {
			return nil
		}
		total += uint64(info.Size())
		return nil
	})
	if err != nil {
		return 0
	}
	return total
}

func DiscoverRuntimes(cacheDir string) []cluster.RuntimeResource {
	status := runtimes.LlamaCPPStatus(cacheDir)
	return []cluster.RuntimeResource{{
		Name:          status.Name,
		Ready:         status.Ready,
		Version:       status.Version,
		Platform:      status.Platform,
		BinaryPath:    status.BinaryPath,
		Source:        status.Source,
		Capabilities:  runtimeCapabilities(status, cacheDir),
		RPCRuntimes:   runtimeRPCRuntimes(status, cacheDir),
		StageRuntimes: runtimeStageRuntimes(status),
		Error:         status.Error,
	}}
}

func runtimeCapabilities(status runtimes.RuntimeStatus, cacheDir string) []string {
	if !status.Ready {
		return nil
	}
	if status.Name == runtimes.LlamaCPPName {
		capabilities := runtimes.LogicalStageCapabilities()
		if state, ok := ReadLlamaCPPRPCState(cacheDir); ok && runtimes.NewLlamaCPPRPCRuntime(status.BinaryPath, state.Endpoint).Probe(context.Background()).Ready {
			capabilities = append(capabilities, runtimes.CapabilityLlamaCPPRPCClient, runtimes.CapabilityLlamaCPPRPCBackend)
		}
		return capabilities
	}
	return nil
}

func runtimeRPCRuntimes(status runtimes.RuntimeStatus, cacheDir string) []cluster.RPCRuntimeResource {
	if status.Name != runtimes.LlamaCPPName {
		return nil
	}
	endpoint := ""
	state, active := ReadLlamaCPPRPCState(cacheDir)
	if active {
		endpoint = state.Endpoint
	}
	probe := runtimes.NewLlamaCPPRPCRuntime(status.BinaryPath, endpoint).Probe(context.Background())
	ready := active && probe.Ready
	blockers := append([]string(nil), probe.Blockers...)
	if probe.Ready && !active {
		blockers = append(blockers, "llama.cpp rpc-server is not running")
	}
	return []cluster.RPCRuntimeResource{{
		Name:       probe.Name,
		Ready:      ready,
		ServerPath: probe.ServerPath,
		Endpoint:   probe.Endpoint,
		Protocol:   probe.Protocol,
		Blockers:   blockers,
	}}
}

func runtimeStageRuntimes(status runtimes.RuntimeStatus) []cluster.StageRuntimeResource {
	if status.Name != runtimes.LlamaCPPName {
		return nil
	}
	probe := runtimes.NewLlamaCPPStageRuntime(status.BinaryPath).Probe(context.Background())
	return []cluster.StageRuntimeResource{{
		Name:          probe.Name,
		Ready:         probe.Ready,
		CLIReady:      probe.CLIReady,
		BinaryPath:    probe.BinaryPath,
		RequiredHooks: append([]string(nil), probe.RequiredHooks...),
		Blockers:      append([]string(nil), probe.Blockers...),
	}}
}

func DiscoverInstalledModels(cacheDir string) []cluster.ModelResource {
	if strings.TrimSpace(cacheDir) == "" {
		return nil
	}
	out := make([]cluster.ModelResource, 0)
	for _, model := range models.Catalog() {
		path := ModelFilePath(cacheDir, model)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() <= 0 {
			continue
		}
		resource := cluster.ModelResource{
			ID:      model.ID,
			Name:    model.Name,
			Family:  model.Family,
			Runtime: string(model.Runtime),
			Path:    path,
			Bytes:   uint64(info.Size()),
			Ready:   true,
		}
		if manifest, ok := readModelManifest(cacheDir, model.ID); ok {
			if strings.TrimSpace(manifest.ID) == model.ID {
				resource.Name = firstNonEmpty(manifest.Name, resource.Name)
				resource.Family = firstNonEmpty(manifest.Family, resource.Family)
				resource.Runtime = firstNonEmpty(manifest.Runtime, resource.Runtime)
				resource.InstalledAt = manifest.InstalledAt
				resource.Layers = manifest.Layers
				if manifest.Bytes > 0 {
					resource.Bytes = manifest.Bytes
				}
				if manifest.File != "" && manifest.File != model.File {
					resource.Error = "manifest file does not match catalog"
				}
				if manifest.Bytes > 0 && manifest.Bytes != uint64(info.Size()) {
					resource.Error = "manifest size does not match model file"
					resource.Bytes = uint64(info.Size())
				}
			} else {
				resource.Error = "manifest id does not match catalog"
			}
		} else {
			resource.Error = "manifest missing"
		}
		out = append(out, cluster.ModelResource{
			ID:          resource.ID,
			Name:        resource.Name,
			Family:      resource.Family,
			Runtime:     resource.Runtime,
			Path:        resource.Path,
			Bytes:       resource.Bytes,
			Layers:      resource.Layers,
			Ready:       resource.Ready,
			Error:       resource.Error,
			InstalledAt: resource.InstalledAt,
		})
	}
	return out
}

const modelManifestFile = "cmesh-model.json"

type ModelManifest struct {
	Schema      string    `json:"schema"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Family      string    `json:"family,omitempty"`
	Runtime     string    `json:"runtime"`
	Repo        string    `json:"repo,omitempty"`
	File        string    `json:"file"`
	URL         string    `json:"url,omitempty"`
	Path        string    `json:"path"`
	Bytes       uint64    `json:"bytes"`
	Layers      int       `json:"layers,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

func ModelFilePath(cacheDir string, model models.Model) string {
	return filepath.Join(cacheDir, "models", model.ID, model.File)
}

func WriteModelManifest(cacheDir string, model models.Model, path string, bytes uint64, installedAt time.Time) error {
	return WriteModelManifestWithLayers(cacheDir, model, path, bytes, installedAt, 0)
}

func WriteModelManifestWithLayers(cacheDir string, model models.Model, path string, bytes uint64, installedAt time.Time, layers int) error {
	if installedAt.IsZero() {
		installedAt = time.Now().UTC()
	}
	if layers <= 0 {
		layers = modelLayerEstimate(model)
	}
	manifest := ModelManifest{
		Schema:      "cmesh.model.v1",
		ID:          model.ID,
		Name:        model.Name,
		Family:      model.Family,
		Runtime:     string(model.Runtime),
		Repo:        model.Repo,
		File:        model.File,
		URL:         model.URL,
		Path:        path,
		Bytes:       bytes,
		Layers:      layers,
		InstalledAt: installedAt.UTC(),
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(ModelManifestPath(cacheDir, model.ID)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(ModelManifestPath(cacheDir, model.ID), append(body, '\n'), 0o644)
}

func modelLayerEstimate(model models.Model) int {
	params := strings.ToUpper(strings.TrimSpace(model.Parameters))
	params = strings.TrimSuffix(params, "B")
	value, err := strconv.ParseFloat(params, 64)
	if err != nil || value <= 0 {
		return 0
	}
	switch {
	case value <= 1.5:
		return 24
	case value <= 4:
		return 32
	case value <= 8:
		return 32
	case value <= 15:
		return 48
	case value <= 28:
		return 56
	default:
		return 64
	}
}

func ModelManifestPath(cacheDir string, modelID string) string {
	return filepath.Join(cacheDir, "models", modelID, modelManifestFile)
}

func readModelManifest(cacheDir string, modelID string) (ModelManifest, bool) {
	body, err := os.ReadFile(ModelManifestPath(cacheDir, modelID))
	if err != nil {
		return ModelManifest{}, false
	}
	var manifest ModelManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return ModelManifest{}, false
	}
	return manifest, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func discoverTotalMemory() uint64 {
	switch runtime.GOOS {
	case "darwin":
		return sysctlUint64("hw.memsize")
	case "linux":
		return linuxMemTotal()
	default:
		return 0
	}
}

func sysctlUint64(key string) uint64 {
	output, err := exec.Command("sysctl", "-n", key).Output()
	if err != nil {
		return 0
	}

	value, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 0
	}

	return value
}

func linuxMemTotal() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}

		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}

		return kb * 1024
	}

	return 0
}

func discoverGPUs(limits config.ResourceLimits) []cluster.GPUResources {
	if !limits.GPUEnabled {
		return nil
	}

	if runtime.GOOS == "darwin" {
		if name := darwinGPUName(); name != "" {
			return []cluster.GPUResources{
				{
					Name:              name,
					Vendor:            "apple",
					AllowedVRAMBytes:  limits.VRAMBytes,
					ComputeCompatible: true,
				},
			}
		}
	}

	return nil
}

func darwinGPUName() string {
	output, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		return ""
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Chipset Model:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Chipset Model:"))
		}
	}

	return ""
}

func allowedInt(configured int, total int) int {
	if configured <= 0 {
		return total
	}
	if total > 0 && configured > total {
		return total
	}
	return configured
}

func allowedBytes(configured uint64, total uint64) uint64 {
	if configured == 0 {
		return total
	}
	if total > 0 && configured > total {
		return total
	}
	return configured
}
