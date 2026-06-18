package manager

import (
	"time"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/resources"
)

type RPCHealthUpdate struct {
	Endpoint  string
	NodeID    string
	NodeName  string
	Ready     bool
	LatencyMS int64
	Error     string
	CheckedAt time.Time
}

type RPCHealthRecord struct {
	Endpoint            string    `json:"endpoint"`
	NodeID              string    `json:"node_id,omitempty"`
	NodeName            string    `json:"node_name,omitempty"`
	Ready               bool      `json:"ready"`
	Successes           int       `json:"successes"`
	Failures            int       `json:"failures"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastLatencyMS       int64     `json:"last_latency_ms,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type Store interface {
	RegisterWorker(req membership.JoinRequest) membership.JoinResponse
	Heartbeat(hb membership.Heartbeat) bool
	MarkWorkerOffline(nodeID string) bool
	PutBenchmark(result resources.BenchmarkResult) bool
	Benchmarks() []resources.BenchmarkResult
	BenchmarkSummaryByNode() map[string]NodeBenchmarkSummary
	PutRPCHealth(update RPCHealthUpdate) RPCHealthRecord
	RPCHealth() []RPCHealthRecord
	CreateJob(req jobs.CreateRequest) (jobs.Job, error)
	CreateJobsBatch(requests []jobs.CreateRequest) ([]jobs.Job, error)
	Jobs() []jobs.Job
	Job(id string) (jobs.Job, bool)
	NextJobForWorker(nodeID string) (jobs.Job, bool)
	UpdateJobProgress(jobID string, req jobs.ProgressRequest) (jobs.Job, bool)
	UpdateCDIPStageState(jobID string, next cdip.StageState, detail string) (jobs.Job, bool)
	CompleteCoordinatorJob(jobID string, result string, errText string) (jobs.Job, bool)
	CompleteJob(jobID string, req jobs.CompleteRequest) (jobs.Job, bool)
	CancelJob(jobID string) (jobs.Job, bool)
	Nodes() []cluster.Node
	ClusterSummary() ClusterSummary
}

var _ Store = (*State)(nil)
var _ Store = (*FileStore)(nil)
var _ Store = (*PostgresStore)(nil)
