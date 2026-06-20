package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/runtimes"
	"github.com/cmesh/cmesh/internal/transport"
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
	AggregateStageMemoryBytes uint64                  `json:"aggregate_stage_memory_bytes"`
	AggregateDiskBytes        uint64                  `json:"aggregate_disk_bytes"`
	StageOverheadBytes        uint64                  `json:"stage_overhead_bytes"`
	Stages                    []DistributedPlanStage  `json:"stages,omitempty"`
	Placement                 DistributedPlacement    `json:"placement"`
	Network                   DistributedNetworkPlan  `json:"network"`
	EstimatedLatency          DistributedLatencyModel `json:"estimated_latency"`
	StageRuntimeDiagnostics   DistributedRuntimeDiag  `json:"stage_runtime_diagnostics"`
	Blockers                  []string                `json:"blockers,omitempty"`
	Warnings                  []string                `json:"warnings,omitempty"`
	NextImplementationTargets []string                `json:"next_implementation_targets,omitempty"`
}

type DistributedRuntimeDiag struct {
	CandidateWorkers       int                         `json:"candidate_workers"`
	RuntimeReadyWorkers    int                         `json:"runtime_ready_workers"`
	StageReadyWorkers      int                         `json:"stage_ready_workers"`
	LogicalStageWorkers    int                         `json:"logical_stage_workers"`
	LlamaCPPStageWorkers   int                         `json:"llama_cpp_stage_workers"`
	ResidentStageWorkers   int                         `json:"resident_stage_workers"`
	MissingStageCapability []DistributedRuntimeMissing `json:"missing_stage_capability,omitempty"`
}

type DistributedRuntimeMissing struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Reason   string `json:"reason"`
}

type DistributedPlanStage struct {
	Index               int      `json:"index"`
	NodeID              string   `json:"node_id"`
	NodeName            string   `json:"node_name"`
	LayerStart          int      `json:"layer_start"`
	LayerEnd            int      `json:"layer_end"`
	Layers              int      `json:"layers"`
	MemoryBytes         uint64   `json:"memory_bytes"`
	ModelMemoryBytes    uint64   `json:"model_memory_bytes"`
	OverheadMemoryBytes uint64   `json:"overhead_memory_bytes"`
	DiskBytes           uint64   `json:"disk_bytes"`
	AllowedMemoryBytes  uint64   `json:"allowed_memory_bytes"`
	AllowedStorageBytes uint64   `json:"allowed_storage_bytes"`
	RuntimeReady        bool     `json:"runtime_ready"`
	RuntimeCapabilities []string `json:"runtime_capabilities,omitempty"`
	StageRuntime        string   `json:"stage_runtime,omitempty"`
	StageRuntimeReady   bool     `json:"stage_runtime_ready"`
	StageRuntimeReason  string   `json:"stage_runtime_reason,omitempty"`
	StageDaemonURL      string   `json:"stage_daemon_url,omitempty"`
	Installed           bool     `json:"installed"`
}

type DistributedPlacement struct {
	Strategy      string                          `json:"strategy"`
	TotalLayers   int                             `json:"total_layers"`
	ModelMemory   uint64                          `json:"model_memory_bytes"`
	ModelDisk     uint64                          `json:"model_disk_bytes"`
	StageOverhead uint64                          `json:"stage_overhead_bytes"`
	Candidates    []DistributedPlacementCandidate `json:"candidates,omitempty"`
}

type DistributedPlacementCandidate struct {
	NodeID                    string `json:"node_id"`
	NodeName                  string `json:"node_name"`
	Selected                  bool   `json:"selected"`
	LayerCapacity             int    `json:"layer_capacity"`
	AssignedLayers            int    `json:"assigned_layers,omitempty"`
	AllowedMemoryBytes        uint64 `json:"allowed_memory_bytes"`
	EffectiveStageMemoryBytes uint64 `json:"effective_stage_memory_bytes"`
	AssignedMemoryBytes       uint64 `json:"assigned_memory_bytes,omitempty"`
	RemainingMemoryBytes      uint64 `json:"remaining_memory_bytes,omitempty"`
	AllowedStorageBytes       uint64 `json:"allowed_storage_bytes"`
	EffectiveStorageBytes     uint64 `json:"effective_storage_bytes"`
	AssignedDiskBytes         uint64 `json:"assigned_disk_bytes,omitempty"`
	RemainingStorageBytes     uint64 `json:"remaining_storage_bytes,omitempty"`
	BlockedReason             string `json:"blocked_reason,omitempty"`
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

type CDIPMockRunResult struct {
	ParentJob jobs.Job   `json:"parent_job"`
	StageJobs []jobs.Job `json:"stage_jobs"`
	Output    string     `json:"output"`
}

type CDIPPrepareResult struct {
	ParentJob jobs.Job            `json:"parent_job"`
	StageJobs []jobs.Job          `json:"stage_jobs"`
	Messages  []cdip.StagePrepare `json:"messages"`
}

type CDIPCommandResult struct {
	ParentJob        jobs.Job               `json:"parent_job"`
	StageJobs        []jobs.Job             `json:"stage_jobs"`
	Messages         []cdip.StageCommand    `json:"messages"`
	ActivationFrames []cdip.ActivationChunk `json:"activation_frames,omitempty"`
	Trace            *CDIPDecodeLoopTrace   `json:"trace,omitempty"`
}

type CDIPDecodeLoopResult struct {
	ParentJob jobs.Job                  `json:"parent_job"`
	StageJobs []jobs.Job                `json:"stage_jobs"`
	Messages  []cdip.StageCommand       `json:"messages"`
	Chunks    []CDIPTerminalDecodeChunk `json:"chunks"`
	Trace     CDIPDecodeLoopTrace       `json:"trace"`
	Output    string                    `json:"output"`
	Final     bool                      `json:"final"`
}

type CDIPDecodeLoopTrace struct {
	Protocol           string   `json:"protocol"`
	Version            string   `json:"version"`
	SessionID          string   `json:"session_id"`
	KVCacheKey         string   `json:"kv_cache_key"`
	Mode               string   `json:"mode"`
	MaxTokens          int      `json:"max_tokens"`
	StageCount         int      `json:"stage_count"`
	StageJobIDs        []string `json:"stage_job_ids"`
	TerminalStageJobID string   `json:"terminal_stage_job_id"`
	Guardrail          string   `json:"guardrail"`
}

type CDIPTerminalDecodeChunk struct {
	Step          uint64 `json:"step"`
	StageJobID    string `json:"stage_job_id"`
	StageIndex    int    `json:"stage_index"`
	KVCacheKey    string `json:"kv_cache_key,omitempty"`
	NextTokenID   int    `json:"next_token_id"`
	NextTokenText string `json:"next_token_text"`
	Tokens        []int  `json:"tokens"`
	Output        string `json:"output"`
	Final         bool   `json:"final"`
}

type CDIPAdvanceResult struct {
	ParentJob        jobs.Job               `json:"parent_job"`
	StageJobs        []jobs.Job             `json:"stage_jobs"`
	Action           string                 `json:"action"`
	Waiting          bool                   `json:"waiting"`
	Reason           string                 `json:"reason,omitempty"`
	PrepareMessages  []cdip.StagePrepare    `json:"prepare_messages,omitempty"`
	Messages         []cdip.StageCommand    `json:"messages,omitempty"`
	ActivationFrames []cdip.ActivationChunk `json:"activation_frames,omitempty"`
}

func prepareCDIPDistributedJob(store Store, parentJobID string) (CDIPPrepareResult, error) {
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPPrepareResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPPrepareResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPPrepareResult{}, fmt.Errorf("distributed parent job has no stage graph")
	}
	messages := make([]cdip.StagePrepare, 0, len(stages))
	for _, stageJob := range stages {
		var input models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &input); err != nil {
			return CDIPPrepareResult{}, fmt.Errorf("invalid stage job input for %s: %w", stageJob.ID, err)
		}
		if input.ParentJobID != parent.ID {
			return CDIPPrepareResult{}, fmt.Errorf("stage job %s belongs to %s, expected %s", stageJob.ID, input.ParentJobID, parent.ID)
		}
		msg := cdip.StagePrepare{
			Envelope:         cdip.NewEnvelope(cdip.MessageStagePrepare),
			ParentJobID:      parent.ID,
			StageJobID:       stageJob.ID,
			ModelID:          input.ModelID,
			Stage:            input.Shard.Stage,
			UpstreamNodeID:   input.UpstreamNodeID,
			DownstreamNodeID: input.DownstreamNodeID,
		}
		if err := msg.Validate(); err != nil {
			return CDIPPrepareResult{}, fmt.Errorf("invalid stage.prepare for %s: %w", stageJob.ID, err)
		}
		messages = append(messages, msg)
	}
	for _, stageJob := range stages {
		if _, ok := store.UpdateCDIPStageState(stageJob.ID, cdip.StagePreparing, "coordinator sent stage.prepare"); !ok {
			return CDIPPrepareResult{}, fmt.Errorf("failed to prepare stage %s", stageJob.ID)
		}
	}
	return CDIPPrepareResult{
		ParentJob: parent,
		StageJobs: cdipStageJobsForParent(store.Jobs(), parent.ID),
		Messages:  messages,
	}, nil
}

func advanceCDIPDistributedJob(store Store, bus transport.ActivationTransport, parentJobID string, step uint64, output string) (CDIPAdvanceResult, error) {
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPAdvanceResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPAdvanceResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPAdvanceResult{}, fmt.Errorf("distributed parent job has no stage graph")
	}
	if allCDIPStagesIn(stages, cdip.StagePlanned) {
		result, err := prepareCDIPDistributedJob(store, parent.ID)
		if err != nil {
			return CDIPAdvanceResult{}, err
		}
		return CDIPAdvanceResult{
			ParentJob:       result.ParentJob,
			StageJobs:       result.StageJobs,
			Action:          "prepare",
			PrepareMessages: result.Messages,
		}, nil
	}
	if allCDIPStagesIn(stages, cdip.StageReady) {
		result, err := startCDIPPrefill(store, parent.ID)
		if err != nil {
			return CDIPAdvanceResult{}, err
		}
		return CDIPAdvanceResult{
			ParentJob: result.ParentJob,
			StageJobs: result.StageJobs,
			Action:    "prefill",
			Messages:  result.Messages,
		}, nil
	}
	if allCDIPStagesIn(stages, cdip.StagePrefill) {
		result, err := startCDIPDecode(store, bus, parent.ID, step)
		if err != nil {
			return CDIPAdvanceResult{}, err
		}
		return CDIPAdvanceResult{
			ParentJob:        result.ParentJob,
			StageJobs:        result.StageJobs,
			Action:           "decode",
			Messages:         result.Messages,
			ActivationFrames: result.ActivationFrames,
		}, nil
	}
	if allCDIPStagesIn(stages, cdip.StageDecode) {
		result, err := completeCDIPDistributedJob(store, parent.ID, output)
		if err != nil {
			return CDIPAdvanceResult{}, err
		}
		return CDIPAdvanceResult{
			ParentJob: result.ParentJob,
			StageJobs: result.StageJobs,
			Action:    "complete",
			Messages:  result.Messages,
		}, nil
	}
	return CDIPAdvanceResult{
		ParentJob: parent,
		StageJobs: stages,
		Action:    "wait",
		Waiting:   true,
		Reason:    "waiting for stages to reach the same coordinator boundary",
	}, nil
}

func allCDIPStagesIn(stages []jobs.Job, state cdip.StageState) bool {
	if len(stages) == 0 {
		return false
	}
	for _, stage := range stages {
		current := stage.CDIPState
		if current == "" {
			current = cdip.StagePlanned
		}
		if current != state {
			return false
		}
	}
	return true
}

func startCDIPPrefill(store Store, parentJobID string) (CDIPCommandResult, error) {
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job has no stage graph")
	}
	for _, stageJob := range stages {
		if stageJob.CDIPState != cdip.StageReady {
			return CDIPCommandResult{}, fmt.Errorf("stage %s is %s, expected %s", stageJob.ID, stageJob.CDIPState, cdip.StageReady)
		}
	}
	messages := make([]cdip.StageCommand, 0, len(stages))
	for _, stageJob := range stages {
		msg := cdip.StageCommand{
			Envelope:    cdip.NewEnvelope(cdip.MessageStagePrefill),
			ParentJobID: parent.ID,
			StageJobID:  stageJob.ID,
			StageIndex:  stageJob.CDIPStageIndex,
		}
		if err := msg.Validate(cdip.MessageStagePrefill); err != nil {
			return CDIPCommandResult{}, fmt.Errorf("invalid stage.prefill for %s: %w", stageJob.ID, err)
		}
		messages = append(messages, msg)
	}
	for _, stageJob := range stages {
		if _, ok := store.UpdateCDIPStageState(stageJob.ID, cdip.StagePrefill, "coordinator sent stage.prefill"); !ok {
			return CDIPCommandResult{}, fmt.Errorf("failed to prefill stage %s", stageJob.ID)
		}
	}
	return CDIPCommandResult{
		ParentJob: parent,
		StageJobs: cdipStageJobsForParent(store.Jobs(), parent.ID),
		Messages:  messages,
	}, nil
}

func startCDIPDecode(store Store, bus transport.ActivationTransport, parentJobID string, step uint64) (CDIPCommandResult, error) {
	if step == 0 {
		step = 1
	}
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job has no stage graph")
	}
	for _, stageJob := range stages {
		if stageJob.CDIPState != cdip.StagePrefill {
			return CDIPCommandResult{}, fmt.Errorf("stage %s is %s, expected %s", stageJob.ID, stageJob.CDIPState, cdip.StagePrefill)
		}
	}
	messages := make([]cdip.StageCommand, 0, len(stages))
	for _, stageJob := range stages {
		msg := cdip.StageCommand{
			Envelope:    cdip.NewEnvelope(cdip.MessageStageDecode),
			ParentJobID: parent.ID,
			StageJobID:  stageJob.ID,
			StageIndex:  stageJob.CDIPStageIndex,
			Step:        step,
		}
		if err := msg.Validate(cdip.MessageStageDecode); err != nil {
			return CDIPCommandResult{}, fmt.Errorf("invalid stage.decode for %s: %w", stageJob.ID, err)
		}
		messages = append(messages, msg)
	}
	ctx := context.Background()
	frames := make([]cdip.ActivationChunk, 0, len(stages)-1)
	for i := 0; i < len(stages)-1; i++ {
		upstream := stages[i]
		downstream := stages[i+1]
		stream := transport.StreamID{ParentJobID: parent.ID, StageJobID: upstream.ID}
		reader, err := bus.OpenReader(ctx, stream)
		if err != nil {
			return CDIPCommandResult{}, err
		}
		payload := []byte{byte(i), byte(step)}
		stageRuntime := runtimes.NewLogicalStageRuntime(string(models.RuntimeLlamaCPP))
		result, err := stageRuntime.DecodeStage(ctx, runtimes.StageCommandRequest{
			ParentJobID:          parent.ID,
			StageJobID:           upstream.ID,
			StageIndex:           upstream.CDIPStageIndex,
			Step:                 step,
			ActivationTransport:  bus,
			UpstreamStageJobID:   upstream.ID,
			DownstreamStageJobID: downstream.ID,
			DownstreamNodeID:     downstream.AssignedTo,
			ActivationPayload:    payload,
			ActivationEncoding:   "mock",
			ActivationShape:      []int{1, 1, len(payload)},
			ActivationDType:      "u8",
			ActivationChecksum:   fmt.Sprintf("mock:%d:%d:%s:%s", step, i, upstream.ID, downstream.ID),
		})
		if err != nil {
			return CDIPCommandResult{}, err
		}
		received, err := reader.Receive(ctx)
		if err != nil {
			return CDIPCommandResult{}, err
		}
		if err := received.Validate(); err != nil {
			return CDIPCommandResult{}, err
		}
		if result.ActivationFrame == nil || received.Header.Checksum != result.ActivationFrame.Checksum {
			return CDIPCommandResult{}, fmt.Errorf("activation frame relay mismatch for stage %s", upstream.ID)
		}
		frames = append(frames, received.Header)
	}
	for _, stageJob := range stages {
		if _, ok := store.UpdateCDIPStageState(stageJob.ID, cdip.StageDecode, "coordinator sent stage.decode"); !ok {
			return CDIPCommandResult{}, fmt.Errorf("failed to decode stage %s", stageJob.ID)
		}
	}
	return CDIPCommandResult{
		ParentJob:        parent,
		StageJobs:        cdipStageJobsForParent(store.Jobs(), parent.ID),
		Messages:         messages,
		ActivationFrames: frames,
	}, nil
}

func startCDIPRelayDecode(store Store, parentJobID string, step uint64) (CDIPCommandResult, error) {
	if step == 0 {
		step = 1
	}
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPCommandResult{}, fmt.Errorf("relay decode requires at least 2 stages: source and terminal")
	}
	for _, stageJob := range stages {
		if stageJob.CDIPState != cdip.StagePrefill {
			return CDIPCommandResult{}, fmt.Errorf("stage %s is %s, expected %s", stageJob.ID, stageJob.CDIPState, cdip.StagePrefill)
		}
	}
	messages := make([]cdip.StageCommand, 0, len(stages))
	for _, stageJob := range stages {
		msg := cdip.StageCommand{
			Envelope:    cdip.NewEnvelope(cdip.MessageStageDecode),
			ParentJobID: parent.ID,
			StageJobID:  stageJob.ID,
			StageIndex:  stageJob.CDIPStageIndex,
			Step:        step,
		}
		if err := msg.Validate(cdip.MessageStageDecode); err != nil {
			return CDIPCommandResult{}, fmt.Errorf("invalid relay stage.decode for %s: %w", stageJob.ID, err)
		}
		messages = append(messages, msg)
	}

	for index, stageJob := range stages {
		var input models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &input); err != nil {
			return CDIPCommandResult{}, fmt.Errorf("invalid stage job input for %s: %w", stageJob.ID, err)
		}
		input.StageJobID = stageJob.ID
		input.Step = step
		input.StageCommand = "decode_boundary"
		if strings.TrimSpace(input.StageSessionID) == "" {
			input.StageSessionID = distributedStageSessionID(parent.ID, input.Stage.Index, input.ConversationID)
		}
		if index > 0 {
			input.UpstreamStageID = stages[index-1].ID
			input.UpstreamNodeID = stages[index-1].AssignedTo
		}
		if index < len(stages)-1 {
			input.DownstreamStageID = stages[index+1].ID
			input.DownstreamNodeID = stages[index+1].AssignedTo
		}
		detail := "coordinator entered relay decode boundary"
		if index == 0 {
			input.StageCommand = "source_decode"
			detail = "coordinator dispatched source_decode stage command"
			body, err := json.Marshal(input)
			if err != nil {
				return CDIPCommandResult{}, err
			}
			if _, ok := store.DispatchCDIPStageCommand(stageJob.ID, string(body), cdip.StageDecode, detail); !ok {
				return CDIPCommandResult{}, fmt.Errorf("failed to dispatch source decode stage %s", stageJob.ID)
			}
			continue
		}
		if index < len(stages)-1 {
			input.StageCommand = "relay_decode"
			detail = "coordinator dispatched relay_decode stage command"
			body, err := json.Marshal(input)
			if err != nil {
				return CDIPCommandResult{}, err
			}
			if _, ok := store.DispatchCDIPStageCommand(stageJob.ID, string(body), cdip.StageDecode, detail); !ok {
				return CDIPCommandResult{}, fmt.Errorf("failed to dispatch relay decode stage %s", stageJob.ID)
			}
			continue
		}
		input.StageCommand = "terminal_decode"
		detail = "coordinator dispatched terminal_decode stage command"
		body, err := json.Marshal(input)
		if err != nil {
			return CDIPCommandResult{}, err
		}
		if _, ok := store.DispatchCDIPStageCommand(stageJob.ID, string(body), cdip.StageDecode, detail); !ok {
			return CDIPCommandResult{}, fmt.Errorf("failed to dispatch terminal decode stage %s", stageJob.ID)
		}
	}

	return CDIPCommandResult{
		ParentJob: parent,
		StageJobs: cdipStageJobsForParent(store.Jobs(), parent.ID),
		Messages:  messages,
	}, nil
}

func runCDIPDecodeLoop(store Store, parentJobID string, requestedMaxTokens int) (CDIPDecodeLoopResult, error) {
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPDecodeLoopResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPDecodeLoopResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPDecodeLoopResult{}, fmt.Errorf("distributed parent job has no stage graph")
	}
	for _, stageJob := range stages {
		if stageJob.CDIPState != cdip.StagePrefill && stageJob.CDIPState != cdip.StageDecode {
			return CDIPDecodeLoopResult{}, fmt.Errorf("stage %s is %s, expected %s or %s", stageJob.ID, stageJob.CDIPState, cdip.StagePrefill, cdip.StageDecode)
		}
	}

	var input models.DistributedGenerateInput
	_ = json.Unmarshal([]byte(parent.Input), &input)
	maxTokens := requestedMaxTokens
	if maxTokens <= 0 {
		maxTokens = input.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 1
	}
	if maxTokens > 16 {
		maxTokens = 16
	}

	terminal := stages[len(stages)-1]
	trace := cdipDecodeLoopTrace(parent, stages, maxTokens, "control-plane")
	messages := make([]cdip.StageCommand, 0, len(stages)*maxTokens)
	chunks := make([]CDIPTerminalDecodeChunk, 0, maxTokens)
	tokenIDs := make([]int, 0, maxTokens)
	outputParts := make([]string, 0, maxTokens)
	for step := 1; step <= maxTokens; step++ {
		for _, stageJob := range stages {
			msg := cdip.StageCommand{
				Envelope:    cdip.NewEnvelope(cdip.MessageStageDecode),
				ParentJobID: parent.ID,
				StageJobID:  stageJob.ID,
				StageIndex:  stageJob.CDIPStageIndex,
				Step:        uint64(step),
			}
			if err := msg.Validate(cdip.MessageStageDecode); err != nil {
				return CDIPDecodeLoopResult{}, fmt.Errorf("invalid loop stage.decode for %s: %w", stageJob.ID, err)
			}
			messages = append(messages, msg)
		}
		tokenID := 1000 + step
		tokenText := fmt.Sprintf(" token-%d", step)
		tokenIDs = append(tokenIDs, tokenID)
		outputParts = append(outputParts, tokenText)
		chunks = append(chunks, CDIPTerminalDecodeChunk{
			Step:          uint64(step),
			StageJobID:    terminal.ID,
			StageIndex:    terminal.CDIPStageIndex,
			KVCacheKey:    trace.KVCacheKey,
			NextTokenID:   tokenID,
			NextTokenText: tokenText,
			Tokens:        append([]int(nil), tokenIDs...),
			Output:        strings.Join(outputParts, ""),
			Final:         step == maxTokens,
		})
	}
	for _, stageJob := range stages {
		if _, ok := store.UpdateCDIPStageState(stageJob.ID, cdip.StageDecode, "coordinator ran decode-loop"); !ok {
			return CDIPDecodeLoopResult{}, fmt.Errorf("failed to mark decode-loop stage %s", stageJob.ID)
		}
	}
	output := strings.Join(outputParts, "")
	resultBody, err := json.Marshal(map[string]any{
		"kind":                  "cdip.distributed_decode_loop_result",
		"protocol":              cdip.Protocol,
		"version":               cdip.Version,
		"parent_job":            parent.ID,
		"terminal_stage_job_id": terminal.ID,
		"terminal_stage_index":  terminal.CDIPStageIndex,
		"tokens":                tokenIDs,
		"token_count":           len(tokenIDs),
		"output":                output,
		"final":                 true,
		"chunks":                chunks,
		"trace":                 trace,
		"guardrail":             trace.Guardrail,
	})
	if err != nil {
		return CDIPDecodeLoopResult{}, err
	}
	parent, ok = store.CompleteCoordinatorJob(parent.ID, string(resultBody), "")
	if !ok {
		return CDIPDecodeLoopResult{}, fmt.Errorf("failed to complete distributed parent job")
	}
	return CDIPDecodeLoopResult{
		ParentJob: parent,
		StageJobs: cdipStageJobsForParent(store.Jobs(), parent.ID),
		Messages:  messages,
		Chunks:    chunks,
		Trace:     trace,
		Output:    output,
		Final:     true,
	}, nil
}

func dispatchCDIPDecodeLoopStep(store Store, parentJobID string, step uint64, requestedMaxTokens int, terminalForceFinal ...bool) (CDIPCommandResult, error) {
	return dispatchCDIPDecodeLoopStepWithTokenFeedback(store, parentJobID, step, requestedMaxTokens, nil, "", terminalForceFinal...)
}

func dispatchCDIPDecodeLoopStepWithTokenFeedback(store Store, parentJobID string, step uint64, requestedMaxTokens int, previousTokenID *int, previousTokenText string, terminalForceFinal ...bool) (CDIPCommandResult, error) {
	if step == 0 {
		step = 1
	}
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPCommandResult{}, fmt.Errorf("decode-loop dispatch requires at least 2 stages: source and terminal")
	}
	for _, stageJob := range stages {
		if stageJob.CDIPState != cdip.StagePrefill && stageJob.CDIPState != cdip.StageDecode {
			return CDIPCommandResult{}, fmt.Errorf("stage %s is %s, expected %s or %s", stageJob.ID, stageJob.CDIPState, cdip.StagePrefill, cdip.StageDecode)
		}
	}
	maxTokens := requestedMaxTokens
	if maxTokens <= 0 {
		var input models.DistributedGenerateInput
		_ = json.Unmarshal([]byte(parent.Input), &input)
		maxTokens = input.MaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 1
	}
	trace := cdipDecodeLoopTrace(parent, stages, maxTokens, "worker-dispatch")
	messages := make([]cdip.StageCommand, 0, len(stages))
	for index, stageJob := range stages {
		msg := cdip.StageCommand{
			Envelope:    cdip.NewEnvelope(cdip.MessageStageDecode),
			ParentJobID: parent.ID,
			StageJobID:  stageJob.ID,
			StageIndex:  stageJob.CDIPStageIndex,
			Step:        step,
		}
		if err := msg.Validate(cdip.MessageStageDecode); err != nil {
			return CDIPCommandResult{}, fmt.Errorf("invalid decode-loop stage.decode for %s: %w", stageJob.ID, err)
		}
		messages = append(messages, msg)

		var input models.DistributedStageJobInput
		if err := json.Unmarshal([]byte(stageJob.Input), &input); err != nil {
			return CDIPCommandResult{}, fmt.Errorf("invalid stage job input for %s: %w", stageJob.ID, err)
		}
		input.StageJobID = stageJob.ID
		input.Step = step
		input.KVCacheKey = trace.KVCacheKey
		if strings.TrimSpace(input.StageSessionID) == "" {
			input.StageSessionID = distributedStageSessionID(parent.ID, input.Stage.Index, input.ConversationID)
		}
		input.TerminalForceFinal = nil
		input.PreviousTokenID = nil
		input.PreviousTokenText = ""
		if index > 0 {
			input.UpstreamStageID = stages[index-1].ID
			input.UpstreamNodeID = stages[index-1].AssignedTo
		}
		if index < len(stages)-1 {
			input.DownstreamStageID = stages[index+1].ID
			input.DownstreamNodeID = stages[index+1].AssignedTo
		}
		detail := fmt.Sprintf("coordinator dispatched decode-loop step %d", step)
		switch {
		case index == 0:
			input.StageCommand = "source_decode"
			if previousTokenID != nil {
				token := *previousTokenID
				input.PreviousTokenID = &token
				input.PreviousTokenText = previousTokenText
			}
		case index < len(stages)-1:
			input.StageCommand = "relay_decode"
		default:
			input.StageCommand = "terminal_decode"
			input.DownstreamStageID = ""
			input.DownstreamNodeID = ""
			if len(terminalForceFinal) > 0 {
				forceFinal := terminalForceFinal[0]
				input.TerminalForceFinal = &forceFinal
			} else {
				forceFinal := step >= uint64(maxTokens)
				input.TerminalForceFinal = &forceFinal
			}
		}
		body, err := json.Marshal(input)
		if err != nil {
			return CDIPCommandResult{}, err
		}
		if _, ok := store.DispatchCDIPStageCommand(stageJob.ID, string(body), cdip.StageDecode, detail); !ok {
			return CDIPCommandResult{}, fmt.Errorf("failed to dispatch decode-loop step %d stage %s", step, stageJob.ID)
		}
	}
	return CDIPCommandResult{
		ParentJob: parent,
		StageJobs: cdipStageJobsForParent(store.Jobs(), parent.ID),
		Messages:  messages,
		Trace:     &trace,
	}, nil
}

func cdipDecodeLoopTrace(parent jobs.Job, stages []jobs.Job, maxTokens int, mode string) CDIPDecodeLoopTrace {
	sessionID := "cdip-session-" + parent.ID
	stageJobIDs := make([]string, 0, len(stages))
	for _, stageJob := range stages {
		stageJobIDs = append(stageJobIDs, stageJob.ID)
	}
	return CDIPDecodeLoopTrace{
		Protocol:           cdip.Protocol,
		Version:            cdip.Version,
		SessionID:          sessionID,
		KVCacheKey:         sessionID + ":kv",
		Mode:               mode,
		MaxTokens:          maxTokens,
		StageCount:         len(stages),
		StageJobIDs:        stageJobIDs,
		TerminalStageJobID: stages[len(stages)-1].ID,
		Guardrail:          "control-plane decode-loop proof; real llama.cpp KV decode loop is still pending",
	}
}

func completeCDIPDistributedJob(store Store, parentJobID string, output string) (CDIPCommandResult, error) {
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPCommandResult{}, fmt.Errorf("distributed parent job has no stage graph")
	}
	for _, stageJob := range stages {
		if stageJob.CDIPState != cdip.StageDecode {
			return CDIPCommandResult{}, fmt.Errorf("stage %s is %s, expected %s", stageJob.ID, stageJob.CDIPState, cdip.StageDecode)
		}
	}
	messages := make([]cdip.StageCommand, 0, len(stages))
	for _, stageJob := range stages {
		msg := cdip.StageCommand{
			Envelope:    cdip.NewEnvelope(cdip.MessageStageComplete),
			ParentJobID: parent.ID,
			StageJobID:  stageJob.ID,
			StageIndex:  stageJob.CDIPStageIndex,
		}
		if err := msg.Validate(cdip.MessageStageComplete); err != nil {
			return CDIPCommandResult{}, fmt.Errorf("invalid stage.complete for %s: %w", stageJob.ID, err)
		}
		messages = append(messages, msg)
	}
	for _, stageJob := range stages {
		if _, ok := store.UpdateCDIPStageState(stageJob.ID, cdip.StageCompleted, "coordinator sent stage.complete"); !ok {
			return CDIPCommandResult{}, fmt.Errorf("failed to complete stage %s", stageJob.ID)
		}
	}
	if strings.TrimSpace(output) == "" {
		output = "CDIP distributed inference completed"
	}
	resultBody, err := json.Marshal(map[string]any{
		"kind":        "cdip.distributed_result",
		"protocol":    cdip.Protocol,
		"version":     cdip.Version,
		"parent_job":  parent.ID,
		"stage_count": len(stages),
		"output":      output,
	})
	if err != nil {
		return CDIPCommandResult{}, err
	}
	parent, ok = store.CompleteCoordinatorJob(parent.ID, string(resultBody), "")
	if !ok {
		return CDIPCommandResult{}, fmt.Errorf("failed to complete distributed parent job")
	}
	return CDIPCommandResult{
		ParentJob: parent,
		StageJobs: cdipStageJobsForParent(store.Jobs(), parent.ID),
		Messages:  messages,
	}, nil
}

func runCDIPMockCoordinator(store Store, parentJobID string) (CDIPMockRunResult, error) {
	parent, ok := store.Job(parentJobID)
	if !ok || parent.Type != models.JobGenerateDistributed {
		return CDIPMockRunResult{}, fmt.Errorf("distributed parent job not found")
	}
	if parent.Status == jobs.StatusSucceeded || parent.Status == jobs.StatusFailed || parent.Status == jobs.StatusCanceled {
		return CDIPMockRunResult{}, fmt.Errorf("distributed parent job is already terminal")
	}
	stages := cdipStageJobsForParent(store.Jobs(), parent.ID)
	if len(stages) < 2 {
		return CDIPMockRunResult{}, fmt.Errorf("distributed parent job has no stage graph")
	}
	for _, stage := range stages {
		if _, ok := store.UpdateCDIPStageState(stage.ID, cdip.StagePreparing, "mock coordinator preparing stage"); !ok {
			return CDIPMockRunResult{}, fmt.Errorf("failed to prepare stage %s", stage.ID)
		}
		if _, ok := store.UpdateCDIPStageState(stage.ID, cdip.StageReady, "mock coordinator stage ready"); !ok {
			return CDIPMockRunResult{}, fmt.Errorf("failed to mark stage ready %s", stage.ID)
		}
	}
	for _, stage := range stages {
		if _, ok := store.UpdateCDIPStageState(stage.ID, cdip.StagePrefill, "mock activation prefill"); !ok {
			return CDIPMockRunResult{}, fmt.Errorf("failed prefill for stage %s", stage.ID)
		}
	}
	for _, stage := range stages {
		if _, ok := store.UpdateCDIPStageState(stage.ID, cdip.StageDecode, "mock activation decode"); !ok {
			return CDIPMockRunResult{}, fmt.Errorf("failed decode for stage %s", stage.ID)
		}
	}
	for _, stage := range stages {
		if _, ok := store.UpdateCDIPStageState(stage.ID, cdip.StageCompleted, "mock stage completed"); !ok {
			return CDIPMockRunResult{}, fmt.Errorf("failed complete for stage %s", stage.ID)
		}
	}
	output := "CDIP mock distributed inference completed"
	parent, ok = store.CompleteCoordinatorJob(parent.ID, output, "")
	if !ok {
		return CDIPMockRunResult{}, fmt.Errorf("failed to complete distributed parent job")
	}
	return CDIPMockRunResult{
		ParentJob: parent,
		StageJobs: cdipStageJobsForParent(store.Jobs(), parent.ID),
		Output:    output,
	}, nil
}

func cdipStageJobsForParent(in []jobs.Job, parentJobID string) []jobs.Job {
	out := make([]jobs.Job, 0)
	for _, job := range in {
		if job.Type != models.JobGenerateStage || job.CDIPParentJobID != parentJobID {
			continue
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CDIPStageIndex != out[j].CDIPStageIndex {
			return out[i].CDIPStageIndex < out[j].CDIPStageIndex
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func distributedModelPlan(model models.Model, nodes []cluster.Node) DistributedModelPlan {
	return distributedModelPlanWithTotalLayers(model, nodes, inferredModelLayersFromNodes(model.ID, nodes))
}

func distributedModelPlanWithTotalLayers(model models.Model, nodes []cluster.Node, totalLayers int) DistributedModelPlan {
	if totalLayers <= 0 {
		totalLayers = estimatedModelLayers(model)
	}
	plan := DistributedModelPlan{
		ModelID:             model.ID,
		Mode:                "pipeline_layers",
		Runtime:             string(model.Runtime),
		TotalLayers:         totalLayers,
		RequiredMemoryBytes: model.MemoryBytes,
		RequiredDiskBytes:   model.DiskBytes,
		StageOverheadBytes:  distributedStageOverheadBytes,
		Network: DistributedNetworkPlan{
			AssumedInterStageLatencyMS: 80,
		},
		EstimatedLatency: DistributedLatencyModel{
			Confidence: "low",
			Assumption: "planning estimate for pipeline-parallel stage workers before live benchmark calibration",
		},
		NextImplementationTargets: []string{
			"publish verified physical stage shard artifacts per selected worker",
			"keep resident stage daemon sessions warm across prompts",
			"benchmark local vs sliced execution under memory pressure",
			"run cautious AWS proof with a model that exceeds one worker budget",
			"add failure recovery for stage daemon restarts and activation replay",
		},
	}

	candidates := distributedCandidates(model, nodes)
	plan.Placement = buildDistributedPlacement(model, candidates, nil, nil, plan.TotalLayers)
	plan.StageRuntimeDiagnostics = distributedRuntimeDiagnostics(model, candidates)
	for _, candidate := range candidates {
		plan.AggregateMemoryBytes += candidate.Resources.Memory.AllowedBytes
		plan.AggregateStageMemoryBytes += effectiveStageMemory(candidate)
		plan.AggregateDiskBytes += effectiveNodeStorage(candidate)
	}
	if len(candidates) == 0 {
		plan.Blockers = append(plan.Blockers, "no online workers are reporting resources")
		return plan
	}
	if len(candidates) < 2 {
		plan.Blockers = append(plan.Blockers, "distributed layer split needs at least 2 online workers")
	}
	if plan.AggregateStageMemoryBytes < model.MemoryBytes {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("aggregate stage RAM short by %.1f GB after %.1f GB per-stage overhead", gbDiff(model.MemoryBytes, plan.AggregateStageMemoryBytes), bytesToGB(distributedStageOverheadBytes)))
	}
	if plan.AggregateDiskBytes < model.DiskBytes {
		plan.Blockers = append(plan.Blockers, fmt.Sprintf("aggregate disk short by %.1f GB", gbDiff(model.DiskBytes, plan.AggregateDiskBytes)))
	}
	if len(plan.Blockers) > 0 {
		return plan
	}

	selected := selectDistributedWorkers(model, candidates)
	plan.Placement = buildDistributedPlacement(model, candidates, selected, nil, plan.TotalLayers)
	if len(selected) < 2 {
		plan.Blockers = append(plan.Blockers, "no multi-worker stage set can hold the model")
		return plan
	}
	layerCounts, ok := allocateDistributedLayers(model, selected, plan.TotalLayers)
	plan.Placement = buildDistributedPlacement(model, candidates, selected, layerCounts, plan.TotalLayers)
	if !ok {
		plan.Blockers = append(plan.Blockers, "no memory-aware layer placement can satisfy per-worker RAM and disk limits")
		return plan
	}
	plan.Stages = buildDistributedStages(model, selected, plan.TotalLayers, layerCounts)
	plan.Feasible = len(plan.Stages) >= 2
	plan.Network.InterStageHops = maxInt(0, len(plan.Stages)-1)
	plan.EstimatedLatency = estimateDistributedLatency(model, len(plan.Stages), plan.Network.AssumedInterStageLatencyMS)
	if !allStagesRuntimeReady(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "one or more selected workers do not report a ready runtime")
	}
	if !allStagesStageRuntimeReady(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "one or more selected workers do not report distributed stage runtime capability")
	}
	if !allStagesInstalled(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "model shards are not installed on all selected workers")
	}
	plan.Blockers = append(plan.Blockers, distributedExecutionBlockers(plan.Stages)...)
	plan.ExecutableNow = len(plan.Blockers) == 0
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

func distributedRuntimeDiagnostics(model models.Model, candidates []cluster.Node) DistributedRuntimeDiag {
	out := DistributedRuntimeDiag{
		CandidateWorkers: len(candidates),
	}
	for _, node := range candidates {
		runtimeStatus, runtimeReady := nodeRuntimeStatus(node, string(model.Runtime))
		if runtimeReady {
			out.RuntimeReadyWorkers++
		}
		stageRuntime, stageReady, reason := nodeDistributedStageRuntime(runtimeStatus, runtimeReady)
		if stageReady {
			out.StageReadyWorkers++
			switch stageRuntime {
			case "logical-stage":
				out.LogicalStageWorkers++
			case "llama.cpp-stage-experimental":
				out.LlamaCPPStageWorkers++
			case "cmesh-stage-daemon":
				out.ResidentStageWorkers++
			}
			continue
		}
		out.MissingStageCapability = append(out.MissingStageCapability, DistributedRuntimeMissing{
			NodeID:   node.ID,
			NodeName: nodeDisplayName(node),
			Reason:   reason,
		})
	}
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

func allocateDistributedLayers(model models.Model, nodes []cluster.Node, totalLayers int) ([]int, bool) {
	if totalLayers <= 0 {
		totalLayers = estimatedModelLayers(model)
	}
	if len(nodes) == 0 || totalLayers < len(nodes) {
		return nil, false
	}
	caps := make([]int, 0, len(nodes))
	totalCap := 0
	for _, node := range nodes {
		capacity := totalLayers
		if model.MemoryBytes > 0 {
			capacity = minInt(capacity, int((effectiveStageMemory(node)*uint64(totalLayers))/model.MemoryBytes))
		}
		if model.DiskBytes > 0 {
			capacity = minInt(capacity, int((effectiveNodeStorage(node)*uint64(totalLayers))/model.DiskBytes))
		}
		if capacity < 1 {
			return nil, false
		}
		caps = append(caps, capacity)
		totalCap += capacity
	}
	if totalCap < totalLayers {
		return nil, false
	}

	assignments := make([]int, len(nodes))
	remaining := totalLayers
	for i := range assignments {
		assignments[i] = 1
		remaining--
	}
	type remainder struct {
		index    int
		fraction float64
		capLeft  int
	}
	remainders := make([]remainder, 0, len(nodes))
	for index, capacity := range caps {
		quota := float64(totalLayers) * float64(capacity) / float64(totalCap)
		extra := int(math.Floor(quota)) - 1
		if extra < 0 {
			extra = 0
		}
		if extra > capacity-1 {
			extra = capacity - 1
		}
		assignments[index] += extra
		remaining -= extra
		remainders = append(remainders, remainder{
			index:    index,
			fraction: quota - math.Floor(quota),
			capLeft:  capacity - assignments[index],
		})
	}
	sort.Slice(remainders, func(i, j int) bool {
		if remainders[i].fraction != remainders[j].fraction {
			return remainders[i].fraction > remainders[j].fraction
		}
		if remainders[i].capLeft != remainders[j].capLeft {
			return remainders[i].capLeft > remainders[j].capLeft
		}
		return nodeDisplayName(nodes[remainders[i].index]) < nodeDisplayName(nodes[remainders[j].index])
	})
	for remaining > 0 {
		progress := false
		for i := range remainders {
			index := remainders[i].index
			if assignments[index] >= caps[index] {
				continue
			}
			assignments[index]++
			remaining--
			progress = true
			if remaining == 0 {
				break
			}
		}
		if !progress {
			return nil, false
		}
	}
	return assignments, true
}

func buildDistributedPlacement(model models.Model, candidates []cluster.Node, selected []cluster.Node, layerCounts []int, totalLayers int) DistributedPlacement {
	if totalLayers <= 0 {
		totalLayers = estimatedModelLayers(model)
	}
	out := DistributedPlacement{
		Strategy:      "memory_disk_weighted_layers",
		TotalLayers:   totalLayers,
		ModelMemory:   model.MemoryBytes,
		ModelDisk:     model.DiskBytes,
		StageOverhead: distributedStageOverheadBytes,
		Candidates:    make([]DistributedPlacementCandidate, 0, len(candidates)),
	}
	selectedIndex := make(map[string]int, len(selected))
	for index, node := range selected {
		selectedIndex[node.ID] = index
	}
	for _, node := range candidates {
		effectiveMemory := effectiveStageMemory(node)
		effectiveStorage := effectiveNodeStorage(node)
		capacity := totalLayers
		reasons := make([]string, 0, 2)
		if effectiveMemory == 0 {
			capacity = 0
			reasons = append(reasons, fmt.Sprintf("allowed RAM must exceed %.1f GB stage overhead", bytesToGB(distributedStageOverheadBytes)))
		} else if model.MemoryBytes > 0 {
			capacity = minInt(capacity, int((effectiveMemory*uint64(totalLayers))/model.MemoryBytes))
		}
		if effectiveStorage == 0 {
			capacity = 0
			reasons = append(reasons, "no effective storage budget")
		} else if model.DiskBytes > 0 {
			capacity = minInt(capacity, int((effectiveStorage*uint64(totalLayers))/model.DiskBytes))
		}
		entry := DistributedPlacementCandidate{
			NodeID:                    node.ID,
			NodeName:                  nodeDisplayName(node),
			LayerCapacity:             capacity,
			AllowedMemoryBytes:        node.Resources.Memory.AllowedBytes,
			EffectiveStageMemoryBytes: effectiveMemory,
			AllowedStorageBytes:       node.Resources.Storage.AllowedBytes,
			EffectiveStorageBytes:     effectiveStorage,
			BlockedReason:             strings.Join(reasons, "; "),
		}
		if index, ok := selectedIndex[node.ID]; ok {
			entry.Selected = true
			if index < len(layerCounts) {
				entry.AssignedLayers = layerCounts[index]
				entry.AssignedMemoryBytes = proportionalBytes(model.MemoryBytes, entry.AssignedLayers, totalLayers) + distributedStageOverheadBytes
				entry.AssignedDiskBytes = proportionalBytes(model.DiskBytes, entry.AssignedLayers, totalLayers)
				if entry.AssignedMemoryBytes <= node.Resources.Memory.AllowedBytes {
					entry.RemainingMemoryBytes = node.Resources.Memory.AllowedBytes - entry.AssignedMemoryBytes
				}
				if entry.AssignedDiskBytes <= effectiveStorage {
					entry.RemainingStorageBytes = effectiveStorage - entry.AssignedDiskBytes
				}
				if entry.AssignedLayers > entry.LayerCapacity {
					entry.BlockedReason = "assigned layers exceed node capacity"
				}
			}
		}
		if entry.BlockedReason == "" && entry.LayerCapacity < 1 {
			entry.BlockedReason = "cannot hold even one layer with current RAM/disk budget"
		}
		out.Candidates = append(out.Candidates, entry)
	}
	return out
}

func buildDistributedStages(model models.Model, nodes []cluster.Node, totalLayers int, layerCounts []int) []DistributedPlanStage {
	if totalLayers <= 0 {
		totalLayers = 1
	}
	stages := make([]DistributedPlanStage, 0, len(nodes))
	nextLayer := 0
	for index, node := range nodes {
		layers := totalLayers - nextLayer
		if index < len(layerCounts) {
			layers = layerCounts[index]
		}
		layerStart := nextLayer
		layerEnd := nextLayer + layers - 1
		nextLayer += layers
		runtimeStatus, runtimeReady := nodeRuntimeStatus(node, string(model.Runtime))
		stageRuntime, stageRuntimeReady, stageRuntimeReason := nodeDistributedStageRuntime(runtimeStatus, runtimeReady)
		stageDaemonURL := nodeStageDaemonURL(runtimeStatus)
		stage := DistributedPlanStage{
			Index:               index,
			NodeID:              node.ID,
			NodeName:            nodeDisplayName(node),
			LayerStart:          layerStart,
			LayerEnd:            layerEnd,
			Layers:              layers,
			ModelMemoryBytes:    proportionalBytes(model.MemoryBytes, layers, totalLayers),
			OverheadMemoryBytes: distributedStageOverheadBytes,
			MemoryBytes:         proportionalBytes(model.MemoryBytes, layers, totalLayers) + distributedStageOverheadBytes,
			DiskBytes:           proportionalBytes(model.DiskBytes, layers, totalLayers),
			AllowedMemoryBytes:  node.Resources.Memory.AllowedBytes,
			AllowedStorageBytes: effectiveNodeStorage(node),
			RuntimeReady:        runtimeReady,
			RuntimeCapabilities: runtimeStatus.Capabilities,
			StageRuntime:        stageRuntime,
			StageRuntimeReady:   stageRuntimeReady,
			StageRuntimeReason:  stageRuntimeReason,
			StageDaemonURL:      stageDaemonURL,
			Installed:           nodeHasModel(node, model.ID),
		}
		stages = append(stages, stage)
	}
	return stages
}

func pinDistributedPlanStageNodes(plan DistributedModelPlan, stageNodeIDs []string) (DistributedModelPlan, error) {
	cleaned := make([]string, 0, len(stageNodeIDs))
	seen := map[string]bool{}
	for _, nodeID := range stageNodeIDs {
		nodeID = strings.TrimSpace(nodeID)
		if nodeID == "" {
			continue
		}
		if seen[nodeID] {
			return plan, fmt.Errorf("distributed generate stage_node_ids contains duplicate node %q", nodeID)
		}
		seen[nodeID] = true
		cleaned = append(cleaned, nodeID)
	}
	if len(cleaned) == 0 {
		return plan, nil
	}
	if len(cleaned) != len(plan.Stages) {
		return plan, fmt.Errorf("distributed generate stage_node_ids must match stage count")
	}
	byNodeID := make(map[string]DistributedPlanStage, len(plan.Stages))
	for _, stage := range plan.Stages {
		byNodeID[stage.NodeID] = stage
	}
	pinned := make([]DistributedPlanStage, 0, len(plan.Stages))
	for index, nodeID := range cleaned {
		nodeStage, ok := byNodeID[nodeID]
		if !ok {
			return plan, fmt.Errorf("distributed generate stage_node_ids[%d] node %q is not in the selected distributed plan", index, nodeID)
		}
		layerStage := plan.Stages[index]
		if layerStage.MemoryBytes > nodeStage.AllowedMemoryBytes {
			return plan, fmt.Errorf("distributed generate stage_node_ids[%d] node %q lacks RAM for layers %d-%d", index, nodeID, layerStage.LayerStart, layerStage.LayerEnd)
		}
		if layerStage.DiskBytes > nodeStage.AllowedStorageBytes {
			return plan, fmt.Errorf("distributed generate stage_node_ids[%d] node %q lacks disk for layers %d-%d", index, nodeID, layerStage.LayerStart, layerStage.LayerEnd)
		}
		nodeStage.Index = index
		nodeStage.LayerStart = layerStage.LayerStart
		nodeStage.LayerEnd = layerStage.LayerEnd
		nodeStage.Layers = layerStage.Layers
		nodeStage.ModelMemoryBytes = layerStage.ModelMemoryBytes
		nodeStage.OverheadMemoryBytes = layerStage.OverheadMemoryBytes
		nodeStage.MemoryBytes = layerStage.MemoryBytes
		nodeStage.DiskBytes = layerStage.DiskBytes
		pinned = append(pinned, nodeStage)
	}
	plan.Stages = pinned
	plan.Network.InterStageHops = maxInt(0, len(plan.Stages)-1)
	plan.StageRuntimeDiagnostics = stageRuntimeDiagnosticsFromPlan(plan.Stages)
	plan.Warnings = nil
	if !allStagesRuntimeReady(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "one or more selected workers do not report a ready runtime")
	}
	if !allStagesStageRuntimeReady(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "one or more selected workers do not report distributed stage runtime capability")
	}
	if !allStagesInstalled(plan.Stages) {
		plan.Warnings = append(plan.Warnings, "model shards are not installed on all selected workers")
	}
	return plan, nil
}

func stageRuntimeDiagnosticsFromPlan(stages []DistributedPlanStage) DistributedRuntimeDiag {
	out := DistributedRuntimeDiag{CandidateWorkers: len(stages)}
	for _, stage := range stages {
		if stage.RuntimeReady {
			out.RuntimeReadyWorkers++
		}
		if stage.StageRuntimeReady {
			out.StageReadyWorkers++
			switch stage.StageRuntime {
			case "logical-stage":
				out.LogicalStageWorkers++
			case "llama.cpp-stage-experimental":
				out.LlamaCPPStageWorkers++
			case "cmesh-stage-daemon":
				out.ResidentStageWorkers++
			}
			continue
		}
		reason := strings.TrimSpace(stage.StageRuntimeReason)
		if reason == "" {
			reason = "distributed stage runtime is not ready"
		}
		out.MissingStageCapability = append(out.MissingStageCapability, DistributedRuntimeMissing{
			NodeID:   stage.NodeID,
			NodeName: stage.NodeName,
			Reason:   reason,
		})
	}
	return out
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

func inferredModelLayersFromNodes(modelID string, nodes []cluster.Node) int {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return 0
	}
	out := 0
	for _, node := range nodes {
		if node.Role != cluster.NodeRoleWorker || node.Status != cluster.NodeStatusOnline {
			continue
		}
		for _, model := range node.Resources.Models {
			if model.ID != modelID || !model.Ready || model.Layers <= 0 {
				continue
			}
			if out == 0 || model.Layers < out {
				out = model.Layers
			}
		}
	}
	return out
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

func effectiveStageMemory(node cluster.Node) uint64 {
	if node.Resources.Memory.AllowedBytes <= distributedStageOverheadBytes {
		return 0
	}
	return node.Resources.Memory.AllowedBytes - distributedStageOverheadBytes
}

func bytesToGB(value uint64) float64 {
	return float64(value) / 1024 / 1024 / 1024
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

func allStagesStageRuntimeReady(stages []DistributedPlanStage) bool {
	for _, stage := range stages {
		if !stage.StageRuntimeReady {
			return false
		}
	}
	return true
}

func allStagesInstalled(stages []DistributedPlanStage) bool {
	for _, stage := range stages {
		if !stage.Installed {
			return false
		}
	}
	return len(stages) > 0
}

func allStagesResidentStageDaemonReady(stages []DistributedPlanStage) bool {
	for _, stage := range stages {
		if strings.TrimSpace(stage.StageDaemonURL) == "" {
			return false
		}
	}
	return len(stages) > 0
}

func distributedExecutionBlockers(stages []DistributedPlanStage) []string {
	blockers := make([]string, 0, 4)
	if !allStagesRuntimeReady(stages) {
		blockers = append(blockers, "one or more selected workers do not report a ready model runtime")
	}
	if !allStagesStageRuntimeReady(stages) {
		blockers = append(blockers, "one or more selected workers do not report distributed stage runtime capability")
	}
	if !allStagesResidentStageDaemonReady(stages) {
		blockers = append(blockers, "resident stage daemon endpoint is not ready on all selected workers")
	}
	if !allStagesInstalled(stages) {
		blockers = append(blockers, "physical model stage shards are not installed on all selected workers")
	}
	return blockers
}

func nodeDistributedStageRuntime(runtimeStatus cluster.RuntimeResource, runtimeReady bool) (string, bool, string) {
	if !runtimeReady {
		reason := strings.TrimSpace(runtimeStatus.Error)
		if reason == "" {
			reason = "runtime is not ready"
		}
		return "", false, reason
	}
	for _, stageRuntime := range runtimeStatus.StageRuntimes {
		if !stageRuntime.Ready {
			continue
		}
		if strings.TrimSpace(stageRuntime.Protocol) != "" && stageRuntime.Protocol != runtimes.StageSessionV1 {
			continue
		}
		if strings.TrimSpace(stageRuntime.Endpoint) == "" {
			continue
		}
		name := strings.TrimSpace(stageRuntime.Name)
		if name == "" {
			name = "cmesh-stage-daemon"
		}
		return name, true, ""
	}
	if runtimeHasCapability(runtimeStatus, runtimes.CapabilityLlamaCPPStageRuntime) {
		return "llama.cpp-stage-experimental", true, ""
	}
	if runtimeHasCapability(runtimeStatus, runtimes.CapabilityLogicalStageRuntime) {
		return "logical-stage", true, ""
	}
	return "", false, "distributed stage runtime capability not reported"
}

func nodeStageDaemonURL(runtimeStatus cluster.RuntimeResource) string {
	for _, stageRuntime := range runtimeStatus.StageRuntimes {
		if !stageRuntime.Ready {
			continue
		}
		if strings.TrimSpace(stageRuntime.Protocol) != "" && stageRuntime.Protocol != runtimes.StageSessionV1 {
			continue
		}
		if endpoint := strings.TrimRight(strings.TrimSpace(stageRuntime.Endpoint), "/"); endpoint != "" {
			return endpoint
		}
	}
	return ""
}

func runtimeHasCapability(runtimeStatus cluster.RuntimeResource, capability string) bool {
	for _, item := range runtimeStatus.Capabilities {
		if item == capability {
			return true
		}
	}
	return false
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

func minInt(left int, right int) int {
	if left < right {
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
	if len(input.Shards) != len(input.Stages) {
		return nil, fmt.Errorf("distributed generate requires one shard per stage")
	}
	if len(input.StageModelPaths) > 0 && len(input.StageModelPaths) != len(input.Stages) {
		return nil, fmt.Errorf("distributed generate stage_model_paths must match stage count")
	}
	out := make([]jobs.CreateRequest, 0, len(input.Stages))
	for index, stage := range input.Stages {
		if strings.TrimSpace(stage.NodeID) == "" {
			return nil, fmt.Errorf("stage %d node_id is required", index)
		}
		if stage.Index != index {
			return nil, fmt.Errorf("stage index mismatch: got %d at position %d", stage.Index, index)
		}
		shard := input.Shards[index]
		if shard.Stage.Index != stage.Index || shard.Stage.NodeID != stage.NodeID || shard.Stage.LayerStart != stage.LayerStart || shard.Stage.LayerEnd != stage.LayerEnd {
			return nil, fmt.Errorf("shard %d does not match stage %d", index, stage.Index)
		}
		modelPath := strings.TrimSpace(input.ModelPath)
		if len(input.StageModelPaths) > 0 {
			modelPath = strings.TrimSpace(input.StageModelPaths[index])
			if modelPath == "" {
				return nil, fmt.Errorf("stage %d model path is required when stage_model_paths is set", index)
			}
		}
		stageInput := models.DistributedStageJobInput{
			ParentJobID:    parent.ID,
			ModelID:        input.ModelID,
			ConversationID: input.ConversationID,
			Stage:          stage,
			Shard:          shard,
			Prompt:         input.Prompt,
			Messages:       input.Messages,
			SystemPrompt:   input.SystemPrompt,
			MaxTokens:      input.MaxTokens,
			Temperature:    input.Temperature,
			StageRunnerBin: input.StageRunnerBin,
			StageDaemonURL: firstNonEmptyString(input.StageDaemonURL, stage.StageDaemonURL),
			StageSessionID: distributedStageSessionID(parent.ID, stage.Index, input.ConversationID),
			ModelPath:      modelPath,
			WorkDir:        distributedStageWorkDir(input.WorkDir, stage.Index),
			TimeoutMS:      input.TimeoutMS,
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

func distributedStageSessionID(parentJobID string, stageIndex int, conversationID string) string {
	seed := strings.Join([]string{
		strings.TrimSpace(parentJobID),
		strconv.Itoa(stageIndex),
		strings.TrimSpace(conversationID),
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("stage-%d-%s", stageIndex, hex.EncodeToString(sum[:8]))
}

func distributedStageWorkDir(root string, stageIndex int) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return ""
	}
	return filepath.Join(root, fmt.Sprintf("stage-%d", stageIndex))
}
