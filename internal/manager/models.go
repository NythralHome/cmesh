package manager

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
)

type ModelSummary struct {
	Model         models.Model      `json:"model"`
	Status        string            `json:"status"`
	InstalledOn   []string          `json:"installed_on"`
	GeneratableOn []string          `json:"generatable_on,omitempty"`
	ActiveJobID   string            `json:"active_job_id,omitempty"`
	LastJobID     string            `json:"last_job_id,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
	LastUpdated   time.Time         `json:"last_updated,omitempty"`
	CapableNodes  int               `json:"capable_nodes"`
	Capabilities  []ModelCapability `json:"capabilities,omitempty"`
}

type ModelCapability struct {
	NodeID              string   `json:"node_id"`
	Name                string   `json:"name"`
	Capable             bool     `json:"capable"`
	AllowedMemoryBytes  uint64   `json:"allowed_memory_bytes"`
	AllowedStorageBytes uint64   `json:"allowed_storage_bytes"`
	FreeStorageBytes    uint64   `json:"free_storage_bytes"`
	AllowedVRAMBytes    uint64   `json:"allowed_vram_bytes,omitempty"`
	ActiveJobs          int      `json:"active_jobs"`
	JobSlots            int      `json:"job_slots"`
	Reasons             []string `json:"reasons,omitempty"`
}

func modelSummaries(catalog []models.Model, jobsList []jobs.Job, nodes []cluster.Node) []ModelSummary {
	out := make([]ModelSummary, 0, len(catalog))
	jobsList = append([]jobs.Job(nil), jobsList...)
	sort.Slice(jobsList, func(i, j int) bool {
		return jobsList[i].UpdatedAt.Before(jobsList[j].UpdatedAt)
	})
	for _, model := range catalog {
		summary := ModelSummary{
			Model:        model,
			Status:       "available",
			Capabilities: modelCapabilities(model, nodes, jobsList),
		}
		summary.CapableNodes = capableModelNodes(summary.Capabilities)
		installed := make(map[string]bool)
		for _, node := range nodes {
			if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
				continue
			}
			for _, installedModel := range node.Resources.Models {
				if installedModel.ID == model.ID {
					installed[node.ID] = true
					if nodeRuntimeReady(node, string(model.Runtime)) {
						summary.GeneratableOn = append(summary.GeneratableOn, node.ID)
					}
				}
			}
		}
		var lastUpdated time.Time
		for _, job := range jobsList {
			modelID, ok := jobModelID(job)
			if !ok || modelID != model.ID {
				continue
			}
			if job.UpdatedAt.After(lastUpdated) {
				lastUpdated = job.UpdatedAt
				summary.LastJobID = job.ID
				summary.LastUpdated = job.UpdatedAt
				summary.LastError = visibleModelLastError(job.Error)
			}
			switch job.Type {
			case models.JobInstall:
				if job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning || job.Status == jobs.StatusQueued {
					summary.Status = "installing"
					summary.ActiveJobID = job.ID
				}
			case models.JobDelete:
				if job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning || job.Status == jobs.StatusQueued {
					summary.Status = "deleting"
					summary.ActiveJobID = job.ID
				}
			}
		}
		for nodeID := range installed {
			summary.InstalledOn = append(summary.InstalledOn, nodeID)
		}
		sort.Strings(summary.InstalledOn)
		sort.Strings(summary.GeneratableOn)
		if len(summary.InstalledOn) > 0 && summary.Status == "available" {
			summary.Status = "installed"
		}
		out = append(out, summary)
	}
	return out
}

func nodeRuntimeReady(node cluster.Node, runtimeName string) bool {
	runtimeName = strings.TrimSpace(runtimeName)
	if runtimeName == "" {
		return true
	}
	for _, runtime := range node.Resources.Runtimes {
		if runtime.Name == runtimeName && runtime.Ready {
			return true
		}
	}
	return false
}

func modelCapabilities(model models.Model, nodes []cluster.Node, jobsList []jobs.Job) []ModelCapability {
	out := make([]ModelCapability, 0, len(nodes))
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		item := ModelCapability{
			NodeID:              node.ID,
			Name:                node.Name,
			AllowedMemoryBytes:  node.Resources.Memory.AllowedBytes,
			AllowedStorageBytes: node.Resources.Storage.AllowedBytes,
			FreeStorageBytes:    node.Resources.Storage.FreeBytes,
			AllowedVRAMBytes:    maxAllowedVRAM(node.Resources.GPU),
			ActiveJobs:          activeModelJobsForNode(jobsList, node.ID),
			JobSlots:            workerJobSlots(node),
		}
		if strings.TrimSpace(item.Name) == "" {
			item.Name = node.ID
		}
		if node.Resources.Memory.AllowedBytes < model.MemoryBytes {
			item.Reasons = append(item.Reasons, fmt.Sprintf("RAM short by %.1f GB", gbDiff(model.MemoryBytes, node.Resources.Memory.AllowedBytes)))
		}
		if node.Resources.Storage.AllowedBytes < model.DiskBytes {
			item.Reasons = append(item.Reasons, fmt.Sprintf("disk short by %.1f GB", gbDiff(model.DiskBytes, node.Resources.Storage.AllowedBytes)))
		}
		if node.Resources.Storage.FreeBytes > 0 && node.Resources.Storage.FreeBytes < model.DiskBytes {
			item.Reasons = append(item.Reasons, fmt.Sprintf("free disk short by %.1f GB", gbDiff(model.DiskBytes, node.Resources.Storage.FreeBytes)))
		}
		if model.VRAMBytes > 0 && !hasAllowedVRAM(node.Resources.GPU, model.VRAMBytes) {
			item.Reasons = append(item.Reasons, fmt.Sprintf("VRAM short by %.1f GB", gbDiff(model.VRAMBytes, item.AllowedVRAMBytes)))
		}
		if item.JobSlots > 0 && item.ActiveJobs >= item.JobSlots {
			item.Reasons = append(item.Reasons, fmt.Sprintf("all job slots busy (%d/%d)", item.ActiveJobs, item.JobSlots))
		}
		item.Capable = len(item.Reasons) == 0
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Capable != out[j].Capable {
			return out[i].Capable
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func activeModelJobsForNode(jobsList []jobs.Job, nodeID string) int {
	var total int
	for _, job := range jobsList {
		if job.AssignedTo != nodeID {
			continue
		}
		if job.Status == jobs.StatusQueued || job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning {
			total++
		}
	}
	return total
}

func capableModelNodes(capabilities []ModelCapability) int {
	var total int
	for _, capability := range capabilities {
		if capability.Capable {
			total++
		}
	}
	return total
}

func hasAllowedVRAM(gpus []cluster.GPUResources, required uint64) bool {
	for _, gpu := range gpus {
		if gpu.ComputeCompatible && gpu.AllowedVRAMBytes >= required {
			return true
		}
	}
	return false
}

func maxAllowedVRAM(gpus []cluster.GPUResources) uint64 {
	var maxValue uint64
	for _, gpu := range gpus {
		if gpu.ComputeCompatible && gpu.AllowedVRAMBytes > maxValue {
			maxValue = gpu.AllowedVRAMBytes
		}
	}
	return maxValue
}

func gbDiff(required uint64, actual uint64) float64 {
	if required <= actual {
		return 0
	}
	return float64(required-actual) / 1024 / 1024 / 1024
}

func visibleModelLastError(value string) string {
	if strings.Contains(value, "unsupported job type") {
		return ""
	}
	return value
}

func jobModelID(job jobs.Job) (string, bool) {
	var payload struct {
		ModelID string `json:"model_id"`
	}
	if strings.TrimSpace(job.Input) == "" {
		return "", false
	}
	if err := json.Unmarshal([]byte(job.Input), &payload); err != nil {
		return "", false
	}
	return payload.ModelID, payload.ModelID != ""
}

func modelInstalledOn(summary ModelSummary, nodeID string) bool {
	for _, installedNodeID := range summary.InstalledOn {
		if installedNodeID == nodeID {
			return true
		}
	}
	return false
}

func modelGeneratableOn(summary ModelSummary, nodeID string) bool {
	for _, readyNodeID := range summary.GeneratableOn {
		if readyNodeID == nodeID {
			return true
		}
	}
	return false
}

func findModelSummary(summaries []ModelSummary, modelID string) (ModelSummary, bool) {
	for _, summary := range summaries {
		if summary.Model.ID == modelID {
			return summary, true
		}
	}
	return ModelSummary{}, false
}

func modelInstallEligibility(summary ModelSummary, nodeID string) (bool, string) {
	if nodeID != "" {
		for _, capability := range summary.Capabilities {
			if capability.NodeID != nodeID {
				continue
			}
			if capability.Capable {
				return true, ""
			}
			return false, capability.Name + ": " + strings.Join(capability.Reasons, "; ")
		}
		return false, "selected worker is not online or does not exist"
	}
	if summary.CapableNodes > 0 {
		return true, ""
	}
	if len(summary.Capabilities) == 0 {
		return false, "no online workers are reporting resources"
	}
	reasons := make([]string, 0, len(summary.Capabilities))
	for _, capability := range summary.Capabilities {
		if capability.Capable {
			continue
		}
		reason := strings.Join(capability.Reasons, "; ")
		if reason == "" {
			reason = "not eligible"
		}
		reasons = append(reasons, capability.Name+": "+reason)
	}
	sort.Strings(reasons)
	return false, strings.Join(reasons, " | ")
}
