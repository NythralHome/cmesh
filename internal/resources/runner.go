package resources

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

type BenchmarkOptions struct {
	NodeID   string
	CacheDir string
}

func RunLocalBenchmarks(options BenchmarkOptions) ([]BenchmarkResult, error) {
	if options.CacheDir != "" {
		if err := os.MkdirAll(options.CacheDir, 0o755); err != nil {
			return nil, err
		}
	}

	results := make([]BenchmarkResult, 0, 3)
	now := time.Now().UTC()

	cpu := runCPUBenchmark(options.NodeID)
	cpu.CreatedAt = now
	results = append(results, cpu)

	memory := runMemoryBenchmark(options.NodeID)
	memory.CreatedAt = now
	results = append(results, memory)

	disk, err := runDiskBenchmark(options.NodeID, options.CacheDir)
	if err != nil {
		return nil, err
	}
	disk.CreatedAt = now
	results = append(results, disk)

	return results, nil
}

func runCPUBenchmark(nodeID string) BenchmarkResult {
	start := time.Now()
	deadline := start.Add(750 * time.Millisecond)
	var iterations uint64
	var value uint64 = 1469598103934665603

	for time.Now().Before(deadline) {
		for i := 0; i < 1024; i++ {
			value ^= uint64(i) + iterations
			value *= 1099511628211
		}
		iterations += 1024
	}

	duration := time.Since(start)
	score := float64(iterations) / duration.Seconds() / 1_000_000
	return BenchmarkResult{
		NodeID:   nodeID,
		Kind:     BenchmarkCPU,
		Score:    score,
		Unit:     "million_ops_per_second",
		Duration: duration,
		Metadata: map[string]string{
			"threads": strconv.Itoa(runtime.GOMAXPROCS(0)),
			"sink":    strconv.FormatUint(value, 10),
		},
	}
}

func runMemoryBenchmark(nodeID string) BenchmarkResult {
	size := 64 * 1024 * 1024
	src := make([]byte, size)
	dst := make([]byte, size)
	_, _ = rand.Read(src)

	start := time.Now()
	deadline := start.Add(500 * time.Millisecond)
	var bytesCopied uint64

	for time.Now().Before(deadline) {
		copy(dst, src)
		bytesCopied += uint64(size)
	}

	duration := time.Since(start)
	score := float64(bytesCopied) / duration.Seconds() / 1024 / 1024 / 1024
	return BenchmarkResult{
		NodeID:   nodeID,
		Kind:     BenchmarkMemory,
		Score:    score,
		Unit:     "gb_per_second",
		Duration: duration,
	}
}

func runDiskBenchmark(nodeID string, cacheDir string) (BenchmarkResult, error) {
	if cacheDir == "" {
		cacheDir = "."
	}

	path := filepath.Join(cacheDir, "benchmark.tmp")
	payload := make([]byte, 16*1024*1024)
	_, _ = rand.Read(payload)

	start := time.Now()
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return BenchmarkResult{}, err
	}
	if _, err := os.ReadFile(path); err != nil {
		return BenchmarkResult{}, err
	}
	_ = os.Remove(path)

	duration := time.Since(start)
	score := float64(len(payload)*2) / duration.Seconds() / 1024 / 1024
	return BenchmarkResult{
		NodeID:   nodeID,
		Kind:     BenchmarkDisk,
		Score:    score,
		Unit:     "mb_per_second",
		Duration: duration,
	}, nil
}
