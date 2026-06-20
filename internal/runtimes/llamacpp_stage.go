package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/cmesh/cmesh/internal/cdip"
)

const LlamaCPPStageRuntimeName = "llama.cpp-stage-experimental"

type StageRuntimeProbe struct {
	Name          string   `json:"name"`
	Runtime       string   `json:"runtime"`
	Ready         bool     `json:"ready"`
	CLIReady      bool     `json:"cli_ready"`
	BinaryPath    string   `json:"binary_path,omitempty"`
	RequiredHooks []string `json:"required_hooks,omitempty"`
	Blockers      []string `json:"blockers,omitempty"`
}

type LlamaCPPStageRuntime struct {
	BinaryPath string
}

type LlamaCPPStagePrepareReport struct {
	Kind           string `json:"kind"`
	Status         string `json:"status"`
	Runtime        string `json:"runtime"`
	ModelPath      string `json:"model_path"`
	ModelName      string `json:"model_name"`
	NLayer         int    `json:"n_layer"`
	StageIndex     int    `json:"stage_index"`
	StageStart     int    `json:"stage_start"`
	StageEnd       int    `json:"stage_end"`
	TensorManifest struct {
		Source              string           `json:"source"`
		ManifestOnly        bool             `json:"manifest_only"`
		TotalTensorCount    int64            `json:"total_tensor_count"`
		SelectedTensorCount int64            `json:"selected_tensor_count"`
		StageTensorCount    int64            `json:"stage_tensor_count"`
		BoundaryTensorCount int64            `json:"boundary_tensor_count"`
		SelectedBytes       uint64           `json:"selected_bytes"`
		Tensors             []StageTensorRef `json:"tensors"`
	} `json:"tensor_manifest"`
	Executable                         bool                      `json:"executable"`
	Guardrail                          string                    `json:"guardrail"`
	MaterializationProbe               StageMaterializationProbe `json:"materialization_probe"`
	SelectedTensorMaterializationReady bool                      `json:"selected_tensor_materialization_ready"`
}

func NewLlamaCPPStageRuntime(binaryPath string) LlamaCPPStageRuntime {
	return LlamaCPPStageRuntime{BinaryPath: strings.TrimSpace(binaryPath)}
}

func ParseLlamaCPPStagePrepareReport(data []byte, fallbackModelID string) (LlamaCPPStagePrepareReport, StageMaterializationPlan, error) {
	var report LlamaCPPStagePrepareReport
	reportData := llamaCPPStagePrepareJSON(data)
	if err := json.Unmarshal(reportData, &report); err != nil {
		return LlamaCPPStagePrepareReport{}, StageMaterializationPlan{}, fmt.Errorf("parse llama.cpp stage prepare report: %w", err)
	}
	if strings.TrimSpace(report.Kind) != "cmesh.llamacpp_stage_prepare" {
		return LlamaCPPStagePrepareReport{}, StageMaterializationPlan{}, fmt.Errorf("unexpected llama.cpp stage prepare kind %q", report.Kind)
	}
	if strings.TrimSpace(report.Status) != "metadata_ready" {
		return report, StageMaterializationPlan{}, fmt.Errorf("llama.cpp stage prepare is not metadata_ready: %s", report.Status)
	}
	modelID := strings.TrimSpace(fallbackModelID)
	if modelID == "" {
		modelID = strings.TrimSpace(report.ModelName)
	}
	plan := StageMaterializationPlan{
		Protocol:                           StageMaterializationPlanV1,
		Runtime:                            strings.TrimSpace(report.Runtime),
		Source:                             strings.TrimSpace(report.TensorManifest.Source),
		ModelID:                            modelID,
		ModelPath:                          strings.TrimSpace(report.ModelPath),
		StageIndex:                         report.StageIndex,
		LayerStart:                         report.StageStart,
		LayerEnd:                           report.StageEnd,
		TotalLayers:                        report.NLayer,
		ManifestOnly:                       report.TensorManifest.ManifestOnly,
		TotalTensorCount:                   report.TensorManifest.TotalTensorCount,
		SelectedTensorCount:                report.TensorManifest.SelectedTensorCount,
		StageTensorCount:                   report.TensorManifest.StageTensorCount,
		BoundaryTensorCount:                report.TensorManifest.BoundaryTensorCount,
		SelectedBytes:                      report.TensorManifest.SelectedBytes,
		MaterializationProbe:               report.MaterializationProbe,
		SelectedTensorMaterializationReady: report.SelectedTensorMaterializationReady,
		Tensors:                            append([]StageTensorRef(nil), report.TensorManifest.Tensors...),
	}
	if err := plan.Validate(); err != nil {
		return report, StageMaterializationPlan{}, err
	}
	return report, plan, nil
}

func llamaCPPStagePrepareJSON(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return trimmed
	}
	marker := []byte(`"kind": "cmesh.llamacpp_stage_prepare"`)
	markerAt := bytes.LastIndex(trimmed, marker)
	if markerAt < 0 {
		return trimmed
	}
	start := bytes.LastIndex(trimmed[:markerAt], []byte("{"))
	if start < 0 {
		return trimmed
	}
	return bytes.TrimSpace(trimmed[start:])
}

func (r LlamaCPPStageRuntime) Probe(ctx context.Context) StageRuntimeProbe {
	probe := StageRuntimeProbe{
		Name:    LlamaCPPStageRuntimeName,
		Runtime: LlamaCPPName,
		Ready:   false,
		RequiredHooks: []string{
			"automatic manager/worker discovery for stage runner and model paths",
			"long-lived stage session daemon with in-memory model and KV ownership",
			"production installer/runtime wiring for stage daemon startup and recovery",
		},
	}
	if err := ctx.Err(); err != nil {
		probe.Blockers = append(probe.Blockers, err.Error())
		return probe
	}
	if r.BinaryPath == "" {
		probe.Blockers = append(probe.Blockers, "cmesh-stage-runner path is required")
		return probe
	}
	probe.BinaryPath = r.BinaryPath
	info, err := os.Stat(r.BinaryPath)
	if err != nil {
		probe.Blockers = append(probe.Blockers, "cmesh-stage-runner is not accessible: "+err.Error())
		return probe
	}
	if info.IsDir() {
		probe.Blockers = append(probe.Blockers, "cmesh-stage-runner path points to a directory")
		return probe
	}
	probe.CLIReady = true
	probe.Blockers = append(probe.Blockers, "CMesh has selected-tensor, Qwen2 hidden-input, source-decode, relay-decode, terminal token export, worker dispatch, planner relay dispatch, token feedback, and file-backed per-stage KV continuity; long-lived daemon-owned model/KV sessions and production discovery are still pending")
	return probe
}

func (r LlamaCPPStageRuntime) PrepareStage(ctx context.Context, req StagePrepareRequest) (StagePrepareResult, error) {
	probe := r.Probe(ctx)
	if !probe.CLIReady {
		return StagePrepareResult{}, fmt.Errorf("llama.cpp stage runtime is not ready: %s", strings.Join(probe.Blockers, "; "))
	}
	modelPath := strings.TrimSpace(req.ModelPath)
	if modelPath == "" {
		return StagePrepareResult{}, fmt.Errorf("model path is required for llama.cpp stage prepare")
	}
	if strings.TrimSpace(req.ParentJobID) == "" {
		return StagePrepareResult{}, fmt.Errorf("parent_job_id is required")
	}
	if strings.TrimSpace(req.ModelID) == "" {
		return StagePrepareResult{}, fmt.Errorf("model_id is required")
	}
	if req.Shard.Stage.Index != req.Stage.Index || req.Shard.Stage.NodeID != req.Stage.NodeID || req.Shard.Stage.LayerStart != req.Stage.LayerStart || req.Shard.Stage.LayerEnd != req.Stage.LayerEnd {
		return StagePrepareResult{}, fmt.Errorf("distributed stage shard does not match stage assignment")
	}
	if req.Shard.Materialization != cdip.ShardLogicalLayers {
		return StagePrepareResult{}, fmt.Errorf("unsupported distributed shard materialization %q", req.Shard.Materialization)
	}
	args := []string{
		"--command", "prepare",
		"--model", modelPath,
		"--stage-start", strconv.Itoa(req.Stage.LayerStart),
		"--stage-end", strconv.Itoa(req.Stage.LayerEnd),
		"--stage-index", strconv.Itoa(req.Stage.Index),
		"--emit-tensor-list",
		"--materialize-selected-tensors",
	}
	output, err := exec.CommandContext(ctx, r.BinaryPath, args...).CombinedOutput()
	if err != nil {
		return StagePrepareResult{}, fmt.Errorf("llama.cpp stage prepare failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	_, plan, err := ParseLlamaCPPStagePrepareReport(output, req.ModelID)
	if err != nil {
		return StagePrepareResult{}, err
	}
	artifact, err := ShardArtifactFromMaterializationPlan(req.Shard.Artifact, plan)
	if err != nil {
		return StagePrepareResult{}, fmt.Errorf("build shard artifact metadata: %w", err)
	}
	return StagePrepareResult{
		Kind:                "cdip.stage_ready",
		ParentJobID:         req.ParentJobID,
		StageIndex:          req.Stage.Index,
		ModelID:             req.ModelID,
		Runtime:             LlamaCPPName,
		LayerStart:          req.Stage.LayerStart,
		LayerEnd:            req.Stage.LayerEnd,
		UpstreamNodeID:      req.UpstreamNodeID,
		DownstreamNodeID:    req.DownstreamNodeID,
		Materialization:     string(req.Shard.Materialization),
		SourceArtifact:      req.Shard.SourceArtifact,
		TargetArtifact:      req.Shard.TargetArtifact,
		Artifact:            artifact,
		ActivationProtocol:  ActivationStreamV1,
		MaterializationPlan: &plan,
	}, nil
}

func (r LlamaCPPStageRuntime) PrefillStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return StageCommandResult{}, llamaCPPStageUnsupported(ctx, r)
}

func (r LlamaCPPStageRuntime) DecodeStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return StageCommandResult{}, llamaCPPStageUnsupported(ctx, r)
}

func (r LlamaCPPStageRuntime) CompleteStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return StageCommandResult{}, llamaCPPStageUnsupported(ctx, r)
}

func (r LlamaCPPStageRuntime) AbortStage(ctx context.Context, req StageCommandRequest) (StageCommandResult, error) {
	return StageCommandResult{}, llamaCPPStageUnsupported(ctx, r)
}

func (r LlamaCPPStageRuntime) Prepare(ctx context.Context, req StagePrepareRequest) (StagePrepared, error) {
	result, err := r.PrepareStage(ctx, req)
	return StagePrepared{StagePrepareResult: result}, err
}

func (r LlamaCPPStageRuntime) Prefill(ctx context.Context, req StageCommandRequest) (StagePrefillResult, error) {
	result, err := r.PrefillStage(ctx, req)
	return StagePrefillResult{StageCommandResult: result}, err
}

func (r LlamaCPPStageRuntime) Decode(ctx context.Context, req StageCommandRequest) (StageDecodeResult, error) {
	result, err := r.DecodeStage(ctx, req)
	return StageDecodeResult{StageCommandResult: result}, err
}

func (r LlamaCPPStageRuntime) Complete(ctx context.Context, req StageCommandRequest) (StageCompleteResult, error) {
	result, err := r.CompleteStage(ctx, req)
	return StageCompleteResult{StageCommandResult: result}, err
}

func (r LlamaCPPStageRuntime) Abort(ctx context.Context, req StageCommandRequest) (StageAbortResult, error) {
	result, err := r.AbortStage(ctx, req)
	return StageAbortResult{StageCommandResult: result}, err
}

func llamaCPPStageUnsupported(ctx context.Context, runtime LlamaCPPStageRuntime) error {
	probe := runtime.Probe(ctx)
	if !probe.CLIReady {
		return fmt.Errorf("llama.cpp stage runtime is not ready: %s", strings.Join(probe.Blockers, "; "))
	}
	return fmt.Errorf("llama.cpp stage runtime is experimental and not executable yet: %s", strings.Join(probe.Blockers, "; "))
}
