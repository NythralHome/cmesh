package manager

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/resources"
)

type State struct {
	mu               sync.RWMutex
	startedAt        time.Time
	heartbeatTimeout time.Duration
	nodes            map[string]cluster.Node
	benchmarks       map[string]map[resources.BenchmarkKind]resources.BenchmarkResult
	jobs             map[string]jobs.Job
}

func NewState() *State {
	return &State{
		startedAt:        time.Now().UTC(),
		heartbeatTimeout: 30 * time.Second,
		nodes:            make(map[string]cluster.Node),
		benchmarks:       make(map[string]map[resources.BenchmarkKind]resources.BenchmarkResult),
		jobs:             make(map[string]jobs.Job),
	}
}

func (s *State) RegisterWorker(req membership.JoinRequest) membership.JoinResponse {
	now := time.Now().UTC()
	nodeID := newNodeID()

	node := cluster.Node{
		ID:        nodeID,
		Name:      req.NodeName,
		Role:      cluster.NodeRoleWorker,
		Status:    cluster.NodeStatusOnline,
		Endpoint:  req.Endpoint,
		Resources: req.Resources,
		JoinedAt:  now,
		UpdatedAt: now,
	}

	s.mu.Lock()
	s.nodes[nodeID] = node
	s.mu.Unlock()

	return membership.JoinResponse{
		NodeID:         nodeID,
		ManagerPeers:   []string{"http://127.0.0.1:8080"},
		HeartbeatEvery: 10 * time.Second,
	}
}

func (s *State) Heartbeat(hb membership.Heartbeat) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	node, ok := s.nodes[hb.NodeID]
	if !ok {
		return false
	}

	node.Status = cluster.NodeStatusOnline
	node.Resources = hb.Resources
	node.UpdatedAt = time.Now().UTC()
	s.nodes[hb.NodeID] = node
	return true
}

func (s *State) MarkWorkerOffline(nodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markWorkerOfflineLocked(nodeID, time.Now().UTC(), "worker went offline")
}

func (s *State) markWorkerOfflineLocked(nodeID string, now time.Time, reason string) bool {
	node, ok := s.nodes[nodeID]
	if !ok {
		return false
	}

	node.Status = cluster.NodeStatusOffline
	node.UpdatedAt = now
	s.nodes[nodeID] = node
	s.failActiveJobsForWorkerLocked(nodeID, now, reason)
	return true
}

func (s *State) failActiveJobsForWorkerLocked(nodeID string, now time.Time, reason string) {
	for id, job := range s.jobs {
		if job.AssignedTo != nodeID {
			continue
		}
		if job.Status != jobs.StatusScheduled && job.Status != jobs.StatusRunning {
			continue
		}
		job.Status = jobs.StatusFailed
		job.Error = reason
		job.FinishedAt = now
		job.UpdatedAt = now
		s.jobs[id] = job
	}
}

func (s *State) PutBenchmark(result resources.BenchmarkResult) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.nodes[result.NodeID]; !ok {
		return false
	}

	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now().UTC()
	}

	if s.benchmarks[result.NodeID] == nil {
		s.benchmarks[result.NodeID] = make(map[resources.BenchmarkKind]resources.BenchmarkResult)
	}
	s.benchmarks[result.NodeID][result.Kind] = result
	return true
}

func (s *State) Benchmarks() []resources.BenchmarkResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	results := make([]resources.BenchmarkResult, 0)
	for _, byKind := range s.benchmarks {
		for _, result := range byKind {
			results = append(results, result)
		}
	}

	return results
}

func (s *State) BenchmarkSummaryByNode() map[string]NodeBenchmarkSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	summaries := make(map[string]NodeBenchmarkSummary, len(s.benchmarks))
	for nodeID, byKind := range s.benchmarks {
		summary := NodeBenchmarkSummary{
			NodeID:  nodeID,
			Results: make(map[resources.BenchmarkKind]resources.BenchmarkResult, len(byKind)),
		}
		for kind, result := range byKind {
			summary.Results[kind] = result
			summary.TotalScore += result.Score
		}
		summaries[nodeID] = summary
	}

	return summaries
}

func (s *State) CreateJob(req jobs.CreateRequest) (jobs.Job, error) {
	now := time.Now().UTC()
	job := jobs.Job{
		ID:          newJobID(),
		Type:        req.Type,
		Status:      jobs.StatusQueued,
		RequestedBy: req.RequestedBy,
		Input:       req.Input,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if req.AssignedTo != "" {
		job.AssignedTo = req.AssignedTo
		job.Status = jobs.StatusScheduled
	} else if workerID := s.pickWorkerLocked(); workerID != "" {
		job.AssignedTo = workerID
		job.Status = jobs.StatusScheduled
	}

	s.jobs[job.ID] = job
	return job, nil
}

func (s *State) Jobs() []jobs.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]jobs.Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		out = append(out, job)
	}
	return out
}

func (s *State) Job(id string) (jobs.Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	job, ok := s.jobs[id]
	return job, ok
}

func (s *State) NextJobForWorker(nodeID string) (jobs.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	for _, job := range s.jobs {
		if job.AssignedTo != nodeID || job.Status != jobs.StatusScheduled {
			continue
		}

		job.Status = jobs.StatusRunning
		job.StartedAt = now
		job.UpdatedAt = now
		s.jobs[job.ID] = job
		return job, true
	}

	return jobs.Job{}, false
}

func (s *State) CompleteJob(jobID string, req jobs.CompleteRequest) (jobs.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok || job.AssignedTo != req.NodeID || job.Status != jobs.StatusRunning {
		return jobs.Job{}, false
	}

	now := time.Now().UTC()
	job.Result = req.Result
	job.Error = req.Error
	job.FinishedAt = now
	job.UpdatedAt = now
	if req.Error != "" {
		job.Status = jobs.StatusFailed
	} else {
		job.Status = jobs.StatusSucceeded
	}
	s.jobs[job.ID] = job

	return job, true
}

func (s *State) pickWorkerLocked() string {
	now := time.Now().UTC()
	var bestID string
	var bestScore float64

	for _, node := range s.nodes {
		node = s.deriveNodeStatus(node, now)
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}

		score := s.nodeBenchmarkScoreLocked(node.ID)
		if bestID == "" || score > bestScore {
			bestID = node.ID
			bestScore = score
		}
	}

	return bestID
}

func (s *State) nodeBenchmarkScoreLocked(nodeID string) float64 {
	var score float64
	for _, result := range s.benchmarks[nodeID] {
		score += result.Score
	}
	return score
}

func (s *State) Nodes() []cluster.Node {
	s.mu.Lock()
	defer s.mu.Unlock()

	nodes := make([]cluster.Node, 0, len(s.nodes))
	now := time.Now().UTC()
	for _, node := range s.nodes {
		previousStatus := node.Status
		node = s.deriveNodeStatus(node, now)
		if previousStatus == cluster.NodeStatusOnline && node.Status == cluster.NodeStatusOffline {
			node.UpdatedAt = now
			s.nodes[node.ID] = node
			s.failActiveJobsForWorkerLocked(node.ID, now, "worker heartbeat timed out")
		}
		nodes = append(nodes, node)
	}

	return nodes
}

func (s *State) deriveNodeStatus(node cluster.Node, now time.Time) cluster.Node {
	if node.Role == cluster.NodeRoleWorker &&
		node.Status == cluster.NodeStatusOnline &&
		now.Sub(node.UpdatedAt) > s.heartbeatTimeout {
		node.Status = cluster.NodeStatusOffline
	}

	return node
}

func (s *State) ClusterSummary() ClusterSummary {
	nodes := s.Nodes()
	benchmarkSummary := s.BenchmarkSummaryByNode()
	summary := ClusterSummary{
		StartedAt: s.startedAt,
	}

	for _, node := range nodes {
		if node.Role == cluster.NodeRoleWorker {
			summary.WorkersTotal++
			if node.Status == cluster.NodeStatusOnline {
				summary.WorkersOnline++
			}
		}

		if node.Role == cluster.NodeRoleWorker && node.Status == cluster.NodeStatusOnline {
			summary.Resources.CPU.CoresTotal += node.Resources.CPU.CoresTotal
			summary.Resources.CPU.CoresAllowed += node.Resources.CPU.CoresAllowed
			summary.Resources.Memory.TotalBytes += node.Resources.Memory.TotalBytes
			summary.Resources.Memory.AllowedBytes += node.Resources.Memory.AllowedBytes
			summary.Resources.Storage.TotalBytes += node.Resources.Storage.TotalBytes
			summary.Resources.Storage.AllowedBytes += node.Resources.Storage.AllowedBytes
			summary.Resources.Storage.FreeBytes += node.Resources.Storage.FreeBytes
			summary.GPUs += len(node.Resources.GPU)
			for _, gpu := range node.Resources.GPU {
				summary.VRAMTotalBytes += gpu.TotalVRAMBytes
				summary.VRAMAllowedBytes += gpu.AllowedVRAMBytes
			}
			summary.BenchmarkScore += benchmarkSummary[node.ID].TotalScore
		}
	}

	return summary
}

func newNodeID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "node-unknown"
	}
	return "node-" + hex.EncodeToString(buf[:])
}

func newJobID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "job-unknown"
	}
	return "job-" + hex.EncodeToString(buf[:])
}

func newClusterBenchmarkID() string {
	var buf [6]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "cb-unknown"
	}
	return "cb-" + hex.EncodeToString(buf[:])
}

type ClusterSummary struct {
	StartedAt        time.Time                `json:"started_at"`
	WorkersTotal     int                      `json:"workers_total"`
	WorkersOnline    int                      `json:"workers_online"`
	GPUs             int                      `json:"gpus"`
	VRAMTotalBytes   uint64                   `json:"vram_total_bytes"`
	VRAMAllowedBytes uint64                   `json:"vram_allowed_bytes"`
	BenchmarkScore   float64                  `json:"benchmark_score"`
	Resources        cluster.ResourceSnapshot `json:"resources"`
}

type NodeBenchmarkSummary struct {
	NodeID     string                                                `json:"node_id"`
	TotalScore float64                                               `json:"total_score"`
	Results    map[resources.BenchmarkKind]resources.BenchmarkResult `json:"results"`
}
