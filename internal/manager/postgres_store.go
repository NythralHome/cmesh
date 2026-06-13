package manager

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool             *pgxpool.Pool
	startedAt        time.Time
	heartbeatTimeout time.Duration
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}

	store := &PostgresStore{
		pool:             pool,
		startedAt:        time.Now().UTC(),
		heartbeatTimeout: 30 * time.Second,
	}
	if err := store.Migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}

	return store, nil
}

func (s *PostgresStore) Close() {
	s.pool.Close()
}

func (s *PostgresStore) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  role TEXT NOT NULL,
  status TEXT NOT NULL,
  endpoint TEXT NOT NULL DEFAULT '',
  resources JSONB NOT NULL,
  joined_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS benchmarks (
  node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  score DOUBLE PRECISION NOT NULL,
  unit TEXT NOT NULL,
  duration BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL,
  metadata JSONB,
  PRIMARY KEY (node_id, kind)
);

CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  status TEXT NOT NULL,
  requested_by TEXT NOT NULL DEFAULT '',
  assigned_to TEXT NOT NULL DEFAULT '',
  input TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL,
  started_at TIMESTAMPTZ,
  finished_at TIMESTAMPTZ
);
`)
	return err
}

func (s *PostgresStore) RegisterWorker(req membership.JoinRequest) membership.JoinResponse {
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

	payload, _ := json.Marshal(node.Resources)
	_, _ = s.pool.Exec(context.Background(), `
INSERT INTO nodes (id, name, role, status, endpoint, resources, joined_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
`, node.ID, node.Name, string(node.Role), string(node.Status), node.Endpoint, payload, node.JoinedAt, node.UpdatedAt)

	return membership.JoinResponse{
		NodeID:         nodeID,
		ManagerPeers:   []string{"http://127.0.0.1:8080"},
		HeartbeatEvery: 10 * time.Second,
	}
}

func (s *PostgresStore) Heartbeat(hb membership.Heartbeat) bool {
	payload, _ := json.Marshal(hb.Resources)
	tag, err := s.pool.Exec(context.Background(), `
UPDATE nodes
SET status = $2, resources = $3, updated_at = $4
WHERE id = $1
`, hb.NodeID, string(cluster.NodeStatusOnline), payload, time.Now().UTC())
	return err == nil && tag.RowsAffected() > 0
}

func (s *PostgresStore) PutBenchmark(result resources.BenchmarkResult) bool {
	if result.CreatedAt.IsZero() {
		result.CreatedAt = time.Now().UTC()
	}

	metadata, _ := json.Marshal(result.Metadata)
	tag, err := s.pool.Exec(context.Background(), `
INSERT INTO benchmarks (node_id, kind, score, unit, duration, created_at, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (node_id, kind) DO UPDATE SET
  score = EXCLUDED.score,
  unit = EXCLUDED.unit,
  duration = EXCLUDED.duration,
  created_at = EXCLUDED.created_at,
  metadata = EXCLUDED.metadata
`, result.NodeID, string(result.Kind), result.Score, result.Unit, int64(result.Duration), result.CreatedAt, metadata)
	return err == nil && tag.RowsAffected() > 0
}

func (s *PostgresStore) Benchmarks() []resources.BenchmarkResult {
	rows, err := s.pool.Query(context.Background(), `
SELECT node_id, kind, score, unit, duration, created_at, metadata
FROM benchmarks
ORDER BY created_at DESC
`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []resources.BenchmarkResult
	for rows.Next() {
		var result resources.BenchmarkResult
		var kind string
		var duration int64
		var metadata []byte
		if err := rows.Scan(&result.NodeID, &kind, &result.Score, &result.Unit, &duration, &result.CreatedAt, &metadata); err != nil {
			continue
		}
		result.Kind = resources.BenchmarkKind(kind)
		result.Duration = time.Duration(duration)
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &result.Metadata)
		}
		results = append(results, result)
	}
	return results
}

func (s *PostgresStore) BenchmarkSummaryByNode() map[string]NodeBenchmarkSummary {
	summaries := make(map[string]NodeBenchmarkSummary)
	for _, result := range s.Benchmarks() {
		summary := summaries[result.NodeID]
		if summary.Results == nil {
			summary.NodeID = result.NodeID
			summary.Results = make(map[resources.BenchmarkKind]resources.BenchmarkResult)
		}
		summary.Results[result.Kind] = result
		summary.TotalScore += result.Score
		summaries[result.NodeID] = summary
	}
	return summaries
}

func (s *PostgresStore) CreateJob(req jobs.CreateRequest) (jobs.Job, error) {
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
	if workerID := s.pickWorker(); workerID != "" {
		job.AssignedTo = workerID
		job.Status = jobs.StatusScheduled
	}

	_, err := s.pool.Exec(context.Background(), `
INSERT INTO jobs (id, type, status, requested_by, assigned_to, input, result, error, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`, job.ID, job.Type, string(job.Status), job.RequestedBy, job.AssignedTo, job.Input, job.Result, job.Error, job.CreatedAt, job.UpdatedAt)
	return job, err
}

func (s *PostgresStore) Jobs() []jobs.Job {
	rows, err := s.pool.Query(context.Background(), `
SELECT id, type, status, requested_by, assigned_to, input, result, error, created_at, updated_at, started_at, finished_at
FROM jobs
ORDER BY created_at DESC
`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []jobs.Job
	for rows.Next() {
		job, ok := scanJob(rows)
		if ok {
			out = append(out, job)
		}
	}
	return out
}

func (s *PostgresStore) Job(id string) (jobs.Job, bool) {
	row := s.pool.QueryRow(context.Background(), `
SELECT id, type, status, requested_by, assigned_to, input, result, error, created_at, updated_at, started_at, finished_at
FROM jobs
WHERE id = $1
`, id)
	return scanJob(row)
}

func (s *PostgresStore) NextJobForWorker(nodeID string) (jobs.Job, bool) {
	now := time.Now().UTC()
	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET status = $3, started_at = $4, updated_at = $4
WHERE id = (
  SELECT id FROM jobs
  WHERE assigned_to = $1 AND status = $2
  ORDER BY created_at ASC
  LIMIT 1
)
RETURNING id, type, status, requested_by, assigned_to, input, result, error, created_at, updated_at, started_at, finished_at
`, nodeID, string(jobs.StatusScheduled), string(jobs.StatusRunning), now)
	return scanJob(row)
}

func (s *PostgresStore) CompleteJob(jobID string, req jobs.CompleteRequest) (jobs.Job, bool) {
	now := time.Now().UTC()
	status := jobs.StatusSucceeded
	if req.Error != "" {
		status = jobs.StatusFailed
	}

	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET status = $3, result = $4, error = $5, finished_at = $6, updated_at = $6
WHERE id = $1 AND assigned_to = $2
RETURNING id, type, status, requested_by, assigned_to, input, result, error, created_at, updated_at, started_at, finished_at
`, jobID, req.NodeID, string(status), req.Result, req.Error, now)
	return scanJob(row)
}

func (s *PostgresStore) Nodes() []cluster.Node {
	rows, err := s.pool.Query(context.Background(), `
SELECT id, name, role, status, endpoint, resources, joined_at, updated_at
FROM nodes
ORDER BY joined_at ASC
`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var nodes []cluster.Node
	now := time.Now().UTC()
	for rows.Next() {
		var node cluster.Node
		var role, status string
		var payload []byte
		if err := rows.Scan(&node.ID, &node.Name, &role, &status, &node.Endpoint, &payload, &node.JoinedAt, &node.UpdatedAt); err != nil {
			continue
		}
		node.Role = cluster.NodeRole(role)
		node.Status = cluster.NodeStatus(status)
		_ = json.Unmarshal(payload, &node.Resources)
		nodes = append(nodes, s.deriveNodeStatus(node, now))
	}
	return nodes
}

func (s *PostgresStore) ClusterSummary() ClusterSummary {
	nodes := s.Nodes()
	benchmarkSummary := s.BenchmarkSummaryByNode()
	summary := ClusterSummary{StartedAt: s.startedAt}
	for _, node := range nodes {
		if node.Role == cluster.NodeRoleWorker {
			summary.WorkersTotal++
			if node.Status == cluster.NodeStatusOnline {
				summary.WorkersOnline++
			}
		}
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
	return summary
}

func (s *PostgresStore) deriveNodeStatus(node cluster.Node, now time.Time) cluster.Node {
	if node.Role == cluster.NodeRoleWorker &&
		node.Status == cluster.NodeStatusOnline &&
		now.Sub(node.UpdatedAt) > s.heartbeatTimeout {
		node.Status = cluster.NodeStatusOffline
	}
	return node
}

func (s *PostgresStore) pickWorker() string {
	nodes := s.Nodes()
	benchmarks := s.BenchmarkSummaryByNode()
	var bestID string
	var bestScore float64
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		score := benchmarks[node.ID].TotalScore
		if bestID == "" || score > bestScore {
			bestID = node.ID
			bestScore = score
		}
	}
	return bestID
}

type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(row jobScanner) (jobs.Job, bool) {
	var job jobs.Job
	var status string
	var startedAt *time.Time
	var finishedAt *time.Time
	if err := row.Scan(
		&job.ID,
		&job.Type,
		&status,
		&job.RequestedBy,
		&job.AssignedTo,
		&job.Input,
		&job.Result,
		&job.Error,
		&job.CreatedAt,
		&job.UpdatedAt,
		&startedAt,
		&finishedAt,
	); err != nil {
		return jobs.Job{}, false
	}
	job.Status = jobs.Status(status)
	if startedAt != nil {
		job.StartedAt = *startedAt
	}
	if finishedAt != nil {
		job.FinishedAt = *finishedAt
	}
	return job, true
}
