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
	Installed     []ModelInstall    `json:"installed,omitempty"`
	ActiveJobID   string            `json:"active_job_id,omitempty"`
	LastJobID     string            `json:"last_job_id,omitempty"`
	LastError     string            `json:"last_error,omitempty"`
	LastUpdated   time.Time         `json:"last_updated,omitempty"`
	CapableNodes  int               `json:"capable_nodes"`
	Capabilities  []ModelCapability `json:"capabilities,omitempty"`
}

type ModelInstall struct {
	NodeID              string                  `json:"node_id"`
	NodeName            string                  `json:"node_name"`
	Path                string                  `json:"path"`
	Bytes               uint64                  `json:"bytes"`
	Family              string                  `json:"family,omitempty"`
	Runtime             string                  `json:"runtime"`
	InstalledAt         time.Time               `json:"installed_at,omitempty"`
	ModelReady          bool                    `json:"model_ready"`
	ModelError          string                  `json:"model_error,omitempty"`
	Repairable          bool                    `json:"repairable"`
	RepairReason        string                  `json:"repair_reason,omitempty"`
	RuntimeReady        bool                    `json:"runtime_ready"`
	RuntimeStatus       cluster.RuntimeResource `json:"runtime_status,omitempty"`
	GenerateReady       bool                    `json:"generate_ready"`
	GenerateBlocked     string                  `json:"generate_blocked,omitempty"`
	AllowedStorageBytes uint64                  `json:"allowed_storage_bytes,omitempty"`
	FreeStorageBytes    uint64                  `json:"free_storage_bytes,omitempty"`
	UsedByModelsBytes   uint64                  `json:"used_by_models_bytes,omitempty"`
	UsedByCacheBytes    uint64                  `json:"used_by_cache_bytes,omitempty"`
	ActiveJobID         string                  `json:"active_job_id,omitempty"`
}

type ModelCapability struct {
	NodeID              string   `json:"node_id"`
	Name                string   `json:"name"`
	Capable             bool     `json:"capable"`
	Installed           bool     `json:"installed,omitempty"`
	AllowedMemoryBytes  uint64   `json:"allowed_memory_bytes"`
	AllowedStorageBytes uint64   `json:"allowed_storage_bytes"`
	FreeStorageBytes    uint64   `json:"free_storage_bytes"`
	AllowedVRAMBytes    uint64   `json:"allowed_vram_bytes,omitempty"`
	ActiveJobs          int      `json:"active_jobs"`
	ActiveJobID         string   `json:"active_job_id,omitempty"`
	JobSlots            int      `json:"job_slots"`
	Reasons             []string `json:"reasons,omitempty"`
}

type ModelCatalogFilters struct {
	Query       string `json:"query,omitempty"`
	Status      string `json:"status,omitempty"`
	Family      string `json:"family,omitempty"`
	CapableOnly bool   `json:"capable_only,omitempty"`
	Sort        string `json:"sort,omitempty"`
}

type ModelPlacementPlan struct {
	ModelID              string            `json:"model_id"`
	Mode                 string            `json:"mode"`
	Feasible             bool              `json:"feasible"`
	RunnableNow          bool              `json:"runnable_now"`
	RequiredMemoryBytes  uint64            `json:"required_memory_bytes"`
	RequiredDiskBytes    uint64            `json:"required_disk_bytes"`
	AggregateMemoryBytes uint64            `json:"aggregate_memory_bytes"`
	AggregateDiskBytes   uint64            `json:"aggregate_disk_bytes"`
	SingleNodeCandidates []ModelCapability `json:"single_node_candidates,omitempty"`
	Shards               []ModelPlanShard  `json:"shards,omitempty"`
	Blockers             []string          `json:"blockers,omitempty"`
	Warnings             []string          `json:"warnings,omitempty"`
}

type ModelPlanShard struct {
	NodeID              string `json:"node_id"`
	NodeName            string `json:"node_name"`
	MemoryBytes         uint64 `json:"memory_bytes"`
	DiskBytes           uint64 `json:"disk_bytes"`
	AllowedMemoryBytes  uint64 `json:"allowed_memory_bytes"`
	AllowedStorageBytes uint64 `json:"allowed_storage_bytes"`
}

func modelSummaries(catalog []models.Model, jobsList []jobs.Job, nodes []cluster.Node) []ModelSummary {
	out := make([]ModelSummary, 0, len(catalog))
	jobsList = append([]jobs.Job(nil), jobsList...)
	sort.Slice(jobsList, func(i, j int) bool {
		return jobsList[i].UpdatedAt.Before(jobsList[j].UpdatedAt)
	})
	for _, model := range catalog {
		installed := make(map[string]bool)
		for _, node := range nodes {
			if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
				continue
			}
			for _, installedModel := range node.Resources.Models {
				if installedModel.ID == model.ID {
					installed[node.ID] = true
				}
			}
		}
		summary := ModelSummary{
			Model:        model,
			Status:       "available",
			Capabilities: modelCapabilities(model, nodes, jobsList, installed),
		}
		summary.CapableNodes = capableModelNodes(summary.Capabilities)
		for _, node := range nodes {
			if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
				continue
			}
			for _, installedModel := range node.Resources.Models {
				if installedModel.ID == model.ID {
					installed[node.ID] = true
					runtimeStatus, runtimeReady := nodeRuntimeStatus(node, string(model.Runtime))
					activeJobID := activeModelJobForNode(jobsList, model.ID, node.ID)
					generateReady, generateBlocked := modelInstallGenerateReadiness(installedModel, runtimeStatus, runtimeReady, activeJobID)
					summary.Installed = append(summary.Installed, ModelInstall{
						NodeID:              node.ID,
						NodeName:            nodeDisplayName(node),
						Path:                installedModel.Path,
						Bytes:               installedModel.Bytes,
						Family:              firstNonEmptyString(installedModel.Family, model.Family),
						Runtime:             firstNonEmptyString(installedModel.Runtime, string(model.Runtime)),
						InstalledAt:         installedModel.InstalledAt,
						ModelReady:          installedModel.Ready,
						ModelError:          installedModel.Error,
						Repairable:          modelInstallRepairable(installedModel),
						RepairReason:        modelInstallRepairReason(installedModel),
						RuntimeReady:        runtimeReady,
						RuntimeStatus:       runtimeStatus,
						GenerateReady:       generateReady,
						GenerateBlocked:     generateBlocked,
						AllowedStorageBytes: node.Resources.Storage.AllowedBytes,
						FreeStorageBytes:    node.Resources.Storage.FreeBytes,
						UsedByModelsBytes:   node.Resources.Storage.UsedByModelsBytes,
						UsedByCacheBytes:    node.Resources.Storage.UsedByCacheBytes,
						ActiveJobID:         activeJobID,
					})
					if generateReady {
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
			case models.JobInstall, models.JobRepair:
				if job.Status == jobs.StatusScheduled || job.Status == jobs.StatusRunning || job.Status == jobs.StatusQueued {
					if job.Type == models.JobRepair {
						summary.Status = "repairing"
					} else {
						summary.Status = "installing"
					}
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
		sort.Slice(summary.Installed, func(i, j int) bool {
			if summary.Installed[i].NodeName == summary.Installed[j].NodeName {
				return summary.Installed[i].NodeID < summary.Installed[j].NodeID
			}
			return summary.Installed[i].NodeName < summary.Installed[j].NodeName
		})
		if len(summary.InstalledOn) > 0 && summary.Status == "available" {
			summary.Status = "installed"
		}
		out = append(out, summary)
	}
	return out
}

func filterAndSortModelSummaries(summaries []ModelSummary, filters ModelCatalogFilters) []ModelSummary {
	filters.Query = strings.TrimSpace(strings.ToLower(filters.Query))
	filters.Status = strings.TrimSpace(strings.ToLower(filters.Status))
	filters.Family = strings.TrimSpace(strings.ToLower(filters.Family))
	filters.Sort = strings.TrimSpace(strings.ToLower(filters.Sort))
	if filters.Sort == "" {
		filters.Sort = "recommended"
	}

	filtered := make([]ModelSummary, 0, len(summaries))
	for _, summary := range summaries {
		if filters.Query != "" && !modelSummaryMatchesQuery(summary, filters.Query) {
			continue
		}
		if filters.Status != "" && strings.ToLower(summary.Status) != filters.Status {
			continue
		}
		if filters.Family != "" && strings.ToLower(summary.Model.Family) != filters.Family {
			continue
		}
		if filters.CapableOnly && summary.CapableNodes == 0 {
			continue
		}
		filtered = append(filtered, summary)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		left := filtered[i]
		right := filtered[j]
		leftName := strings.ToLower(left.Model.Name)
		rightName := strings.ToLower(right.Model.Name)
		switch filters.Sort {
		case "name":
			return leftName < rightName
		case "ram-asc":
			if left.Model.MemoryBytes != right.Model.MemoryBytes {
				return left.Model.MemoryBytes < right.Model.MemoryBytes
			}
		case "ram-desc":
			if left.Model.MemoryBytes != right.Model.MemoryBytes {
				return left.Model.MemoryBytes > right.Model.MemoryBytes
			}
		case "disk-asc":
			if left.Model.DiskBytes != right.Model.DiskBytes {
				return left.Model.DiskBytes < right.Model.DiskBytes
			}
		case "capable-desc":
			if left.CapableNodes != right.CapableNodes {
				return left.CapableNodes > right.CapableNodes
			}
			if left.Model.MemoryBytes != right.Model.MemoryBytes {
				return left.Model.MemoryBytes < right.Model.MemoryBytes
			}
		default:
			if left.CapableNodes != right.CapableNodes {
				return left.CapableNodes > right.CapableNodes
			}
			if left.Model.MemoryBytes != right.Model.MemoryBytes {
				return left.Model.MemoryBytes < right.Model.MemoryBytes
			}
			if left.Model.DiskBytes != right.Model.DiskBytes {
				return left.Model.DiskBytes < right.Model.DiskBytes
			}
		}
		return leftName < rightName
	})

	return filtered
}

func modelSummaryMatchesQuery(summary ModelSummary, query string) bool {
	haystack := strings.ToLower(strings.Join([]string{
		summary.Model.ID,
		summary.Model.Name,
		summary.Model.Family,
		summary.Model.Parameters,
		summary.Model.Quant,
		string(summary.Model.Runtime),
		summary.Model.Description,
	}, " "))
	return strings.Contains(haystack, query)
}

func modelPlacementPlan(summary ModelSummary) ModelPlacementPlan {
	plan := ModelPlacementPlan{
		ModelID:             summary.Model.ID,
		Mode:                "blocked",
		RequiredMemoryBytes: summary.Model.MemoryBytes,
		RequiredDiskBytes:   summary.Model.DiskBytes,
	}
	for _, capability := range summary.Capabilities {
		if capability.Capable {
			plan.SingleNodeCandidates = append(plan.SingleNodeCandidates, capability)
		}
	}
	if len(plan.SingleNodeCandidates) > 0 {
		plan.Mode = "single_worker"
		plan.Feasible = true
		plan.RunnableNow = true
		return plan
	}
	if len(summary.Capabilities) == 0 {
		plan.Blockers = append(plan.Blockers, "no online workers are reporting resources")
		return plan
	}

	candidates := make([]ModelCapability, 0, len(summary.Capabilities))
	for _, capability := range summary.Capabilities {
		if capability.Installed || capability.ActiveJobID != "" {
			continue
		}
		if capability.JobSlots > 0 && capability.ActiveJobs >= capability.JobSlots {
			continue
		}
		capability.AllowedStorageBytes = effectiveShardStorageBytes(capability)
		if capability.AllowedMemoryBytes == 0 || capability.AllowedStorageBytes == 0 {
			continue
		}
		candidates = append(candidates, capability)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].AllowedMemoryBytes != candidates[j].AllowedMemoryBytes {
			return candidates[i].AllowedMemoryBytes > candidates[j].AllowedMemoryBytes
		}
		if candidates[i].AllowedStorageBytes != candidates[j].AllowedStorageBytes {
			return candidates[i].AllowedStorageBytes > candidates[j].AllowedStorageBytes
		}
		return candidates[i].Name < candidates[j].Name
	})

	var selected []ModelCapability
	for _, capability := range candidates {
		selected = append(selected, capability)
		plan.AggregateMemoryBytes += capability.AllowedMemoryBytes
		plan.AggregateDiskBytes += capability.AllowedStorageBytes
		if plan.AggregateMemoryBytes >= summary.Model.MemoryBytes && plan.AggregateDiskBytes >= summary.Model.DiskBytes && len(selected) > 1 {
			break
		}
	}
	if len(selected) > 1 && plan.AggregateMemoryBytes >= summary.Model.MemoryBytes && plan.AggregateDiskBytes >= summary.Model.DiskBytes {
		plan.Mode = "sharded_estimate"
		plan.Feasible = true
		plan.RunnableNow = false
		plan.Warnings = append(plan.Warnings,
			"multi-worker layer sharding is available when selected workers report ready CDIP stage runtimes",
			"real latency will depend on network bandwidth, activation transfer size, and runtime support",
		)
		plan.Shards = buildModelPlanShards(summary.Model, selected)
		return plan
	}

	plan.Blockers = modelPlacementBlockers(summary, plan.AggregateMemoryBytes, plan.AggregateDiskBytes)
	return plan
}

func effectiveShardStorageBytes(capability ModelCapability) uint64 {
	if capability.FreeStorageBytes > 0 && capability.FreeStorageBytes < capability.AllowedStorageBytes {
		return capability.FreeStorageBytes
	}
	return capability.AllowedStorageBytes
}

func buildModelPlanShards(model models.Model, selected []ModelCapability) []ModelPlanShard {
	shards := make([]ModelPlanShard, 0, len(selected))
	remainingMemory := model.MemoryBytes
	remainingDisk := model.DiskBytes
	for index, capability := range selected {
		memoryShare := uint64(0)
		diskShare := uint64(0)
		if index == len(selected)-1 {
			memoryShare = remainingMemory
			diskShare = remainingDisk
		} else {
			memoryShare = minUint64(capability.AllowedMemoryBytes, remainingMemory)
			diskShare = minUint64(capability.AllowedStorageBytes, remainingDisk)
		}
		if memoryShare > remainingMemory {
			memoryShare = remainingMemory
		}
		if diskShare > remainingDisk {
			diskShare = remainingDisk
		}
		remainingMemory -= memoryShare
		remainingDisk -= diskShare
		shards = append(shards, ModelPlanShard{
			NodeID:              capability.NodeID,
			NodeName:            capability.Name,
			MemoryBytes:         memoryShare,
			DiskBytes:           diskShare,
			AllowedMemoryBytes:  capability.AllowedMemoryBytes,
			AllowedStorageBytes: capability.AllowedStorageBytes,
		})
	}
	return shards
}

func modelPlacementBlockers(summary ModelSummary, aggregateMemoryBytes uint64, aggregateDiskBytes uint64) []string {
	blockers := make([]string, 0, 4)
	if aggregateMemoryBytes < summary.Model.MemoryBytes {
		blockers = append(blockers, fmt.Sprintf("aggregate RAM short by %.1f GB", gbDiff(summary.Model.MemoryBytes, aggregateMemoryBytes)))
	}
	if aggregateDiskBytes < summary.Model.DiskBytes {
		blockers = append(blockers, fmt.Sprintf("aggregate disk short by %.1f GB", gbDiff(summary.Model.DiskBytes, aggregateDiskBytes)))
	}
	if len(blockers) == 0 {
		reasons := make([]string, 0, len(summary.Capabilities))
		for _, capability := range summary.Capabilities {
			if len(capability.Reasons) == 0 {
				continue
			}
			reasons = append(reasons, capability.Name+": "+strings.Join(capability.Reasons, "; "))
		}
		sort.Strings(reasons)
		blockers = append(blockers, reasons...)
	}
	if len(blockers) == 0 {
		blockers = append(blockers, "no viable placement found")
	}
	return blockers
}

func minUint64(left uint64, right uint64) uint64 {
	if left < right {
		return left
	}
	return right
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func modelInstallRepairable(model cluster.ModelResource) bool {
	return !model.Ready || strings.TrimSpace(model.Error) != ""
}

func modelInstallRepairReason(model cluster.ModelResource) string {
	if strings.TrimSpace(model.Error) != "" {
		return model.Error
	}
	if !model.Ready {
		return "model files are not ready"
	}
	return ""
}

func modelInstallGenerateReadiness(model cluster.ModelResource, runtimeStatus cluster.RuntimeResource, runtimeReady bool, activeJobID string) (bool, string) {
	if !model.Ready {
		if strings.TrimSpace(model.Error) != "" {
			return false, "model files are not ready: " + model.Error
		}
		return false, "model files are not ready"
	}
	if !runtimeReady {
		if strings.TrimSpace(runtimeStatus.Error) != "" {
			return false, "runtime is not ready: " + runtimeStatus.Error
		}
		return false, "runtime is not ready"
	}
	if strings.TrimSpace(activeJobID) != "" {
		return false, "model has an active job on this worker: " + activeJobID
	}
	return true, ""
}

func nodeRuntimeReady(node cluster.Node, runtimeName string) bool {
	_, ready := nodeRuntimeStatus(node, runtimeName)
	return ready
}

func nodeRuntimeStatus(node cluster.Node, runtimeName string) (cluster.RuntimeResource, bool) {
	runtimeName = strings.TrimSpace(runtimeName)
	if runtimeName == "" {
		return cluster.RuntimeResource{Ready: true}, true
	}
	for _, runtime := range node.Resources.Runtimes {
		if runtime.Name == runtimeName && runtime.Ready {
			return runtime, true
		}
		if runtime.Name == runtimeName {
			return runtime, false
		}
	}
	return cluster.RuntimeResource{Name: runtimeName, Error: "runtime not reported"}, false
}

func nodeDisplayName(node cluster.Node) string {
	if strings.TrimSpace(node.Name) != "" {
		return node.Name
	}
	return node.ID
}

func modelCapabilities(model models.Model, nodes []cluster.Node, jobsList []jobs.Job, installed map[string]bool) []ModelCapability {
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
			ActiveJobID:         activeModelJobForNode(jobsList, model.ID, node.ID),
			JobSlots:            workerJobSlots(node),
		}
		item.Installed = installed[node.ID]
		if strings.TrimSpace(item.Name) == "" {
			item.Name = node.ID
		}
		if item.Installed {
			item.Reasons = append(item.Reasons, "already installed")
		}
		if item.ActiveJobID != "" {
			item.Reasons = append(item.Reasons, "model job already active: "+item.ActiveJobID)
		}
		if node.Resources.Memory.AllowedBytes < model.MemoryBytes {
			item.Reasons = append(item.Reasons, fmt.Sprintf("RAM short by %.1f GB", gbDiff(model.MemoryBytes, node.Resources.Memory.AllowedBytes)))
		}
		if node.Resources.Storage.AllowedBytes < model.DiskBytes {
			item.Reasons = append(item.Reasons, fmt.Sprintf("disk short by %.1f GB", gbDiff(model.DiskBytes, node.Resources.Storage.AllowedBytes)))
		}
		if node.Resources.Storage.AllowedBytes > 0 && node.Resources.Storage.UsedByModelsBytes > 0 && node.Resources.Storage.UsedByModelsBytes+model.DiskBytes > node.Resources.Storage.AllowedBytes {
			remaining := uint64(0)
			if node.Resources.Storage.AllowedBytes > node.Resources.Storage.UsedByModelsBytes {
				remaining = node.Resources.Storage.AllowedBytes - node.Resources.Storage.UsedByModelsBytes
			}
			item.Reasons = append(item.Reasons, fmt.Sprintf("model quota short by %.1f GB", gbDiff(model.DiskBytes, remaining)))
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

func modelGenerateBlockedReason(summary ModelSummary, nodeID string) string {
	for _, install := range summary.Installed {
		if install.NodeID != nodeID {
			continue
		}
		if install.GenerateBlocked != "" {
			return install.GenerateBlocked
		}
		if !install.ModelReady {
			if install.ModelError != "" {
				return "model files are not ready on the selected worker: " + install.ModelError
			}
			return "model files are not ready on the selected worker"
		}
		if !install.RuntimeReady {
			if install.RuntimeStatus.Error != "" {
				return "model runtime is not ready on the selected worker: " + install.RuntimeStatus.Error
			}
			return "model runtime is not ready on the selected worker"
		}
		return ""
	}
	return "model is not installed on the selected worker"
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
