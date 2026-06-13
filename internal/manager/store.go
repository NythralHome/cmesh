package manager

import (
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/resources"
)

type Store interface {
	RegisterWorker(req membership.JoinRequest) membership.JoinResponse
	Heartbeat(hb membership.Heartbeat) bool
	PutBenchmark(result resources.BenchmarkResult) bool
	Benchmarks() []resources.BenchmarkResult
	BenchmarkSummaryByNode() map[string]NodeBenchmarkSummary
	CreateJob(req jobs.CreateRequest) (jobs.Job, error)
	Jobs() []jobs.Job
	Job(id string) (jobs.Job, bool)
	NextJobForWorker(nodeID string) (jobs.Job, bool)
	CompleteJob(jobID string, req jobs.CompleteRequest) (jobs.Job, bool)
	Nodes() []cluster.Node
	ClusterSummary() ClusterSummary
}

var _ Store = (*State)(nil)
var _ Store = (*PostgresStore)(nil)
