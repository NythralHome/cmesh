package manager

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/membership"
)

type State struct {
	mu               sync.RWMutex
	startedAt        time.Time
	heartbeatTimeout time.Duration
	nodes            map[string]cluster.Node
}

func NewState() *State {
	return &State{
		startedAt:        time.Now().UTC(),
		heartbeatTimeout: 30 * time.Second,
		nodes:            make(map[string]cluster.Node),
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

func (s *State) Nodes() []cluster.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes := make([]cluster.Node, 0, len(s.nodes))
	for _, node := range s.nodes {
		node = s.deriveNodeStatus(node, time.Now().UTC())
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

type ClusterSummary struct {
	StartedAt        time.Time                `json:"started_at"`
	WorkersTotal     int                      `json:"workers_total"`
	WorkersOnline    int                      `json:"workers_online"`
	GPUs             int                      `json:"gpus"`
	VRAMTotalBytes   uint64                   `json:"vram_total_bytes"`
	VRAMAllowedBytes uint64                   `json:"vram_allowed_bytes"`
	Resources        cluster.ResourceSnapshot `json:"resources"`
}
