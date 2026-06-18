package manager

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
)

const distributedStageOverheadBytes = 512 * 1024 * 1024

type DistributedModelPlan struct {
	ModelID                   string                  `json:"model_id"`
	Mode                      string                  `json:"mode"`
	Runtime                   string                  `json:"runtime"`
	Feasible                  bool                    `json:"feasible"`
	ExecutableNow             bool                    `json:"executable_now"`
	TotalLayers               int                     `json:"total_layers"`
	RequiredMemoryBytes       uint64                  `json:"required_memory_bytes"`
	RequiredDiskBytes         uint64                  `json:"required_disk_bytes"`
	AggregateMemoryBytes      uint64                  `json:"aggregate_memory_bytes"`
	AggregateDiskBytes        uint64                  `json:"aggregate_disk_bytes"`
	StageOverheadBytes        uint64                  `json:"stage_overhead_bytes"`
	Stages                    []DistributedPlanStage  `json:"stages,omitempty"`
	Network                   DistributedNetworkPlan  `json:"network"`
	EstimatedLatency          DistributedLatencyModel `json:"estimated_latency"`
	Blockers                  []string                `json:"blockers,omitempty"`
	Warnings                  []string                `json:"warnings,omitempty"`
	NextImplementationTargets []string                `json:"next_implementation_targets,omitempty"`
}

type DistributedPlanStage struct {
	Index               int    `json:"index"`
	NodeID              string `json:"node_id"`
	NodeName            string `json:"node_name"`
	LayerStart          int    `json:"layer_start"`
	LayerEnd            int    `json:"layer_end"`
	Layers              int    `json:"layers"`
	MemoryBytes         uint64 `json:"memory_bytes"`
	DiskBytes           uint64 `json:"disk_bytes"`
	AllowedMemoryBytes  uint64 `json:"allowed_memory_bytes"`
	AllowedStorageBytes uint64 `json:"allowed_storage_bytes"`
	RuntimeReady        bool   `json:"runtime_ready"`
	Installed           bool   `json:"installed"`
}

type DistributedNetworkPlan struct {
	InterStageHops             int `json:"inter_stage_hops"`
	AssumedInterStageLatencyMS int `json:"assumed_inter_stage_latency_ms"`
}

type DistributedLatencyModel struct {
	FirstTokenMS       int    `json:"first_token_ms"`
	PerOutputTokenMS   int    `json:"per_output_token_ms"`
	Confidence         string `json:"confidence"`
	Assumption         string `json:"assumption"`
	PipelinePenaltyPct int    `json:"pipeline_penalty_pct"`
}

func distributedModelPlan(model models.Model, nodes []cluster.Node) DistributedModelPlan {
	plan := DistributedModelPlan{
		ModelID:             model.ID,
		Mode:                "pipeline_layers",
		Runtime:             string(model.Runtime),
		TotalLayers:         estimatedModelLayers(model),
		RequiredMemoryBytes: model.MemoryBytes,
		RequiredDiskBytes:   model.DiskBytes,
		StageOverheadBytes:  distributedStageOverheadBytes,
		Network: DistributedNetworkPlan{
			AssumedInterStageLatencyMS: 80,
		},
		EstimatedLatency: DistributedLatencyModel{
			Confidence: "low",
			Assumption: "planning estimate before the distributed runtime protocol exists",
		},
		NextImplementationTargets: []string{
			"worker-to-worker transport for activation tensors",
			"model shard materialization per layer range",
			"distributed generate job coordinator",
			"streaming partial-token protocol with cancellation",
		},
	}

	candidates := distributedCandidates(model, nodes)
	for _, candidate := range candidates {
		plan.AggregateMemoryBytes += candidate.Resources.Memory.AllowedBytes
		plan.AggregateDiskBytes += effectiveNodeStorage(candidate)
	}
	if len(candidates) == 0 {
		plan.Blockers = append(plan.Blockers, "no online workers are reporting resources")
		return plan
	}
	if len(candidates) < 2 {
		plan.Blockers = append(plan.Blockers, "distributed layer split needs at least 2 online workers")
	}
	if plan.AggregateMemoryBytes < model.MemoryBytes {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("aggregate RAM short by %.1f GB", gbDiff(model.MemoryBytes, plan.AggregateMemoryBytes)))
	}
	if plan.AggregateDiskBytes < model.DiskBytes {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("aggregate disk short by %.1f GB", gbDiff(model.DiskBytes, plan.AggregateDiskBytes)))
	}
	if len(plan.Blockers) > 0 {
		return plan
	}

	selected := selectDistributedWorkers(model, candidates)
	if len(selected) < 2 {
		plan.Blockers = append(plan.Blockers, "no multi-worker stage set can hold the model")
		return plan
	}
	plan.Stages = buildDistributedStages(model, selected, plan.TotalLayers)
	plan.Feasible = len(plan.Stages) >= 2
	plan.Network.InterStageHops = maxInt(0, len(plan.Stages)-1)
	plan.EstimatedLatency = estimateDistributedLatency(model, len(plan.Stages), plan.Network.AssumedInterStageLatencyMS)
	if !allStagesRuntimeReady(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "one or more selected workers do not report a ready runtime")
	}
	if !allStagesInstalled(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "model shards are not installed on all selected workers")
	}
	plan.Blockers = append(plan.Blockers, "distributed runtime protocol is not implemented yet")
	plan.ExecutableNow = false
	return plan
}

func distributedCandidates(model models.Model, nodes []cluster.Node) []cluster.Node {
	out := make([]cluster.Node, 0, len(nodes))
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		if node.Resources.Memory.AllowedBytes <= distributedStageOverheadBytes {
			continue
		}
		if effectiveNodeStorage(node) == 0 {
			continue
		}
		out = append(out, node)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i].Resources.Memory.AllowedBytes
		right := out[j].Resources.Memory.AllowedBytes
		if left != right {
			return left > right
		}
		return nodeDisplayName(out[i]) < nodeDisplayName(out[j])
	})
	return out
}

func selectDistributedWorkers(model models.Model, candidates []cluster.Node) []cluster.Node {
	selected := make([]cluster.Node, 0, len(candidates))
	var memory uint64
	var disk uint64
	for _, candidate := range candidates {
		selected = append(selected, candidate)
		memory += candidate.Resources.Memory.AllowedBytes - distributedStageOverheadBytes
		disk += effectiveNodeStorage(candidate)
		if len(selected) >= 2 && memory >= model.MemoryBytes && disk >= model.DiskBytes {
			return selected
		}
	}
	return nil
}

func buildDistributedStages(model models.Model, nodes []cluster.Node, totalLayers int) []DistributedPlanStage {
	if totalLayers <= 0 {
		totalLayers = 1
	}
	weights := make([]uint64, 0, len(nodes))
	var totalWeight uint64
	for _, node := range nodes {
		weight := node.Resources.Memory.AllowedBytes - distributedStageOverheadBytes
		weights = append(weights, weight)
		totalWeight += weight
	}
	stages := make([]DistributedPlanStage, 0, len(nodes))
	nextLayer := 0
	for index, node := range nodes {
		layers := 1
		if index == len(nodes)-1 {
			layers = totalLayers - nextLayer
		} else if totalWeight > 0 {
			layers = int(math.Round(float64(totalLayers) * float64(weights[index]) / float64(totalWeight)))
			if layers < 1 {
				layers = 1
			}
			maxAllowed := totalLayers - nextLayer - (len(nodes) - index - 1)
			if layers > maxAllowed {
				layers = maxAllowed
			}
		}
		layerStart := nextLayer
		layerEnd := nextLayer + layers - 1
		nextLayer += layers
		stage := DistributedPlanStage{
			Index:               index,
			NodeID:              node.ID,
			NodeName:            nodeDisplayName(node),
			LayerStart:          layerStart,
			LayerEnd:            layerEnd,
			Layers:              layers,
			MemoryBytes:         proportionalBytes(model.MemoryBytes, layers, totalLayers) + distributedStageOverheadBytes,
			DiskBytes:           proportionalBytes(model.DiskBytes, layers, totalLayers),
			AllowedMemoryBytes:  node.Resources.Memory.AllowedBytes,
			AllowedStorageBytes: effectiveNodeStorage(node),
			RuntimeReady:        nodeRuntimeReady(node, string(model.Runtime)),
			Installed:           nodeHasModel(node, model.ID),
		}
		stages = append(stages, stage)
	}
	return stages
}

func estimatedModelLayers(model models.Model) int {
	params := strings.ToUpper(strings.TrimSpace(model.Parameters))
	params = strings.TrimSuffix(params, "B")
	value, err := strconv.ParseFloat(params, 64)
	if err != nil || value <= 0 {
		return 32
	}
	switch {
	case value <= 1.5:
		return 24
	case value <= 4:
		return 32
	case value <= 8:
		return 32
	case value <= 15:
		return 48
	case value <= 28:
		return 56
	default:
		return 64
	}
}

func estimateDistributedLatency(model models.Model, stages int, interStageLatencyMS int) DistributedLatencyModel {
	if stages < 1 {
		stages = 1
	}
	basePerToken := 120
	params := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(model.Parameters), "B"))
	if value, err := strconv.ParseFloat(params, 64); err == nil {
		basePerToken = 80 + int(value*18)
	}
	networkPenalty := maxInt(0, stages-1) * interStageLatencyMS
	pipelinePenalty := maxInt(15, (stages-1)*18)
	perToken := basePerToken + networkPenalty + (basePerToken*pipelinePenalty)/100
	return DistributedLatencyModel{
		FirstTokenMS:       perToken*3 + networkPenalty,
		PerOutputTokenMS:   perToken,
		Confidence:         "low",
		Assumption:         "single prompt stream over pipeline-parallel workers with one activation hop per stage",
		PipelinePenaltyPct: pipelinePenalty,
	}
}

func effectiveNodeStorage(node cluster.Node) uint64 {
	if node.Resources.Storage.FreeBytes > 0 && node.Resources.Storage.FreeBytes < node.Resources.Storage.AllowedBytes {
		return node.Resources.Storage.FreeBytes
	}
	return node.Resources.Storage.AllowedBytes
}

func nodeHasModel(node cluster.Node, modelID string) bool {
	for _, installed := range node.Resources.Models {
		if installed.ID == modelID && installed.Ready {
			return true
		}
	}
	return false
}

func allStagesRuntimeReady(stages []DistributedPlanStage) bool {
	for _, stage := range stages {
		if !stage.RuntimeReady {
			return false
		}
	}
	return len(stages) > 0
}

func allStagesInstalled(stages []DistributedPlanStage) bool {
	for _, stage := range stages {
		if !stage.Installed {
			return false
		}
	}
	return len(stages) > 0
}

func proportionalBytes(total uint64, part int, whole int) uint64 {
	if whole <= 0 || part <= 0 || total == 0 {
		return 0
	}
	return uint64(math.Ceil(float64(total) * float64(part) / float64(whole)))
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func distributedStageJobRequests(parent jobs.Job, input models.DistributedGenerateInput) ([]jobs.CreateRequest, error) {
	if parent.ID == "" {
		return nil, fmt.Errorf("parent job id is required")
	}
	if strings.TrimSpace(input.ModelID) == "" {
		return nil, fmt.Errorf("model_id is required")
	}
	if len(input.Stages) < 2 {
		return nil, fmt.Errorf("distributed generate requires at least 2 stages")
	}
	out := make([]jobs.CreateRequest, 0, len(input.Stages))
	for index, stage := range input.Stages {
		if strings.TrimSpace(stage.NodeID) == "" {
			return nil, fmt.Errorf("stage %d node_id is required", index)
		}
		if stage.Index != index {
			return nil, fmt.Errorf("stage index mismatch: got %d at position %d", stage.Index, index)
		}
		stageInput := models.DistributedStageJobInput{
			ParentJobID:    parent.ID,
			ModelID:        input.ModelID,
			ConversationID: input.ConversationID,
			Stage:          stage,
			Prompt:         input.Prompt,
			Messages:       input.Messages,
			SystemPrompt:   input.SystemPrompt,
			MaxTokens:      input.MaxTokens,
			Temperature:    input.Temperature,
		}
		if index > 0 {
			stageInput.UpstreamNodeID = input.Stages[index-1].NodeID
		}
		if index < len(input.Stages)-1 {
			stageInput.DownstreamNodeID = input.Stages[index+1].NodeID
		}
		body, err := json.Marshal(stageInput)
		if err != nil {
			return nil, err
		}
		out = append(out, jobs.CreateRequest{
			Type:        models.JobGenerateStage,
			Input:       string(body),
			RequestedBy: "distributed-coordinator:" + parent.ID,
			AssignedTo:  stage.NodeID,
			Requirements: jobs.Requirements{
				CPUCores: 1,
			},
			MaxAttempts:     1,
			NoAutoAssign:    true,
			CDIPState:       cdip.StagePlanned,
			CDIPParentJobID: parent.ID,
			CDIPStageIndex:  stage.Index,
		})
	}
	return out, nil
}
