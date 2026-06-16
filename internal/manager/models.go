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
	Model        models.Model      `json:"model"`
	Status       string            `json:"status"`
	InstalledOn  []string          `json:"installed_on"`
	ActiveJobID  string            `json:"active_job_id,omitempty"`
	LastJobID    string            `json:"last_job_id,omitempty"`
	LastError    string            `json:"last_error,omitempty"`
	LastUpdated  time.Time         `json:"last_updated,omitempty"`
	CapableNodes int               `json:"capable_nodes"`
	Capabilities []ModelCapability `json:"capabilities,omitempty"`
}

type ModelCapability struct {
	NodeID  string   `json:"node_id"`
	Name    string   `json:"name"`
	Capable bool     `json:"capable"`
	Reasons []string `json:"reasons,omitempty"`
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
			Capabilities: modelCapabilities(model, nodes),
		}
		summary.CapableNodes = capableModelNodes(summary.Capabilities)
		installed := make(map[string]bool)
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
				summary.LastError = job.Error
			}
			switch job.Type {
			case models.JobInstall:
				if job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning || job.Status == jobs.StatusQueued {
					summary.Status = "installing"
					summary.ActiveJobID = job.ID
				}
				if job.Status == jobs.StatusSucceeded && job.AssignedTo != "" {
					installed[job.AssignedTo] = true
				}
			case models.JobDelete:
				if job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning || job.Status == jobs.StatusQueued {
					summary.Status = "deleting"
					summary.ActiveJobID = job.ID
				}
				if job.Status == jobs.StatusSucceeded && job.AssignedTo != "" {
					delete(installed, job.AssignedTo)
				}
			}
		}
		for nodeID := range installed {
			summary.InstalledOn = append(summary.InstalledOn, nodeID)
		}
		sort.Strings(summary.InstalledOn)
		if len(summary.InstalledOn) > 0 && summary.Status == "available" {
			summary.Status = "installed"
		}
		out = append(out, summary)
	}
	return out
}

func modelCapabilities(model models.Model, nodes []cluster.Node) []ModelCapability {
	out := make([]ModelCapability, 0, len(nodes))
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		item := ModelCapability{
			NodeID: node.ID,
			Name:   node.Name,
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
		if model.VRAMBytes > 0 && !hasAllowedVRAM(node.Resources.GPU, model.VRAMBytes) {
			item.Reasons = append(item.Reasons, fmt.Sprintf("VRAM short by %.1f GB", gbDiff(model.VRAMBytes, maxAllowedVRAM(node.Resources.GPU))))
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

func findModelSummary(summaries []ModelSummary, modelID string) (ModelSummary, bool) {
	for _, summary := range summaries {
		if summary.Model.ID == modelID {
			return summary, true
		}
	}
	return ModelSummary{}, false
}
