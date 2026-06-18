package runtimes

import (
	"context"
	"fmt"
	"strings"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/models"
)

const ActivationStreamV1 = "activation-stream-v1"

type StagePrepareRequest struct {
	ParentJobID      string
	StageJobID       string
	ModelID          string
	Stage            models.DistributedStageInput
	Shard            cdip.ModelShard
	UpstreamNodeID   string
	DownstreamNodeID string
}

type StagePrepareResult struct {
	Kind               string
	ParentJobID        string
	StageIndex         int
	ModelID            string
	Runtime            string
	LayerStart         int
	LayerEnd           int
	UpstreamNodeID     string
	DownstreamNodeID   string
	Materialization    string
	SourceArtifact     string
	TargetArtifact     string
	ActivationProtocol string
}

type StageCommandRequest struct {
	ParentJobID string
	StageJobID  string
	StageIndex  int
	Step        uint64
}

type StageCommandResult struct {
	Kind        string
	ParentJobID string
	StageJobID  string
	StageIndex  int
	Step        uint64
}

type DistributedStageRuntime interface {
	PrepareStage(ctx context.Context, req StagePrepareRequest) (StagePrepareResult, error)
	PrefillStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error)
	DecodeStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error)
	CompleteStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error)
	AbortStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error)
}

type LogicalStageRuntime struct {
	Runtime string
}

func NewLogicalStageRuntime(runtimeName string) LogicalStageRuntime {
	return LogicalStageRuntime{Runtime: strings.TrimSpace(runtimeName)}
}

func (r LogicalStageRuntime) PrepareStage(ctx context.Context, req StagePrepareRequest) (StagePrepareResult, error) {
	if err := ctx.Err(); err != nil {
		return StagePrepareResult{}, err
	}
	if strings.TrimSpace(req.ParentJobID) == "" {
		return StagePrepareResult{}, fmt.Errorf("parent_job_id is required")
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return StagePrepareResult{}, fmt.Errorf("model_id is required")
	}
	if strings.TrimSpace(r.Runtime) == "" {
		return StagePrepareResult{}, fmt.Errorf("distributed stage runtime is required")
	}
	if req.Shard.Stage.Index != req.Stage.Index || req.Shard.Stage.NodeID != req.Stage.NodeID || req.Shard.Stage.LayerStart != req.Stage.LayerStart || req.Shard.Stage.LayerEnd != req.Stage.LayerEnd {
		return StagePrepareResult{}, fmt.Errorf("distributed stage shard does not match stage assignment")
	}
	if req.Shard.Materialization != cdip.ShardLogicalLayers {
		return StagePrepareResult{}, fmt.Errorf("unsupported distributed shard materialization %q", req.Shard.Materialization)
	}
	msg := cdip.StagePrepare{
		Envelope:         cdip.NewEnvelope(cdip.MessageStagePrepare),
		ParentJobID:      req.ParentJobID,
		StageJobID:       req.StageJobID,
		ModelID:          req.ModelID,
		Stage:            req.Shard.Stage,
		UpstreamNodeID:   req.UpstreamNodeID,
		DownstreamNodeID: req.DownstreamNodeID,
	}
	if strings.TrimSpace(msg.StageJobID) == "" {
		msg.StageJobID = "worker-local-stage-prepare"
	}
	if err := msg.Validate(); err != nil {
		return StagePrepareResult{}, fmt.Errorf("invalid distributed stage prepare contract: %w", err)
	}
	return StagePrepareResult{
		Kind:               "cdip.stage_ready",
		ParentJobID:        req.ParentJobID,
		StageIndex:         req.Stage.Index,
		ModelID:            req.ModelID,
		Runtime:            r.Runtime,
		LayerStart:         req.Shard.Stage.LayerStart,
		LayerEnd:           req.Shard.Stage.LayerEnd,
		UpstreamNodeID:     req.UpstreamNodeID,
		DownstreamNodeID:   req.DownstreamNodeID,
		Materialization:    string(req.Shard.Materialization),
		SourceArtifact:     req.Shard.SourceArtifact,
		TargetArtifact:     req.Shard.TargetArtifact,
		ActivationProtocol: ActivationStreamV1,
	}, nil
}

func (r LogicalStageRuntime) PrefillStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return stageCommandResult(ctx, "cdip.stage_prefill", req)
}

func (r LogicalStageRuntime) DecodeStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return stageCommandResult(ctx, "cdip.stage_decode", req)
}

func (r LogicalStageRuntime) CompleteStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return stageCommandResult(ctx, "cdip.stage_complete", req)
}

func (r LogicalStageRuntime) AbortStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return stageCommandResult(ctx, "cdip.stage_abort", req)
}

func stageCommandResult(ctx context.Context, kind string, req StageCommandRequest) (StageCommandResult, error) {
	if err := ctx.Err(); err != nil {
		return StageCommandResult{}, err
	}
	if strings.TrimSpace(req.ParentJobID) == "" {
		return StageCommandResult{}, fmt.Errorf("parent_job_id is required")
	}
	if strings.TrimSpace(req.StageJobID) == "" {
		return StageCommandResult{}, fmt.Errorf("stage_job_id is required")
	}
	return StageCommandResult{
		Kind:        kind,
		ParentJobID: req.ParentJobID,
		StageJobID:  req.StageJobID,
		StageIndex:  req.StageIndex,
		Step:        req.Step,
	}, nil
}
