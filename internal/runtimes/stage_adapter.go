package runtimes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cmesh/cmesh/internal/cdip"
)

const TensorEnvelopeV1 = "cdip.tensor-envelope-v1"
const LlamaCPPEmbeddingBatchPlanV1 = "cdip.llamacpp-embedding-batch-plan-v1"
const StageSessionV1 = "cdip.stage-session-v1"
const PhysicalShardPlanV1 = "cdip.physical-shard-plan-v1"

const (
	StageSessionModeFile   = "file_backed"
	StageSessionModeDaemon = "daemon"
)

type TensorEnvelope struct {
	Protocol             string `json:"protocol"`
	DType                string `json:"dtype"`
	Shape                []int  `json:"shape"`
	ByteCount            int    `json:"byte_count"`
	Checksum             string `json:"checksum"`
	Sequence             uint64 `json:"sequence"`
	StageIndex           int    `json:"stage_index"`
	KVCacheKey           string `json:"kv_cache_key,omitempty"`
	Encoding             string `json:"encoding,omitempty"`
	ParentJobID          string `json:"parent_job_id,omitempty"`
	StageJobID           string `json:"stage_job_id,omitempty"`
	UpstreamStageJobID   string `json:"upstream_stage_job_id,omitempty"`
	DownstreamStageJobID string `json:"downstream_stage_job_id,omitempty"`
	DownstreamNodeID     string `json:"downstream_node_id,omitempty"`
	TimingMS             int64  `json:"timing_ms"`
}

type LlamaCPPEmbeddingBatchPlan struct {
	Protocol                 string `json:"protocol"`
	SourceProtocol           string `json:"source_protocol"`
	SourceDType              string `json:"source_dtype"`
	LlamaBatchDType          string `json:"llama_batch_dtype"`
	Shape                    []int  `json:"shape"`
	NTokens                  int    `json:"n_tokens"`
	NEmbd                    int    `json:"n_embd"`
	ElementCount             int    `json:"element_count"`
	SourceByteCount          int    `json:"source_byte_count"`
	LlamaBatchByteCount      int    `json:"llama_batch_byte_count"`
	RequiresFloat32Expansion bool   `json:"requires_float32_expansion"`
	KVCacheKey               string `json:"kv_cache_key,omitempty"`
}

func TensorEnvelopeFromActivation(frame cdip.ActivationChunk, payload []byte, stageIndex int, upstreamStageJobID string, downstreamStageJobID string, downstreamNodeID string, timingMS int64) TensorEnvelope {
	return TensorEnvelope{
		Protocol:             TensorEnvelopeV1,
		DType:                strings.TrimSpace(frame.DType),
		Shape:                append([]int(nil), frame.Shape...),
		ByteCount:            len(payload),
		Checksum:             strings.TrimSpace(frame.Checksum),
		Sequence:             frame.Sequence,
		StageIndex:           stageIndex,
		Encoding:             strings.TrimSpace(frame.Encoding),
		ParentJobID:          frame.ParentJobID,
		StageJobID:           frame.StageJobID,
		UpstreamStageJobID:   strings.TrimSpace(upstreamStageJobID),
		DownstreamStageJobID: strings.TrimSpace(downstreamStageJobID),
		DownstreamNodeID:     strings.TrimSpace(downstreamNodeID),
		TimingMS:             timingMS,
	}
}

func (e TensorEnvelope) ValidatePayload(payload []byte) error {
	if strings.TrimSpace(e.Protocol) != TensorEnvelopeV1 {
		return fmt.Errorf("unsupported tensor envelope protocol %q", e.Protocol)
	}
	if strings.TrimSpace(e.DType) == "" {
		return fmt.Errorf("tensor envelope dtype is required")
	}
	if len(e.Shape) == 0 {
		return fmt.Errorf("tensor envelope shape is required")
	}
	for _, dim := range e.Shape {
		if dim <= 0 {
			return fmt.Errorf("tensor envelope shape contains invalid dimension %d", dim)
		}
	}
	if e.ByteCount != len(payload) {
		return fmt.Errorf("tensor envelope byte count mismatch: envelope=%d actual=%d", e.ByteCount, len(payload))
	}
	checksum := strings.TrimSpace(e.Checksum)
	if strings.HasPrefix(checksum, "sha256:") {
		sum := sha256.Sum256(payload)
		actual := "sha256:" + hex.EncodeToString(sum[:])
		if checksum != actual {
			return fmt.Errorf("tensor envelope checksum mismatch: envelope=%s actual=%s", checksum, actual)
		}
	}
	return nil
}

func BuildLlamaCPPEmbeddingBatchPlan(envelope TensorEnvelope) (LlamaCPPEmbeddingBatchPlan, error) {
	if strings.TrimSpace(envelope.Protocol) != TensorEnvelopeV1 {
		return LlamaCPPEmbeddingBatchPlan{}, fmt.Errorf("unsupported tensor envelope protocol %q", envelope.Protocol)
	}
	dtype := strings.ToLower(strings.TrimSpace(envelope.DType))
	if dtype != "f16" && dtype != "f32" {
		return LlamaCPPEmbeddingBatchPlan{}, fmt.Errorf("llama.cpp embedding bridge requires f16 or f32 activation, got %q", envelope.DType)
	}
	if len(envelope.Shape) != 2 && len(envelope.Shape) != 3 {
		return LlamaCPPEmbeddingBatchPlan{}, fmt.Errorf("llama.cpp embedding bridge expects shape [tokens,n_embd] or [batch,tokens,n_embd], got %v", envelope.Shape)
	}
	shape := append([]int(nil), envelope.Shape...)
	for _, dim := range shape {
		if dim <= 0 {
			return LlamaCPPEmbeddingBatchPlan{}, fmt.Errorf("llama.cpp embedding bridge shape contains invalid dimension %d", dim)
		}
	}
	if len(shape) == 3 && shape[0] != 1 {
		return LlamaCPPEmbeddingBatchPlan{}, fmt.Errorf("llama.cpp embedding bridge currently supports one sequence per stage batch, got batch=%d", shape[0])
	}
	nTokens := shape[len(shape)-2]
	nEmbd := shape[len(shape)-1]
	elementCount := nTokens * nEmbd
	bytesPerElement := 4
	if dtype == "f16" {
		bytesPerElement = 2
	}
	expectedBytes := elementCount * bytesPerElement
	if envelope.ByteCount != expectedBytes {
		return LlamaCPPEmbeddingBatchPlan{}, fmt.Errorf("llama.cpp embedding bridge byte count mismatch: dtype=%s shape=%v expected=%d actual=%d", dtype, shape, expectedBytes, envelope.ByteCount)
	}
	return LlamaCPPEmbeddingBatchPlan{
		Protocol:                 LlamaCPPEmbeddingBatchPlanV1,
		SourceProtocol:           TensorEnvelopeV1,
		SourceDType:              dtype,
		LlamaBatchDType:          "f32",
		Shape:                    shape,
		NTokens:                  nTokens,
		NEmbd:                    nEmbd,
		ElementCount:             elementCount,
		SourceByteCount:          envelope.ByteCount,
		LlamaBatchByteCount:      elementCount * 4,
		RequiresFloat32Expansion: dtype == "f16",
		KVCacheKey:               strings.TrimSpace(envelope.KVCacheKey),
	}, nil
}

type StagePrepared struct {
	StagePrepareResult
	Session *StageSession `json:"session,omitempty"`
}

type StageSession struct {
	Protocol             string `json:"protocol"`
	Mode                 string `json:"mode"`
	SessionID            string `json:"session_id"`
	ParentJobID          string `json:"parent_job_id,omitempty"`
	StageJobID           string `json:"stage_job_id,omitempty"`
	ModelID              string `json:"model_id"`
	ModelPath            string `json:"model_path,omitempty"`
	StageIndex           int    `json:"stage_index"`
	LayerStart           int    `json:"layer_start"`
	LayerEnd             int    `json:"layer_end"`
	KVCacheKey           string `json:"kv_cache_key,omitempty"`
	StatePath            string `json:"state_path,omitempty"`
	Endpoint             string `json:"endpoint,omitempty"`
	RuntimeBackend       string `json:"runtime_backend,omitempty"`
	RuntimeStatus        string `json:"runtime_status,omitempty"`
	PersistentModel      bool   `json:"persistent_model"`
	PersistentKVInMemory bool   `json:"persistent_kv_in_memory"`
	Ready                bool   `json:"ready"`
}

func (s StageSession) Validate() error {
	if strings.TrimSpace(s.Protocol) != StageSessionV1 {
		return fmt.Errorf("unsupported stage session protocol %q", s.Protocol)
	}
	mode := strings.TrimSpace(s.Mode)
	if mode != StageSessionModeFile && mode != StageSessionModeDaemon {
		return fmt.Errorf("unsupported stage session mode %q", s.Mode)
	}
	if strings.TrimSpace(s.SessionID) == "" {
		return fmt.Errorf("stage session id is required")
	}
	if strings.TrimSpace(s.ModelID) == "" {
		return fmt.Errorf("stage session model_id is required")
	}
	if s.StageIndex < 0 {
		return fmt.Errorf("stage session stage_index is invalid")
	}
	if s.LayerStart < 0 || s.LayerEnd < s.LayerStart {
		return fmt.Errorf("stage session layer range is invalid")
	}
	if mode == StageSessionModeFile && strings.TrimSpace(s.StatePath) == "" {
		return fmt.Errorf("file-backed stage session state_path is required")
	}
	if mode == StageSessionModeDaemon && strings.TrimSpace(s.Endpoint) == "" {
		return fmt.Errorf("daemon stage session endpoint is required")
	}
	return nil
}

type StageTensorRef struct {
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"`
	Bytes    uint64 `json:"bytes"`
	Boundary bool   `json:"boundary,omitempty"`
}

type StageMaterializationPlan struct {
	Protocol                           string                    `json:"protocol"`
	Runtime                            string                    `json:"runtime"`
	Source                             string                    `json:"source"`
	ModelID                            string                    `json:"model_id"`
	ModelPath                          string                    `json:"model_path,omitempty"`
	StageIndex                         int                       `json:"stage_index"`
	LayerStart                         int                       `json:"layer_start"`
	LayerEnd                           int                       `json:"layer_end"`
	TotalLayers                        int                       `json:"total_layers,omitempty"`
	ManifestOnly                       bool                      `json:"manifest_only"`
	TotalTensorCount                   int64                     `json:"total_tensor_count"`
	SelectedTensorCount                int64                     `json:"selected_tensor_count"`
	StageTensorCount                   int64                     `json:"stage_tensor_count"`
	BoundaryTensorCount                int64                     `json:"boundary_tensor_count"`
	SelectedBytes                      uint64                    `json:"selected_bytes"`
	MaterializationProbe               StageMaterializationProbe `json:"materialization_probe,omitempty"`
	SelectedTensorMaterializationReady bool                      `json:"selected_tensor_materialization_ready"`
	Tensors                            []StageTensorRef          `json:"tensors,omitempty"`
}

const StageMaterializationPlanV1 = "cdip.stage-materialization-plan-v1"

type StageMaterializationProbe struct {
	Requested           bool   `json:"requested"`
	Attempted           bool   `json:"attempted"`
	Loaded              bool   `json:"loaded"`
	Status              string `json:"status,omitempty"`
	SelectedTensorCount int64  `json:"selected_tensor_count,omitempty"`
	SelectedBytes       uint64 `json:"selected_bytes,omitempty"`
	Error               string `json:"error,omitempty"`
}

type PhysicalShardPlan struct {
	Protocol                       string   `json:"protocol"`
	Status                         string   `json:"status"`
	Runtime                        string   `json:"runtime"`
	ModelID                        string   `json:"model_id"`
	ModelPath                      string   `json:"model_path,omitempty"`
	StageIndex                     int      `json:"stage_index"`
	LayerStart                     int      `json:"layer_start"`
	LayerEnd                       int      `json:"layer_end"`
	SelectedTensorManifestURI      string   `json:"selected_tensor_manifest_uri,omitempty"`
	SelectedTensorManifestChecksum string   `json:"selected_tensor_manifest_checksum,omitempty"`
	SelectedTensorCount            int64    `json:"selected_tensor_count"`
	SelectedBytes                  uint64   `json:"selected_bytes"`
	TargetArtifact                 string   `json:"target_artifact,omitempty"`
	PlanURI                        string   `json:"plan_uri,omitempty"`
	TargetURI                      string   `json:"target_uri,omitempty"`
	TargetChecksum                 string   `json:"target_checksum,omitempty"`
	ArtifactBytes                  uint64   `json:"artifact_bytes,omitempty"`
	ArtifactKind                   string   `json:"artifact_kind,omitempty"`
	LoadableGGUF                   bool     `json:"loadable_gguf"`
	PhysicalArtifactReady          bool     `json:"physical_artifact_ready"`
	Blockers                       []string `json:"blockers,omitempty"`
}

func BuildPhysicalShardPlan(plan StageMaterializationPlan, manifestURI string, manifestChecksum string, targetArtifact string) (PhysicalShardPlan, error) {
	if err := plan.Validate(); err != nil {
		return PhysicalShardPlan{}, err
	}
	out := PhysicalShardPlan{
		Protocol:                       PhysicalShardPlanV1,
		Status:                         "blocked_missing_physical_shard_writer",
		Runtime:                        plan.Runtime,
		ModelID:                        plan.ModelID,
		ModelPath:                      plan.ModelPath,
		StageIndex:                     plan.StageIndex,
		LayerStart:                     plan.LayerStart,
		LayerEnd:                       plan.LayerEnd,
		SelectedTensorManifestURI:      strings.TrimSpace(manifestURI),
		SelectedTensorManifestChecksum: strings.TrimSpace(manifestChecksum),
		SelectedTensorCount:            plan.SelectedTensorCount,
		SelectedBytes:                  plan.SelectedBytes,
		TargetArtifact:                 strings.TrimSpace(targetArtifact),
		ArtifactKind:                   "plan",
		LoadableGGUF:                   false,
		PhysicalArtifactReady:          false,
		Blockers: []string{
			"write standalone GGUF shard containing selected tensors and required metadata",
			"teach llama.cpp stage runtime to open the shard without original full model",
			"validate shard checksum and tensor allowlist before stage session prepare",
		},
	}
	if out.SelectedTensorManifestURI == "" {
		return PhysicalShardPlan{}, fmt.Errorf("selected tensor manifest uri is required")
	}
	if !strings.HasPrefix(out.SelectedTensorManifestChecksum, "sha256:") {
		return PhysicalShardPlan{}, fmt.Errorf("selected tensor manifest checksum must be sha256")
	}
	return out, nil
}

func (p StageMaterializationPlan) Validate() error {
	if strings.TrimSpace(p.Protocol) != StageMaterializationPlanV1 {
		return fmt.Errorf("unsupported stage materialization plan protocol %q", p.Protocol)
	}
	if strings.TrimSpace(p.Runtime) == "" {
		return fmt.Errorf("stage materialization runtime is required")
	}
	if strings.TrimSpace(p.ModelID) == "" {
		return fmt.Errorf("stage materialization model_id is required")
	}
	if p.StageIndex < 0 {
		return fmt.Errorf("stage materialization stage_index is invalid")
	}
	if p.LayerStart < 0 || p.LayerEnd < p.LayerStart {
		return fmt.Errorf("stage materialization layer range is invalid")
	}
	if p.SelectedTensorCount < 0 || p.StageTensorCount < 0 || p.BoundaryTensorCount < 0 {
		return fmt.Errorf("stage materialization tensor counts cannot be negative")
	}
	if p.SelectedTensorCount != p.StageTensorCount+p.BoundaryTensorCount {
		return fmt.Errorf("stage materialization selected tensor count mismatch")
	}
	if len(p.Tensors) > 0 && int64(len(p.Tensors)) != p.SelectedTensorCount {
		return fmt.Errorf("stage materialization tensor list length mismatch: list=%d selected=%d", len(p.Tensors), p.SelectedTensorCount)
	}
	var bytes uint64
	for _, tensor := range p.Tensors {
		if strings.TrimSpace(tensor.Name) == "" {
			return fmt.Errorf("stage materialization tensor name is required")
		}
		if tensor.Bytes == 0 {
			return fmt.Errorf("stage materialization tensor %q has zero bytes", tensor.Name)
		}
		bytes += tensor.Bytes
	}
	if len(p.Tensors) > 0 && bytes != p.SelectedBytes {
		return fmt.Errorf("stage materialization selected bytes mismatch: list=%d selected=%d", bytes, p.SelectedBytes)
	}
	if p.SelectedTensorMaterializationReady && !p.MaterializationProbe.Loaded {
		return fmt.Errorf("stage materialization cannot be ready unless selected tensor probe loaded")
	}
	if p.MaterializationProbe.SelectedTensorCount > 0 && p.MaterializationProbe.SelectedTensorCount != p.SelectedTensorCount {
		return fmt.Errorf("stage materialization probe tensor count mismatch")
	}
	if p.MaterializationProbe.SelectedBytes > 0 && p.MaterializationProbe.SelectedBytes != p.SelectedBytes {
		return fmt.Errorf("stage materialization probe selected bytes mismatch")
	}
	return nil
}

func ShardArtifactFromMaterializationPlan(base cdip.ShardArtifact, plan StageMaterializationPlan) (cdip.ShardArtifact, error) {
	if err := plan.Validate(); err != nil {
		return cdip.ShardArtifact{}, err
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return cdip.ShardArtifact{}, err
	}
	sum := sha256.Sum256(body)
	out := base
	if strings.TrimSpace(out.Protocol) == "" {
		out.Protocol = "cdip.shard-artifact-v1"
	}
	out.Status = "selected_tensor_manifest_ready"
	out.LayerStart = plan.LayerStart
	out.LayerEnd = plan.LayerEnd
	out.ExpectedBytes = plan.SelectedBytes
	out.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	out.PhysicalArtifactReady = false
	return out, nil
}

type StagePrefillResult struct {
	StageCommandResult
}

type StageDecodeResult struct {
	StageCommandResult
}

type StageCompleteResult struct {
	StageCommandResult
}

type StageAbortResult struct {
	StageCommandResult
}

type ModelStageAdapter interface {
	Prepare(ctx context.Context, req StagePrepareRequest) (StagePrepared, error)
	Prefill(ctx context.Context, req StageCommandRequest) (StagePrefillResult, error)
	Decode(ctx context.Context, req StageCommandRequest) (StageDecodeResult, error)
	Complete(ctx context.Context, req StageCommandRequest) (StageCompleteResult, error)
	Abort(ctx context.Context, req StageCommandRequest) (StageAbortResult, error)
}
