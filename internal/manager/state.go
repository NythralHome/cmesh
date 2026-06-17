package manager

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/membership"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/resources"
)

const defaultWorkerJobSlots = 1

type State struct {
	mu               sync.RWMutex
	startedAt        time.Time
	heartbeatTimeout time.Duration
	nodes            map[string]cluster.Node
	benchmarks       map[string]map[resources.BenchmarkKind]resources.BenchmarkResult
	jobs             map[string]jobs.Job
	conversations    map[string]Conversation
	memories         map[string]Memory
}

func NewState() *State {
	return &State{
		startedAt:        time.Now().UTC(),
		heartbeatTimeout: 30 * time.Second,
		nodes:            make(map[string]cluster.Node),
		benchmarks:       make(map[string]map[resources.BenchmarkKind]resources.BenchmarkResult),
		jobs:             make(map[string]jobs.Job),
		conversations:    make(map[string]Conversation),
		memories:         make(map[string]Memory),
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
	s.scheduleQueuedJobsLocked(now)
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
	now := time.Now().UTC()
	node.UpdatedAt = now
	s.nodes[hb.NodeID] = node
	s.scheduleQueuedJobsLocked(now)
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
		job = s.rescheduleOrFailJobLocked(job, nodeID, now, reason)
		s.jobs[id] = job
	}
}

func (s *State) rescheduleOrFailJobLocked(job jobs.Job, failedNodeID string, now time.Time, reason string) jobs.Job {
	job.MaxAttempts = normalizeMaxAttempts(job.MaxAttempts)
	if job.Attempts <= 0 {
		job.Attempts = 1
	}
	if job.Attempts < job.MaxAttempts {
		if nextWorkerID := s.pickWorkerExcludingLocked(job.Requirements, map[string]bool{failedNodeID: true}); nextWorkerID != "" {
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
		ID:           newJobID(),
		Type:         req.Type,
		Status:       jobs.StatusQueued,
		RequestedBy:  req.RequestedBy,
		Input:        req.Input,
		Requirements: req.Requirements,
		MaxAttempts:  normalizeMaxAttempts(req.MaxAttempts),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if req.AssignedTo != "" {
		if node, ok := s.nodes[req.AssignedTo]; ok && s.nodeCanAcceptJobLocked(node, job.Requirements, now) {
			job.AssignedTo = req.AssignedTo
			job.Status = jobs.StatusScheduled
			job.Attempts = 1
		} else {
			job.LastFailure = s.waitingReasonLocked(job.Requirements, now)
		}
	} else if workerID := s.pickWorkerLocked(job.Requirements); workerID != "" {
		job.AssignedTo = workerID
		job.Status = jobs.StatusScheduled
		job.Attempts = 1
	} else {
		job.LastFailure = s.waitingReasonLocked(job.Requirements, now)
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
	if !ok || job.AssignedTo != req.NodeID {
		return jobs.Job{}, false
	}
	if job.Status == jobs.StatusCanceled {
		return job, true
	}
	if job.Status != jobs.StatusRunning {
		return jobs.Job{}, false
	}

	now := time.Now().UTC()
	job.Result = req.Result
	job.Error = req.Error
	job.UpdatedAt = now
	if req.Error != "" {
		job = s.rescheduleOrFailJobLocked(job, req.NodeID, now, req.Error)
	} else {
		job.Status = jobs.StatusSucceeded
		job.FinishedAt = now
		s.appendAssistantMessageForJobLocked(job, now)
		s.cleanupModelPersistenceForDeleteJobLocked(job)
	}
	s.jobs[job.ID] = job
	s.scheduleQueuedJobsLocked(now)

	return job, true
}

func (s *State) Conversation(id string) (Conversation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	conversation, ok := s.conversations[id]
	conversation.Messages = append([]models.ChatMessage(nil), conversation.Messages...)
	return conversation, ok
}

func (s *State) Conversations() []Conversation {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Conversation, 0, len(s.conversations))
	for _, conversation := range s.conversations {
		conversation.Messages = append([]models.ChatMessage(nil), conversation.Messages...)
		out = append(out, conversation)
	}
	return out
}

func (s *State) AppendConversationMessage(id string, modelID string, nodeID string, systemPrompt string, message models.ChatMessage) Conversation {
	now := time.Now().UTC()
	message = normalizeChatMessage(message)

	s.mu.Lock()
	defer s.mu.Unlock()

	conversation, ok := s.conversations[id]
	if !ok {
		conversation = Conversation{
			ID:        id,
			CreatedAt: now,
		}
	}
	conversation.ModelID = modelID
	conversation.NodeID = nodeID
	if systemPrompt != "" {
		conversation.SystemPrompt = systemPrompt
	}
	if message.Content != "" {
		conversation.Messages = append(conversation.Messages, message)
		conversation.Messages = trimConversationMessages(conversation.Messages)
		if message.Role == "user" {
			s.upsertExtractedMemoriesLocked(modelID, conversation.ID, message.Content, now)
		}
	}
	conversation.UpdatedAt = now
	s.conversations[id] = conversation

	conversation.Messages = append([]models.ChatMessage(nil), conversation.Messages...)
	return conversation
}

func (s *State) Memories(modelID string) []Memory {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Memory, 0, len(s.memories))
	for _, memory := range s.memories {
		if modelID != "" && memory.ModelID != modelID {
			continue
		}
		out = append(out, memory)
	}
	return out
}

func (s *State) DeleteMemory(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.memories[id]; !ok {
		return false
	}
	delete(s.memories, id)
	return true
}

func (s *State) upsertExtractedMemoriesLocked(modelID string, conversationID string, content string, now time.Time) {
	for _, memory := range extractMemories(modelID, conversationID, content, now) {
		existingID := ""
		for id, existing := range s.memories {
			if existing.ModelID == memory.ModelID && existing.Key == memory.Key {
				existingID = id
				break
			}
		}
		if existingID != "" {
			existing := s.memories[existingID]
			existing.Value = memory.Value
			existing.Source = memory.Source
			existing.ConversationID = memory.ConversationID
			existing.UpdatedAt = now
			s.memories[existingID] = existing
			continue
		}
		s.memories[memory.ID] = memory
	}
}

func (s *State) cleanupModelPersistenceForDeleteJobLocked(job jobs.Job) {
	if job.Type != models.JobDelete {
		return
	}
	var input models.DeleteInput
	if err := json.Unmarshal([]byte(job.Input), &input); err != nil || input.ModelID == "" {
		return
	}
	for id, memory := range s.memories {
		if memory.ModelID == input.ModelID {
			delete(s.memories, id)
		}
	}
	for id, conversation := range s.conversations {
		if conversation.ModelID == input.ModelID {
			delete(s.conversations, id)
		}
	}
}

func (s *State) appendAssistantMessageForJobLocked(job jobs.Job, now time.Time) {
	if job.Type != models.JobGenerate || job.Result == "" {
		return
	}
	var input models.GenerateInput
	if err := json.Unmarshal([]byte(job.Input), &input); err != nil || input.ConversationID == "" {
		return
	}
	var result struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(job.Result), &result); err != nil || result.Output == "" {
		return
	}
	conversation, ok := s.conversations[input.ConversationID]
	if !ok {
		conversation = Conversation{
			ID:        input.ConversationID,
			ModelID:   input.ModelID,
			CreatedAt: now,
		}
	}
	conversation.ModelID = input.ModelID
	if input.SystemPrompt != "" {
		conversation.SystemPrompt = input.SystemPrompt
	}
	conversation.Messages = append(conversation.Messages, models.ChatMessage{
		Role:    "assistant",
		Content: result.Output,
	})
	conversation.Messages = trimConversationMessages(conversation.Messages)
	conversation.UpdatedAt = now
	s.conversations[input.ConversationID] = conversation
}

func (s *State) CancelJob(jobID string) (jobs.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok || !jobCanBeCanceled(job) {
		return jobs.Job{}, false
	}

	now := time.Now().UTC()
	job.Status = jobs.StatusCanceled
	job.Error = "canceled by operator"
	job.LastFailure = ""
	job.UpdatedAt = now
	job.FinishedAt = now
	s.jobs[job.ID] = job
	s.scheduleQueuedJobsLocked(now)
	return job, true
}

func (s *State) scheduleQueuedJobsLocked(now time.Time) {
	for id, job := range s.jobs {
		if job.Status != jobs.StatusQueued || job.AssignedTo != "" {
			continue
		}
		workerID := s.pickWorkerLocked(job.Requirements)
		if workerID == "" {
			reason := s.waitingReasonLocked(job.Requirements, now)
			if job.LastFailure == "" || job.LastFailure == "waiting for capable worker" || job.LastFailure == "waiting for available worker capacity" {
				job.LastFailure = reason
				job.UpdatedAt = now
				s.jobs[id] = job
			}
			continue
		}
		job.AssignedTo = workerID
		job.Status = jobs.StatusScheduled
		job.Attempts++
		if job.LastFailure == "waiting for capable worker" || job.LastFailure == "waiting for available worker capacity" {
			job.LastFailure = ""
		}
		job.Error = ""
		job.Result = ""
		job.StartedAt = time.Time{}
		job.FinishedAt = time.Time{}
		job.UpdatedAt = now
		s.jobs[id] = job
	}
}

func (s *State) pickWorkerLocked(req jobs.Requirements) string {
	return s.pickWorkerExcludingLocked(req, nil)
}

func (s *State) pickWorkerExcludingLocked(req jobs.Requirements, excluded map[string]bool) string {
	now := time.Now().UTC()
	var bestID string
	var bestScore float64

	for _, node := range s.nodes {
		if excluded[node.ID] {
			continue
		}
		node = s.deriveNodeStatus(node, now)
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		if !nodeMeetsRequirements(node, req) {
			continue
		}
		if s.activeJobsForWorkerLocked(node.ID) >= workerJobSlots(node) {
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

func (s *State) nodeCanAcceptJobLocked(node cluster.Node, req jobs.Requirements, now time.Time) bool {
	node = s.deriveNodeStatus(node, now)
	return node.Role == cluster.NodeRoleWorker &&
		node.Status == cluster.NodeStatusOnline &&
		nodeMeetsRequirements(node, req) &&
		s.activeJobsForWorkerLocked(node.ID) < workerJobSlots(node)
}

func (s *State) waitingReasonLocked(req jobs.Requirements, now time.Time) string {
	for _, node := range s.nodes {
		node = s.deriveNodeStatus(node, now)
		if node.Role == cluster.NodeRoleWorker &&
			node.Status == cluster.NodeStatusOnline &&
			nodeMeetsRequirements(node, req) {
			return "waiting for available worker capacity"
		}
	}
	return "waiting for capable worker"
}

func (s *State) activeJobsForWorkerLocked(nodeID string) int {
	var active int
	for _, job := range s.jobs {
		if job.AssignedTo != nodeID {
			continue
		}
		if job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning {
			active++
		}
	}
	return active
}

func workerJobSlots(node cluster.Node) int {
	if node.Resources.JobSlots > 0 {
		return node.Resources.JobSlots
	}
	return defaultWorkerJobSlots
}

func jobCanBeCanceled(job jobs.Job) bool {
	return job.Status == jobs.StatusQueued ||
		job.Status == jobs.StatusScheduled ||
		job.Status == jobs.StatusRunning
}

func nodeMeetsRequirements(node cluster.Node, req jobs.Requirements) bool {
	if req.CPUCores > 0 && node.Resources.CPU.CoresAllowed < req.CPUCores {
		return false
	}
	if req.MemoryBytes > 0 && node.Resources.Memory.AllowedBytes < req.MemoryBytes {
		return false
	}
	if req.DiskBytes > 0 && node.Resources.Storage.AllowedBytes < req.DiskBytes {
		return false
	}
	if !req.GPURequired && req.VRAMBytes == 0 {
		return true
	}
	for _, gpu := range node.Resources.GPU {
		if !gpu.ComputeCompatible {
			continue
		}
		if req.VRAMBytes == 0 || gpu.AllowedVRAMBytes >= req.VRAMBytes {
			return true
		}
	}
	return false
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

func normalizeMaxAttempts(value int) int {
	if value <= 0 {
		return jobs.DefaultMaxAttempts
	}
	return value
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
