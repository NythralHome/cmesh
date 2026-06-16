package manager

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
)

type ModelSummary struct {
	Model        models.Model `json:"model"`
	Status       string       `json:"status"`
	InstalledOn  []string     `json:"installed_on"`
	ActiveJobID  string       `json:"active_job_id,omitempty"`
	LastJobID    string       `json:"last_job_id,omitempty"`
	LastError    string       `json:"last_error,omitempty"`
	LastUpdated  time.Time    `json:"last_updated,omitempty"`
	CapableNodes int          `json:"capable_nodes"`
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
			CapableNodes: capableModelNodes(model, nodes),
		}
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

func capableModelNodes(model models.Model, nodes []cluster.Node) int {
	var total int
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		if node.Resources.Memory.AllowedBytes >= model.MemoryBytes &&
			node.Resources.Storage.AllowedBytes >= model.DiskBytes {
			total++
		}
	}
	return total
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
