package manager

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/models"
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

CREATE TABLE IF NOT EXISTS rpc_health (
  endpoint TEXT PRIMARY KEY,
  node_id TEXT NOT NULL DEFAULT '',
  node_name TEXT NOT NULL DEFAULT '',
  ready BOOLEAN NOT NULL DEFAULT FALSE,
  successes INTEGER NOT NULL DEFAULT 0,
  failures INTEGER NOT NULL DEFAULT 0,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_latency_ms BIGINT NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  updated_at TIMESTAMPTZ NOT NULL
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

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS max_attempts INTEGER NOT NULL DEFAULT 3;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS last_failure TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS requirements JSONB NOT NULL DEFAULT '{}';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS cdip_state TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS cdip_parent_job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS cdip_stage_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS auth_token TEXT NOT NULL DEFAULT '';
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
		AuthToken: newWorkerAuthToken(),
		Resources: req.Resources,
		JoinedAt:  now,
		UpdatedAt: now,
	}

	payload, _ := json.Marshal(node.Resources)
	_, _ = s.pool.Exec(context.Background(), `
INSERT INTO nodes (id, name, role, status, endpoint, auth_token, resources, joined_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
`, node.ID, node.Name, string(node.Role), string(node.Status), node.Endpoint, node.AuthToken, payload, node.JoinedAt, node.UpdatedAt)
	s.scheduleQueuedJobs(now)

	return membership.JoinResponse{
		NodeID:         nodeID,
		NodeAuthToken:  node.AuthToken,
		ManagerPeers:   []string{"http://127.0.0.1:8080"},
		HeartbeatEvery: 10 * time.Second,
	}
}

func (s *PostgresStore) Heartbeat(hb membership.Heartbeat) bool {
	payload, _ := json.Marshal(hb.Resources)
	now := time.Now().UTC()
	tag, err := s.pool.Exec(context.Background(), `
UPDATE nodes
SET status = $2, resources = $3, updated_at = $4
WHERE id = $1
`, hb.NodeID, string(cluster.NodeStatusOnline), payload, now)
	ok := err == nil && tag.RowsAffected() > 0
	if ok {
		s.scheduleQueuedJobs(now)
	}
	return ok
}

func (s *PostgresStore) MarkWorkerOffline(nodeID string) bool {
	now := time.Now().UTC()
	tag, err := s.pool.Exec(context.Background(), `
UPDATE nodes
SET status = $2, updated_at = $3
WHERE id = $1
`, nodeID, string(cluster.NodeStatusOffline), now)
	if err != nil || tag.RowsAffected() == 0 {
		return false
	}
	s.failActiveJobsForWorker(nodeID, now, "worker went offline")
	return true
}

func (s *PostgresStore) failActiveJobsForWorker(nodeID string, now time.Time, reason string) {
	rows, err := s.pool.Query(context.Background(), `
SELECT id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
FROM jobs
WHERE assigned_to = $1 AND status IN ($2, $3)
`, nodeID, string(jobs.StatusScheduled), string(jobs.StatusRunning))
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		job, ok := scanJob(rows)
		if !ok {
			continue
		}
		job = s.rescheduleOrFailJob(job, nodeID, now, reason)
		_, _ = s.pool.Exec(context.Background(), `
UPDATE jobs
SET status = $2,
    assigned_to = $3,
    result = $4,
    error = $5,
    attempts = $6,
    max_attempts = $7,
    last_failure = $8,
    updated_at = $9,
    started_at = $10,
    finished_at = $11
WHERE id = $1
`, job.ID, string(job.Status), job.AssignedTo, job.Result, job.Error, job.Attempts, job.MaxAttempts, job.LastFailure, job.UpdatedAt, nullableTime(job.StartedAt), nullableTime(job.FinishedAt))
	}
}

func (s *PostgresStore) rescheduleOrFailJob(job jobs.Job, failedNodeID string, now time.Time, reason string) jobs.Job {
	job.MaxAttempts = normalizeMaxAttempts(job.MaxAttempts)
	if job.Attempts <= 0 {
		job.Attempts = 1
	}
	if job.Attempts < job.MaxAttempts {
		if nextWorkerID := s.pickWorkerExcept(job.Requirements, map[string]bool{failedNodeID: true}); nextWorkerID != "" {
			job.Status = jobs.StatusScheduled
			job.AssignedTo = nextWorkerID
			job.Attempts++
			job.LastFailure = reason
			job.Error = ""
			job.Result = ""
			job.StartedAt = time.Time{}
			job.FinishedAt = time.Time{}
			job.UpdatedAt = now
			return job
		}
		job.Status = jobs.StatusQueued
		job.AssignedTo = ""
		job.LastFailure = reason + "; waiting for capable worker"
		job.Error = ""
		job.Result = ""
		job.StartedAt = time.Time{}
		job.FinishedAt = time.Time{}
		job.UpdatedAt = now
		return job
	}
	job.Status = jobs.StatusFailed
	job.Error = reason
	job.LastFailure = reason
	job.FinishedAt = now
	job.UpdatedAt = now
	return job
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

func (s *PostgresStore) PutRPCHealth(update RPCHealthUpdate) RPCHealthRecord {
	endpoint := strings.TrimSpace(update.Endpoint)
	if endpoint == "" {
		return RPCHealthRecord{}
	}
	if update.CheckedAt.IsZero() {
		update.CheckedAt = time.Now().UTC()
	}
	row := s.pool.QueryRow(context.Background(), `
INSERT INTO rpc_health (
  endpoint, node_id, node_name, ready, successes, failures, consecutive_failures,
  last_latency_ms, last_error, updated_at
)
VALUES ($1, $2, $3, $4, CASE WHEN $4 THEN 1 ELSE 0 END, CASE WHEN $4 THEN 0 ELSE 1 END,
  CASE WHEN $4 THEN 0 ELSE 1 END, $5, $6, $7)
ON CONFLICT (endpoint) DO UPDATE SET
  node_id = CASE WHEN EXCLUDED.node_id = '' THEN rpc_health.node_id ELSE EXCLUDED.node_id END,
  node_name = CASE WHEN EXCLUDED.node_name = '' THEN rpc_health.node_name ELSE EXCLUDED.node_name END,
  ready = EXCLUDED.ready,
  successes = rpc_health.successes + CASE WHEN EXCLUDED.ready THEN 1 ELSE 0 END,
  failures = rpc_health.failures + CASE WHEN EXCLUDED.ready THEN 0 ELSE 1 END,
  consecutive_failures = CASE WHEN EXCLUDED.ready THEN 0 ELSE rpc_health.consecutive_failures + 1 END,
  last_latency_ms = EXCLUDED.last_latency_ms,
  last_error = EXCLUDED.last_error,
  updated_at = EXCLUDED.updated_at
RETURNING endpoint, node_id, node_name, ready, successes, failures, consecutive_failures, last_latency_ms, last_error, updated_at
`, endpoint, strings.TrimSpace(update.NodeID), strings.TrimSpace(update.NodeName), update.Ready, update.LatencyMS, strings.TrimSpace(update.Error), update.CheckedAt)
	record, ok := scanRPCHealth(row)
	if !ok {
		return RPCHealthRecord{}
	}
	return record
}

func (s *PostgresStore) RPCHealth() []RPCHealthRecord {
	rows, err := s.pool.Query(context.Background(), `
SELECT endpoint, node_id, node_name, ready, successes, failures, consecutive_failures, last_latency_ms, last_error, updated_at
FROM rpc_health
ORDER BY ready DESC, updated_at DESC, endpoint ASC
`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	records := make([]RPCHealthRecord, 0)
	for rows.Next() {
		if record, ok := scanRPCHealth(rows); ok {
			records = append(records, record)
		}
	}
	return records
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
		ID:              newJobID(),
		Type:            req.Type,
		Status:          jobs.StatusQueued,
		RequestedBy:     req.RequestedBy,
		Input:           req.Input,
		Requirements:    req.Requirements,
		MaxAttempts:     normalizeMaxAttempts(req.MaxAttempts),
		CDIPState:       req.CDIPState,
		CDIPParentJobID: req.CDIPParentJobID,
		CDIPStageIndex:  req.CDIPStageIndex,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if req.NoAutoAssign {
		job.AssignedTo = req.AssignedTo
		job.LastFailure = "waiting for coordinator"
	} else if req.AssignedTo != "" {
		if s.workerCanAccept(req.AssignedTo, job.Requirements) {
			job.AssignedTo = req.AssignedTo
			job.Status = jobs.StatusScheduled
			job.Attempts = 1
		} else {
			job.LastFailure = s.waitingReason(job.Requirements)
		}
	} else {
		if workerID := s.pickWorker(job.Requirements); workerID != "" {
			job.AssignedTo = workerID
			job.Status = jobs.StatusScheduled
			job.Attempts = 1
		} else {
			job.LastFailure = s.waitingReason(job.Requirements)
		}
	}

	requirements, _ := json.Marshal(job.Requirements)
	_, err := s.pool.Exec(context.Background(), `
INSERT INTO jobs (id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, cdip_state, cdip_parent_job_id, cdip_stage_index)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
`, job.ID, job.Type, string(job.Status), job.RequestedBy, job.AssignedTo, job.Input, requirements, job.Result, job.Error, job.Attempts, job.MaxAttempts, job.LastFailure, job.CreatedAt, job.UpdatedAt, string(job.CDIPState), job.CDIPParentJobID, job.CDIPStageIndex)
	return job, err
}

func (s *PostgresStore) CreateJobsBatch(requests []jobs.CreateRequest) ([]jobs.Job, error) {
	now := time.Now().UTC()
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())

	created := make([]jobs.Job, 0, len(requests))
	for _, req := range requests {
		job := jobs.Job{
			ID:              newJobID(),
			Type:            req.Type,
			Status:          jobs.StatusQueued,
			RequestedBy:     req.RequestedBy,
			Input:           req.Input,
			Requirements:    req.Requirements,
			MaxAttempts:     normalizeMaxAttempts(req.MaxAttempts),
			CDIPState:       req.CDIPState,
			CDIPParentJobID: req.CDIPParentJobID,
			CDIPStageIndex:  req.CDIPStageIndex,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if req.NoAutoAssign {
			job.AssignedTo = req.AssignedTo
			job.LastFailure = "waiting for coordinator"
		} else if req.AssignedTo != "" {
			if s.workerCanAccept(req.AssignedTo, job.Requirements) {
				job.AssignedTo = req.AssignedTo
				job.Status = jobs.StatusScheduled
				job.Attempts = 1
			} else {
				job.LastFailure = s.waitingReason(job.Requirements)
			}
		} else {
			if workerID := s.pickWorker(job.Requirements); workerID != "" {
				job.AssignedTo = workerID
				job.Status = jobs.StatusScheduled
				job.Attempts = 1
			} else {
				job.LastFailure = s.waitingReason(job.Requirements)
			}
		}

		requirements, _ := json.Marshal(job.Requirements)
		if _, err := tx.Exec(context.Background(), `
INSERT INTO jobs (id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, cdip_state, cdip_parent_job_id, cdip_stage_index)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
`, job.ID, job.Type, string(job.Status), job.RequestedBy, job.AssignedTo, job.Input, requirements, job.Result, job.Error, job.Attempts, job.MaxAttempts, job.LastFailure, job.CreatedAt, job.UpdatedAt, string(job.CDIPState), job.CDIPParentJobID, job.CDIPStageIndex); err != nil {
			return nil, err
		}
		created = append(created, job)
	}
	if err := tx.Commit(context.Background()); err != nil {
		return nil, err
	}
	return created, nil
}

func (s *PostgresStore) Jobs() []jobs.Job {
	rows, err := s.pool.Query(context.Background(), `
SELECT id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
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
SELECT id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
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
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, nodeID, string(jobs.StatusScheduled), string(jobs.StatusRunning), now)
	return scanJob(row)
}

func (s *PostgresStore) RecoverStaleJobs(staleAfter time.Duration) []jobs.Job {
	if staleAfter <= 0 {
		return nil
	}

	now := time.Now().UTC()
	cutoff := now.Add(-staleAfter)
	rows, err := s.pool.Query(context.Background(), `
SELECT id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
FROM jobs
WHERE assigned_to <> '' AND status IN ($1, $2) AND updated_at < $3
ORDER BY updated_at ASC
`, string(jobs.StatusScheduled), string(jobs.StatusRunning), cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()

	recovered := make([]jobs.Job, 0)
	for rows.Next() {
		job, ok := scanJob(rows)
		if !ok {
			continue
		}
		reason := "job timed out waiting for worker progress"
		failedNodeID := job.AssignedTo
		job = s.rescheduleOrFailJob(job, failedNodeID, now, reason)
		if job.Status == jobs.StatusFailed && job.Type == models.JobGenerateStage {
			job.CDIPState = cdip.StageFailed
			job.Result = cdipStageResult(cdip.StageFailed, reason, now, "")
			s.failCDIPParentJob(job, now, reason)
		}
		updated, ok := s.updateRecoveredJob(job)
		if ok {
			recovered = append(recovered, updated)
		}
	}
	return recovered
}

func (s *PostgresStore) updateRecoveredJob(job jobs.Job) (jobs.Job, bool) {
	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET status = $2,
    assigned_to = $3,
    result = $4,
    error = $5,
    attempts = $6,
    max_attempts = $7,
    last_failure = $8,
    updated_at = $9,
    started_at = $10,
    finished_at = $11,
    cdip_state = $12
WHERE id = $1
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, job.ID, string(job.Status), job.AssignedTo, job.Result, job.Error, job.Attempts, job.MaxAttempts, job.LastFailure, job.UpdatedAt, nullableTime(job.StartedAt), nullableTime(job.FinishedAt), string(job.CDIPState))
	return scanJob(row)
}

func (s *PostgresStore) failCDIPParentJob(stage jobs.Job, now time.Time, reason string) {
	parentID := strings.TrimSpace(stage.CDIPParentJobID)
	if parentID == "" {
		return
	}
	_, _ = s.pool.Exec(context.Background(), `
UPDATE jobs
SET status = $2,
    result = '',
    error = $3,
    last_failure = $3,
    updated_at = $4,
    finished_at = $4
WHERE id = $1 AND type = $5 AND status NOT IN ($6, $7, $8)
`, parentID, string(jobs.StatusFailed), reason, now, models.JobGenerateDistributed, string(jobs.StatusSucceeded), string(jobs.StatusFailed), string(jobs.StatusCanceled))
}

func (s *PostgresStore) CompleteJob(jobID string, req jobs.CompleteRequest) (jobs.Job, bool) {
	now := time.Now().UTC()
	selectRow := s.pool.QueryRow(context.Background(), `
SELECT id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
FROM jobs
WHERE id = $1 AND assigned_to = $2 AND status IN ($3, $4)
`, jobID, req.NodeID, string(jobs.StatusRunning), string(jobs.StatusCanceled))
	job, ok := scanJob(selectRow)
	if !ok {
		return jobs.Job{}, false
	}
	if job.Status == jobs.StatusCanceled {
		return job, true
	}

	job.Result = req.Result
	job.Error = req.Error
	job.UpdatedAt = now
	if req.Error != "" {
		job = s.rescheduleOrFailJob(job, req.NodeID, now, req.Error)
	} else {
		job.Status = jobs.StatusSucceeded
		job.FinishedAt = now
	}

	updatedRow := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET status = $2,
    assigned_to = $3,
    result = $4,
    error = $5,
    attempts = $6,
    max_attempts = $7,
    last_failure = $8,
    updated_at = $9,
    started_at = $10,
    finished_at = $11
WHERE id = $1
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, job.ID, string(job.Status), job.AssignedTo, job.Result, job.Error, job.Attempts, job.MaxAttempts, job.LastFailure, job.UpdatedAt, nullableTime(job.StartedAt), nullableTime(job.FinishedAt))
	updated, ok := scanJob(updatedRow)
	if ok {
		s.scheduleQueuedJobs(now)
	}
	return updated, ok
}

func (s *PostgresStore) UpdateJobProgress(jobID string, req jobs.ProgressRequest) (jobs.Job, bool) {
	now := time.Now().UTC()
	result := jobProgressResult(req, now)
	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET result = $4,
    updated_at = $5
WHERE id = $1 AND assigned_to = $2 AND status = $3
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, jobID, req.NodeID, string(jobs.StatusRunning), result, now)
	return scanJob(row)
}

func (s *PostgresStore) UpdateCDIPStageState(jobID string, next cdip.StageState, detail string) (jobs.Job, bool) {
	return s.UpdateCDIPStageStateWithWorkerResult(jobID, next, detail, "")
}

func (s *PostgresStore) UpdateCDIPStageStateWithWorkerResult(jobID string, next cdip.StageState, detail string, workerResult string) (jobs.Job, bool) {
	now := time.Now().UTC()
	current, ok := s.Job(jobID)
	if !ok || current.Type != models.JobGenerateStage {
		return jobs.Job{}, false
	}
	from := current.CDIPState
	if from == "" {
		from = cdip.StagePlanned
	}
	if !cdip.CanTransition(from, next) {
		return jobs.Job{}, false
	}
	status := current.Status
	errorText := current.Error
	lastFailure := current.LastFailure
	finishedAt := current.FinishedAt
	attempts := current.Attempts
	switch next {
	case cdip.StagePreparing:
		if current.Status == jobs.StatusQueued && strings.TrimSpace(current.AssignedTo) != "" {
			status = jobs.StatusScheduled
			lastFailure = ""
			if attempts == 0 {
				attempts = 1
			}
		}
	case cdip.StageCompleted:
		status = jobs.StatusSucceeded
		finishedAt = now
	case cdip.StageFailed:
		status = jobs.StatusFailed
		errorText = strings.TrimSpace(detail)
		lastFailure = errorText
		finishedAt = now
	case cdip.StageAborted:
		status = jobs.StatusCanceled
		errorText = strings.TrimSpace(detail)
		finishedAt = now
	}
	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET cdip_state = $2,
    status = $3,
    result = $4,
    error = $5,
    last_failure = $6,
    attempts = $7,
    updated_at = $8,
    finished_at = $9
WHERE id = $1 AND type = $10
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, jobID, string(next), string(status), cdipStageResult(next, detail, now, workerResult), errorText, lastFailure, attempts, now, nullableTime(finishedAt), models.JobGenerateStage)
	return scanJob(row)
}

func (s *PostgresStore) DispatchCDIPStageCommand(jobID string, input string, next cdip.StageState, detail string) (jobs.Job, bool) {
	now := time.Now().UTC()
	current, ok := s.Job(jobID)
	if !ok || current.Type != models.JobGenerateStage {
		return jobs.Job{}, false
	}
	from := current.CDIPState
	if from == "" {
		from = cdip.StagePlanned
	}
	if !cdip.CanTransition(from, next) {
		return jobs.Job{}, false
	}
	if strings.TrimSpace(input) == "" {
		input = current.Input
	}
	attempts := current.Attempts + 1
	if attempts <= 0 {
		attempts = 1
	}
	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET input = $2,
    cdip_state = $3,
    status = $4,
    result = $5,
    error = '',
    last_failure = '',
    attempts = $6,
    updated_at = $7,
    started_at = NULL,
    finished_at = NULL
WHERE id = $1 AND type = $8
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, jobID, input, string(next), string(jobs.StatusScheduled), cdipStageResult(next, detail, now, ""), attempts, now, models.JobGenerateStage)
	return scanJob(row)
}

func (s *PostgresStore) CompleteCoordinatorJob(jobID string, result string, errText string) (jobs.Job, bool) {
	now := time.Now().UTC()
	status := jobs.StatusSucceeded
	lastFailure := ""
	errText = strings.TrimSpace(errText)
	if errText != "" {
		status = jobs.StatusFailed
		lastFailure = errText
	}
	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET status = $2,
    result = $3,
    error = $4,
    last_failure = $5,
    updated_at = $6,
    finished_at = $6
WHERE id = $1 AND type = $7
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, jobID, string(status), strings.TrimSpace(result), errText, lastFailure, now, models.JobGenerateDistributed)
	return scanJob(row)
}

func (s *PostgresStore) CancelJob(jobID string) (jobs.Job, bool) {
	now := time.Now().UTC()
	row := s.pool.QueryRow(context.Background(), `
UPDATE jobs
SET status = $2,
    error = $3,
    last_failure = '',
    updated_at = $4,
    finished_at = $4
WHERE id = $1 AND status IN ($5, $6, $7)
RETURNING id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
`, jobID, string(jobs.StatusCanceled), "canceled by operator", now, string(jobs.StatusQueued), string(jobs.StatusScheduled), string(jobs.StatusRunning))
	job, ok := scanJob(row)
	if ok {
		if job.Type == models.JobGenerateDistributed {
			s.cancelCDIPStageJobsForParent(job.ID, now)
		}
		s.scheduleQueuedJobs(now)
	}
	return job, ok
}

func (s *PostgresStore) cancelCDIPStageJobsForParent(parentJobID string, now time.Time) {
	_, _ = s.pool.Exec(context.Background(), `
UPDATE jobs
SET status = $2,
    cdip_state = $3,
    result = $4,
    error = $5,
    last_failure = '',
    updated_at = $6,
    finished_at = $6
WHERE type = $7
  AND cdip_parent_job_id = $1
  AND status IN ($8, $9, $10)
`, parentJobID, string(jobs.StatusCanceled), string(cdip.StageAborted), cdipStageResult(cdip.StageAborted, "canceled by operator", now, ""), "canceled by operator", now, models.JobGenerateStage, string(jobs.StatusQueued), string(jobs.StatusScheduled), string(jobs.StatusRunning))
}

func (s *PostgresStore) Nodes() []cluster.Node {
	rows, err := s.pool.Query(context.Background(), `
SELECT id, name, role, status, endpoint, auth_token, resources, joined_at, updated_at
FROM nodes
ORDER BY joined_at ASC
`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var nodes []cluster.Node
	var staleWorkerIDs []string
	now := time.Now().UTC()
	for rows.Next() {
		var node cluster.Node
		var role, status string
		var payload []byte
		if err := rows.Scan(&node.ID, &node.Name, &role, &status, &node.Endpoint, &node.AuthToken, &payload, &node.JoinedAt, &node.UpdatedAt); err != nil {
			continue
		}
		node.Role = cluster.NodeRole(role)
		node.Status = cluster.NodeStatus(status)
		_ = json.Unmarshal(payload, &node.Resources)
		previousStatus := node.Status
		node = s.deriveNodeStatus(node, now)
		if previousStatus == cluster.NodeStatusOnline && node.Status == cluster.NodeStatusOffline {
			node.UpdatedAt = now
			staleWorkerIDs = append(staleWorkerIDs, node.ID)
		}
		nodes = append(nodes, node)
	}
	rows.Close()
	for _, nodeID := range staleWorkerIDs {
		_, _ = s.pool.Exec(context.Background(), `
UPDATE nodes
SET status = $2, updated_at = $3
WHERE id = $1
`, nodeID, string(cluster.NodeStatusOffline), now)
		s.failActiveJobsForWorker(nodeID, now, "worker heartbeat timed out")
	}
	return nodes
}

func (s *PostgresStore) WorkerAuthToken(nodeID string) (string, bool) {
	var token string
	err := s.pool.QueryRow(context.Background(), `
SELECT auth_token
FROM nodes
WHERE id = $1
`, nodeID).Scan(&token)
	if err != nil {
		return "", false
	}
	return token, true
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
		if node.Role == cluster.NodeRoleWorker && node.Status == cluster.NodeStatusOnline {
			summary.Resources.CPU.CoresTotal += node.Resources.CPU.CoresTotal
			summary.Resources.CPU.CoresAllowed += node.Resources.CPU.CoresAllowed
			summary.Resources.Memory.TotalBytes += node.Resources.Memory.TotalBytes
			summary.Resources.Memory.AllowedBytes += node.Resources.Memory.AllowedBytes
			summary.Resources.Storage.TotalBytes += node.Resources.Storage.TotalBytes
			summary.Resources.Storage.AllowedBytes += node.Resources.Storage.AllowedBytes
			summary.Resources.Storage.FreeBytes += node.Resources.Storage.FreeBytes
			summary.Resources.Storage.UsedByModelsBytes += node.Resources.Storage.UsedByModelsBytes
			summary.Resources.Storage.UsedByRuntimesBytes += node.Resources.Storage.UsedByRuntimesBytes
			summary.Resources.Storage.UsedByCacheBytes += node.Resources.Storage.UsedByCacheBytes
			summary.Resources.Storage.PartialModelBytes += node.Resources.Storage.PartialModelBytes
			summary.Resources.Storage.PartialModelFiles += node.Resources.Storage.PartialModelFiles
			summary.Resources.Storage.OrphanModelBytes += node.Resources.Storage.OrphanModelBytes
			summary.Resources.Storage.OrphanModelDirs += node.Resources.Storage.OrphanModelDirs
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

func (s *PostgresStore) deriveNodeStatus(node cluster.Node, now time.Time) cluster.Node {
	if node.Role == cluster.NodeRoleWorker &&
		node.Status == cluster.NodeStatusOnline &&
		now.Sub(node.UpdatedAt) > s.heartbeatTimeout {
		node.Status = cluster.NodeStatusOffline
	}
	return node
}

func (s *PostgresStore) scheduleQueuedJobs(now time.Time) {
	rows, err := s.pool.Query(context.Background(), `
SELECT id, type, status, requested_by, assigned_to, input, requirements, result, error, attempts, max_attempts, last_failure, created_at, updated_at, started_at, finished_at, cdip_state, cdip_parent_job_id, cdip_stage_index
FROM jobs
WHERE status = $1 AND assigned_to = ''
ORDER BY created_at ASC
`, string(jobs.StatusQueued))
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		job, ok := scanJob(rows)
		if !ok {
			continue
		}
		workerID := s.pickWorker(job.Requirements)
		if workerID == "" {
			if job.LastFailure == "" || job.LastFailure == "waiting for capable worker" || job.LastFailure == "waiting for available worker capacity" {
				_, _ = s.pool.Exec(context.Background(), `
UPDATE jobs
SET last_failure = $2, updated_at = $3
WHERE id = $1
`, job.ID, s.waitingReason(job.Requirements), now)
			}
			continue
		}
		lastFailure := job.LastFailure
		if lastFailure == "waiting for capable worker" || lastFailure == "waiting for available worker capacity" {
			lastFailure = ""
		}
		_, _ = s.pool.Exec(context.Background(), `
UPDATE jobs
SET status = $2,
    assigned_to = $3,
    attempts = attempts + 1,
    last_failure = $6,
    error = '',
    result = '',
    started_at = NULL,
    finished_at = NULL,
    updated_at = $4
WHERE id = $1 AND status = $5 AND assigned_to = ''
`, job.ID, string(jobs.StatusScheduled), workerID, now, string(jobs.StatusQueued), lastFailure)
	}
}

func (s *PostgresStore) pickWorker(req jobs.Requirements) string {
	return s.pickWorkerExcept(req, nil)
}

func (s *PostgresStore) pickWorkerExcept(req jobs.Requirements, excluded map[string]bool) string {
	nodes := s.Nodes()
	benchmarks := s.BenchmarkSummaryByNode()
	var bestID string
	var bestScore float64
	for _, node := range nodes {
		if excluded[node.ID] {
			continue
		}
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		if !nodeMeetsRequirements(node, req) {
			continue
		}
		if s.activeJobsForWorker(node.ID) >= workerJobSlots(node) {
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

func (s *PostgresStore) workerCanAccept(nodeID string, req jobs.Requirements) bool {
	for _, node := range s.Nodes() {
		if node.ID == nodeID {
			return node.Role == cluster.NodeRoleWorker &&
				node.Status == cluster.NodeStatusOnline &&
				nodeMeetsRequirements(node, req) &&
				s.activeJobsForWorker(node.ID) < workerJobSlots(node)
		}
	}
	return false
}

func (s *PostgresStore) waitingReason(req jobs.Requirements) string {
	for _, node := range s.Nodes() {
		if node.Role == cluster.NodeRoleWorker &&
			node.Status == cluster.NodeStatusOnline &&
			nodeMeetsRequirements(node, req) {
			return "waiting for available worker capacity"
		}
	}
	return "waiting for capable worker"
}

func (s *PostgresStore) activeJobsForWorker(nodeID string) int {
	var count int
	row := s.pool.QueryRow(context.Background(), `
SELECT COUNT(*)
FROM jobs
WHERE assigned_to = $1 AND status IN ($2, $3)
`, nodeID, string(jobs.StatusScheduled), string(jobs.StatusRunning))
	if err := row.Scan(&count); err != nil {
		return workerJobSlots(cluster.Node{})
	}
	return count
}

type jobScanner interface {
	Scan(dest ...any) error
}

func scanJob(row jobScanner) (jobs.Job, bool) {
	var job jobs.Job
	var status string
	var startedAt *time.Time
	var finishedAt *time.Time
	var requirements []byte
	var cdipState string
	if err := row.Scan(
		&job.ID,
		&job.Type,
		&status,
		&job.RequestedBy,
		&job.AssignedTo,
		&job.Input,
		&requirements,
		&job.Result,
		&job.Error,
		&job.Attempts,
		&job.MaxAttempts,
		&job.LastFailure,
		&job.CreatedAt,
		&job.UpdatedAt,
		&startedAt,
		&finishedAt,
		&cdipState,
		&job.CDIPParentJobID,
		&job.CDIPStageIndex,
	); err != nil {
		return jobs.Job{}, false
	}
	job.Status = jobs.Status(status)
	job.CDIPState = cdip.StageState(cdipState)
	if startedAt != nil {
		job.StartedAt = *startedAt
	}
	if finishedAt != nil {
		job.FinishedAt = *finishedAt
	}
	if len(requirements) > 0 {
		_ = json.Unmarshal(requirements, &job.Requirements)
	}
	job.MaxAttempts = normalizeMaxAttempts(job.MaxAttempts)
	return job, true
}

func scanRPCHealth(row jobScanner) (RPCHealthRecord, bool) {
	var record RPCHealthRecord
	if err := row.Scan(
		&record.Endpoint,
		&record.NodeID,
		&record.NodeName,
		&record.Ready,
		&record.Successes,
		&record.Failures,
		&record.ConsecutiveFailures,
		&record.LastLatencyMS,
		&record.LastError,
		&record.UpdatedAt,
	); err != nil {
		return RPCHealthRecord{}, false
	}
	return record, true
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
