package manager

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/resources"
)

type FileStore struct {
	*State
	path string
}

type fileStoreSnapshot struct {
	StartedAt     time.Time                                                        `json:"started_at"`
	Nodes         map[string]cluster.Node                                          `json:"nodes"`
	Benchmarks    map[string]map[resources.BenchmarkKind]resources.BenchmarkResult `json:"benchmarks"`
	Jobs          map[string]jobs.Job                                              `json:"jobs"`
	Conversations map[string]Conversation                                          `json:"conversations,omitempty"`
}

func NewFileStore(path string) (*FileStore, error) {
	store := &FileStore{
		State: NewState(),
		path:  path,
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *FileStore) RegisterWorker(req membership.JoinRequest) membership.JoinResponse {
	resp := s.State.RegisterWorker(req)
	_ = s.save()
	return resp
}

func (s *FileStore) Heartbeat(hb membership.Heartbeat) bool {
	ok := s.State.Heartbeat(hb)
	if ok {
		_ = s.save()
	}
	return ok
}

func (s *FileStore) MarkWorkerOffline(nodeID string) bool {
	ok := s.State.MarkWorkerOffline(nodeID)
	if ok {
		_ = s.save()
	}
	return ok
}

func (s *FileStore) PutBenchmark(result resources.BenchmarkResult) bool {
	ok := s.State.PutBenchmark(result)
	if ok {
		_ = s.save()
	}
	return ok
}

func (s *FileStore) CreateJob(req jobs.CreateRequest) (jobs.Job, error) {
	job, err := s.State.CreateJob(req)
	if err == nil {
		_ = s.save()
	}
	return job, err
}

func (s *FileStore) NextJobForWorker(nodeID string) (jobs.Job, bool) {
	job, ok := s.State.NextJobForWorker(nodeID)
	if ok {
		_ = s.save()
	}
	return job, ok
}

func (s *FileStore) CompleteJob(jobID string, req jobs.CompleteRequest) (jobs.Job, bool) {
	job, ok := s.State.CompleteJob(jobID, req)
	if ok {
		_ = s.save()
	}
	return job, ok
}

func (s *FileStore) AppendConversationMessage(id string, modelID string, nodeID string, systemPrompt string, message models.ChatMessage) Conversation {
	conversation := s.State.AppendConversationMessage(id, modelID, nodeID, systemPrompt, message)
	_ = s.save()
	return conversation
}

func (s *FileStore) CancelJob(jobID string) (jobs.Job, bool) {
	job, ok := s.State.CancelJob(jobID)
	if ok {
		_ = s.save()
	}
	return job, ok
}

func (s *FileStore) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.save()
	}
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}

	var snapshot fileStoreSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !snapshot.StartedAt.IsZero() {
		s.startedAt = snapshot.StartedAt
	}
	if snapshot.Nodes != nil {
		s.nodes = snapshot.Nodes
	}
	if snapshot.Benchmarks != nil {
		s.benchmarks = snapshot.Benchmarks
	}
	if snapshot.Jobs != nil {
		s.jobs = snapshot.Jobs
	}
	if snapshot.Conversations != nil {
		s.conversations = snapshot.Conversations
	}
	return nil
}

func (s *FileStore) save() error {
	s.mu.RLock()
	snapshot := fileStoreSnapshot{
		StartedAt:     s.startedAt,
		Nodes:         cloneNodes(s.nodes),
		Benchmarks:    cloneBenchmarks(s.benchmarks),
		Jobs:          cloneJobs(s.jobs),
		Conversations: cloneConversations(s.conversations),
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func cloneNodes(in map[string]cluster.Node) map[string]cluster.Node {
	out := make(map[string]cluster.Node, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneBenchmarks(in map[string]map[resources.BenchmarkKind]resources.BenchmarkResult) map[string]map[resources.BenchmarkKind]resources.BenchmarkResult {
	out := make(map[string]map[resources.BenchmarkKind]resources.BenchmarkResult, len(in))
	for nodeID, byKind := range in {
		out[nodeID] = make(map[resources.BenchmarkKind]resources.BenchmarkResult, len(byKind))
		for kind, result := range byKind {
			out[nodeID][kind] = result
		}
	}
	return out
}

func cloneJobs(in map[string]jobs.Job) map[string]jobs.Job {
	out := make(map[string]jobs.Job, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
