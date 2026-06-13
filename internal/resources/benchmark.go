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
	NodeID    string            `json:"node_id"`
	Kind      BenchmarkKind     `json:"kind"`
	Score     float64           `json:"score"`
	Unit      string            `json:"unit"`
	Duration  time.Duration     `json:"duration"`
	CreatedAt time.Time         `json:"created_at"`
	Metadata  map[string]string `json:"metadata"`
}
