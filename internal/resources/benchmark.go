package resources

import "time"

type BenchmarkKind string

const (
	BenchmarkCPU     BenchmarkKind = "cpu"
	BenchmarkMemory  BenchmarkKind = "memory"
	BenchmarkDisk    BenchmarkKind = "disk"
	BenchmarkNetwork BenchmarkKind = "network"
	BenchmarkAI      BenchmarkKind = "ai"
)

type BenchmarkResult struct {
	NodeID    string
	Kind      BenchmarkKind
	Score     float64
	Unit      string
	Duration  time.Duration
	CreatedAt time.Time
	Metadata  map[string]string
}
