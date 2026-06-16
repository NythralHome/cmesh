package resources

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/config"
	"github.com/cmesh/cmesh/internal/models"
)

type DiscoveryOptions struct {
	Limits   config.ResourceLimits
	CacheDir string
}

func DiscoverLocal(options DiscoveryOptions) cluster.ResourceSnapshot {
	if options.CacheDir != "" {
		_ = os.MkdirAll(options.CacheDir, 0o755)
	}

	totalMemory := discoverTotalMemory()
	totalStorage, freeStorage := discoverDisk(options.CacheDir)

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
			TotalBytes:   totalStorage,
			AllowedBytes: allowedBytes(options.Limits.DiskBytes, freeStorage),
			FreeBytes:    freeStorage,
		},
		JobSlots: allowedInt(options.Limits.JobSlots, 1),
		Models:   DiscoverInstalledModels(options.CacheDir),
	}
}

func DiscoverInstalledModels(cacheDir string) []cluster.ModelResource {
	if strings.TrimSpace(cacheDir) == "" {
		return nil
	}
	out := make([]cluster.ModelResource, 0)
	for _, model := range models.Catalog() {
		path := filepath.Join(cacheDir, "models", model.ID, model.File)
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() <= 0 {
			continue
		}
		out = append(out, cluster.ModelResource{
			ID:    model.ID,
			Name:  model.Name,
			Path:  path,
			Bytes: uint64(info.Size()),
		})
	}
	return out
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
