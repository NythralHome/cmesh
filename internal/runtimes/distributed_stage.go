package runtimes

import (
	"context"
	"fmt"
	"strings"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/transport"
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
	ParentJobID         string
	StageJobID          string
	StageIndex          int
	Step                uint64
	ActivationTransport transport.ActivationTransport
	DownstreamNodeID    string
	ActivationPayload   []byte
	ActivationEncoding  string
	ActivationShape     []int
	ActivationDType     string
	ActivationChecksum  string
}

type StageCommandResult struct {
	Kind            string                `json:"kind"`
	ParentJobID     string                `json:"parent_job_id"`
	StageJobID      string                `json:"stage_job_id"`
	StageIndex      int                   `json:"stage_index"`
	Step            uint64                `json:"step"`
	ActivationFrame *cdip.ActivationChunk `json:"activation_frame,omitempty"`
	ActivationBytes int                   `json:"activation_bytes,omitempty"`
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
	result, err := stageCommandResult(ctx, "cdip.stage_decode", req)
	if err != nil {
		return StageCommandResult{}, err
	}
	if req.ActivationTransport == nil || strings.TrimSpace(req.DownstreamNodeID) == "" {
		return result, nil
	}
	if len(req.ActivationPayload) == 0 {
		return StageCommandResult{}, fmt.Errorf("activation payload is required when downstream node is set")
	}
	encoding := strings.TrimSpace(req.ActivationEncoding)
	if encoding == "" {
		encoding = "mock"
	}
	dtype := strings.TrimSpace(req.ActivationDType)
	if dtype == "" {
		dtype = "u8"
	}
	shape := req.ActivationShape
	if len(shape) == 0 {
		shape = []int{1, 1, len(req.ActivationPayload)}
	}
	frame := transport.ActivationFrame{
		Header: cdip.ActivationChunk{
			Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
			ParentJobID:  req.ParentJobID,
			StageJobID:   req.StageJobID,
			Sequence:     req.Step,
			ContentType:  "application/vnd.cmesh.activation+binary",
			Encoding:     encoding,
			Shape:        shape,
			DType:        dtype,
			PayloadBytes: uint64(len(req.ActivationPayload)),
			Checksum:     req.ActivationChecksum,
		},
		Payload: req.ActivationPayload,
	}
	writer, err := req.ActivationTransport.OpenWriter(ctx, transport.StreamID{ParentJobID: req.ParentJobID, StageJobID: req.StageJobID}, req.DownstreamNodeID)
	if err != nil {
		return StageCommandResult{}, err
	}
	if err := writer.Send(ctx, frame); err != nil {
		return StageCommandResult{}, err
	}
	result.ActivationFrame = &frame.Header
	result.ActivationBytes = len(frame.Payload)
	return result, nil
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
