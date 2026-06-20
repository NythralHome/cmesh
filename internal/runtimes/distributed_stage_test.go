package runtimes

import (
	"context"
	"strings"
	"testing"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/transport"
)

func TestLogicalStageRuntimePrepareStage(t *testing.T) {
	runtime := NewLogicalStageRuntime(LlamaCPPName)
	result, err := runtime.PrepareStage(context.Background(), StagePrepareRequest{
		ParentJobID: "job-parent",
		StageJobID:  "job-stage",
		ModelID:     "qwen2.5-0.5b-instruct-q4-k-m",
		Stage:       models.DistributedStageInput{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 11},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 11},
			Runtime:         LlamaCPPName,
			Materialization: cdip.ShardLogicalLayers,
			SourceArtifact:  "https://example.test/model.gguf",
			TargetArtifact:  "qwen.stage-0",
		},
		DownstreamNodeID: "node-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_ready" || result.ActivationProtocol != ActivationStreamV1 || result.LayerEnd != 11 {
		t.Fatalf("unexpected stage prepare result: %#v", result)
	}
}

func TestLogicalStageRuntimeRejectsMismatchedShard(t *testing.T) {
	runtime := NewLogicalStageRuntime(LlamaCPPName)
	_, err := runtime.PrepareStage(context.Background(), StagePrepareRequest{
		ParentJobID: "job-parent",
		StageJobID:  "job-stage",
		ModelID:     "qwen2.5-0.5b-instruct-q4-k-m",
		Stage:       models.DistributedStageInput{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 11},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 1, NodeID: "node-a", LayerStart: 12, LayerEnd: 23},
			Runtime:         LlamaCPPName,
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected mismatched shard error, got %v", err)
	}
}

func TestLogicalStageRuntimeDecodeSendsActivationFrame(t *testing.T) {
	ctx := context.Background()
	stream := transport.StreamID{ParentJobID: "job-parent", StageJobID: "job-stage-0"}
	bus := transport.NewMemoryActivationTransport(1)
	reader, err := bus.OpenReader(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	runtime := NewLogicalStageRuntime(LlamaCPPName)
	result, err := runtime.DecodeStage(ctx, StageCommandRequest{
		ParentJobID:          stream.ParentJobID,
		StageJobID:           stream.StageJobID,
		StageIndex:           0,
		Step:                 3,
		ActivationTransport:  bus,
		UpstreamStageJobID:   "job-stage-0",
		DownstreamStageJobID: "job-stage-1",
		DownstreamNodeID:     "node-b",
		ActivationPayload:    []byte{3, 9},
		ActivationDType:      "f16",
		ActivationShape:      []int{1, 1, 1},
		ActivationChecksum:   "mock:3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_decode" || result.ActivationFrame == nil || result.ActivationBytes != 2 {
		t.Fatalf("unexpected decode result: %#v", result)
	}
	if result.TensorEnvelope == nil || result.TensorEnvelope.Protocol != TensorEnvelopeV1 || result.TensorEnvelope.DType != "f16" || result.TensorEnvelope.ByteCount != 2 || result.TensorEnvelope.DownstreamStageJobID != "job-stage-1" {
		t.Fatalf("unexpected tensor envelope: %#v", result.TensorEnvelope)
	}
	frame, err := reader.Receive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Header.Sequence != 3 || frame.Header.Checksum != "mock:3" || string(frame.Payload) != string([]byte{3, 9}) {
		t.Fatalf("unexpected activation frame: %#v", frame)
	}
}

func TestStageMaterializationPlanValidate(t *testing.T) {
	plan := StageMaterializationPlan{
		Protocol:            StageMaterializationPlanV1,
		Runtime:             "llama.cpp",
		Source:              "gguf metadata",
		ModelID:             "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:           "/models/qwen.gguf",
		StageIndex:          0,
		LayerStart:          0,
		LayerEnd:            1,
		ManifestOnly:        true,
		TotalTensorCount:    10,
		SelectedTensorCount: 3,
		StageTensorCount:    2,
		BoundaryTensorCount: 1,
		SelectedBytes:       96,
		Tensors: []StageTensorRef{
			{Name: "token_embd.weight", Type: "Q4_K", Bytes: 32, Boundary: true},
			{Name: "blk.0.attn_q.weight", Type: "Q4_K", Bytes: 32},
			{Name: "blk.1.attn_q.weight", Type: "Q4_K", Bytes: 32},
		},
	}
	if err := plan.Validate(); err != nil {
		t.Fatalf("expected plan to validate: %v", err)
	}
}

func TestShardArtifactFromMaterializationPlan(t *testing.T) {
	plan := StageMaterializationPlan{
		Protocol:            StageMaterializationPlanV1,
		Runtime:             "llama.cpp",
		Source:              "gguf metadata",
		ModelID:             "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:           "/models/qwen.gguf",
		StageIndex:          2,
		LayerStart:          16,
		LayerEnd:            23,
		ManifestOnly:        true,
		TotalTensorCount:    10,
		SelectedTensorCount: 2,
		StageTensorCount:    1,
		BoundaryTensorCount: 1,
		SelectedBytes:       64,
		MaterializationProbe: StageMaterializationProbe{
			Requested:           true,
			Attempted:           true,
			Loaded:              true,
			Status:              "loaded",
			SelectedTensorCount: 2,
			SelectedBytes:       64,
		},
		SelectedTensorMaterializationReady: true,
		Tensors: []StageTensorRef{
			{Name: "output.weight", Type: "Q4_K", Bytes: 32, Boundary: true},
			{Name: "blk.16.attn_q.weight", Type: "Q4_K", Bytes: 32},
		},
	}
	artifact, err := ShardArtifactFromMaterializationPlan(cdip.ShardArtifact{URI: "file:///tmp/stage-2.manifest.json"}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Protocol != "cdip.shard-artifact-v1" || artifact.Status != "selected_tensor_manifest_ready" {
		t.Fatalf("unexpected artifact status: %#v", artifact)
	}
	if artifact.LayerStart != 16 || artifact.LayerEnd != 23 || artifact.ExpectedBytes != 64 {
		t.Fatalf("unexpected artifact range/bytes: %#v", artifact)
	}
	if artifact.PhysicalArtifactReady {
		t.Fatalf("selected tensor manifest must not claim physical GGUF readiness: %#v", artifact)
	}
	if !strings.HasPrefix(artifact.Checksum, "sha256:") {
		t.Fatalf("expected sha256 checksum, got %#v", artifact)
	}
}

func TestBuildPhysicalShardPlan(t *testing.T) {
	plan := StageMaterializationPlan{
		Protocol:            StageMaterializationPlanV1,
		Runtime:             "llama.cpp",
		Source:              "gguf metadata",
		ModelID:             "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:           "/models/qwen.gguf",
		StageIndex:          1,
		LayerStart:          8,
		LayerEnd:            15,
		ManifestOnly:        true,
		TotalTensorCount:    10,
		SelectedTensorCount: 1,
		StageTensorCount:    1,
		SelectedBytes:       32,
		Tensors: []StageTensorRef{
			{Name: "blk.8.attn_q.weight", Type: "Q4_K", Bytes: 32},
		},
	}
	physical, err := BuildPhysicalShardPlan(plan, "file:///tmp/stage-1-materialization-plan.json", "sha256:abc", "qwen.stage-1")
	if err != nil {
		t.Fatal(err)
	}
	if physical.Protocol != PhysicalShardPlanV1 || physical.Status != "blocked_missing_physical_shard_writer" || physical.PhysicalArtifactReady {
		t.Fatalf("unexpected physical shard plan status: %#v", physical)
	}
	if physical.SelectedTensorManifestURI == "" || physical.SelectedTensorManifestChecksum == "" || len(physical.Blockers) == 0 {
		t.Fatalf("expected manifest references and blockers: %#v", physical)
	}
}

func TestStageMaterializationPlanValidateRejectsMismatchedCounts(t *testing.T) {
	plan := StageMaterializationPlan{
		Protocol:            StageMaterializationPlanV1,
		Runtime:             "llama.cpp",
		ModelID:             "model",
		StageIndex:          0,
		LayerStart:          0,
		LayerEnd:            1,
		SelectedTensorCount: 2,
		StageTensorCount:    2,
		BoundaryTensorCount: 1,
	}
	if err := plan.Validate(); err == nil {
		t.Fatalf("expected selected tensor count mismatch")
	}
}

func TestStageMaterializationPlanValidateRejectsMismatchedBytes(t *testing.T) {
	plan := StageMaterializationPlan{
		Protocol:            StageMaterializationPlanV1,
		Runtime:             "llama.cpp",
		ModelID:             "model",
		StageIndex:          0,
		LayerStart:          0,
		LayerEnd:            1,
		SelectedTensorCount: 1,
		StageTensorCount:    1,
		SelectedBytes:       16,
		Tensors: []StageTensorRef{
			{Name: "blk.0.attn_q.weight", Bytes: 8},
		},
	}
	if err := plan.Validate(); err == nil {
		t.Fatalf("expected selected bytes mismatch")
	}
}

func TestStageMaterializationPlanValidateRejectsFalseReadyProbe(t *testing.T) {
	plan := StageMaterializationPlan{
		Protocol:            StageMaterializationPlanV1,
		Runtime:             "llama.cpp",
		ModelID:             "model",
		StageIndex:          0,
		LayerStart:          0,
		LayerEnd:            1,
		SelectedTensorCount: 1,
		StageTensorCount:    1,
		SelectedBytes:       32,
		MaterializationProbe: StageMaterializationProbe{
			Requested:           true,
			Attempted:           true,
			Loaded:              false,
			Status:              "failed",
			SelectedTensorCount: 1,
			SelectedBytes:       32,
		},
		SelectedTensorMaterializationReady: true,
		Tensors: []StageTensorRef{
			{Name: "blk.0.attn_q.weight", Bytes: 32},
		},
	}
	err := plan.Validate()
	if err == nil || !strings.Contains(err.Error(), "probe loaded") {
		t.Fatalf("expected loaded probe guardrail error, got %v", err)
	}
}

func TestStageSessionValidateFileBacked(t *testing.T) {
	session := StageSession{
		Protocol:        StageSessionV1,
		Mode:            StageSessionModeFile,
		SessionID:       "session-stage-0",
		ParentJobID:     "job-parent",
		StageJobID:      "job-stage-0",
		ModelID:         "qwen2.5-14b-instruct-q4-k-m",
		StageIndex:      0,
		LayerStart:      0,
		LayerEnd:        15,
		KVCacheKey:      "cdip-session-job-parent:kv",
		StatePath:       "/var/lib/cmesh/stage-work/stage-0/session.seq",
		PersistentModel: false,
		Ready:           true,
	}
	if err := session.Validate(); err != nil {
		t.Fatalf("expected file-backed stage session to validate: %v", err)
	}
}

func TestStageSessionValidateDaemon(t *testing.T) {
	session := StageSession{
		Protocol:             StageSessionV1,
		Mode:                 StageSessionModeDaemon,
		SessionID:            "session-stage-1",
		ModelID:              "qwen2.5-14b-instruct-q4-k-m",
		StageIndex:           1,
		LayerStart:           16,
		LayerEnd:             31,
		Endpoint:             "unix:///var/run/cmesh/stage-1.sock",
		PersistentModel:      true,
		PersistentKVInMemory: true,
		Ready:                true,
	}
	if err := session.Validate(); err != nil {
		t.Fatalf("expected daemon stage session to validate: %v", err)
	}
}

func TestStageSessionValidateRejectsMissingDaemonEndpoint(t *testing.T) {
	err := (StageSession{
		Protocol:   StageSessionV1,
		Mode:       StageSessionModeDaemon,
		SessionID:  "session-stage-1",
		ModelID:    "model",
		StageIndex: 1,
		LayerStart: 1,
		LayerEnd:   2,
	}).Validate()
	if err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("expected daemon endpoint validation error, got %v", err)
	}
}

func TestBuildLlamaCPPEmbeddingBatchPlanExpandsF16Activation(t *testing.T) {
	plan, err := BuildLlamaCPPEmbeddingBatchPlan(TensorEnvelope{
		Protocol:   TensorEnvelopeV1,
		DType:      "f16",
		Shape:      []int{1, 2, 4},
		ByteCount:  16,
		KVCacheKey: "kv-stage-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Protocol != LlamaCPPEmbeddingBatchPlanV1 || plan.NTokens != 2 || plan.NEmbd != 4 || plan.ElementCount != 8 {
		t.Fatalf("unexpected embedding batch plan: %#v", plan)
	}
	if !plan.RequiresFloat32Expansion || plan.SourceByteCount != 16 || plan.LlamaBatchByteCount != 32 || plan.LlamaBatchDType != "f32" {
		t.Fatalf("unexpected f16 expansion metadata: %#v", plan)
	}
}

func TestBuildLlamaCPPEmbeddingBatchPlanAcceptsF32Activation(t *testing.T) {
	plan, err := BuildLlamaCPPEmbeddingBatchPlan(TensorEnvelope{
		Protocol:  TensorEnvelopeV1,
		DType:     "f32",
		Shape:     []int{3, 4},
		ByteCount: 48,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RequiresFloat32Expansion || plan.NTokens != 3 || plan.NEmbd != 4 || plan.LlamaBatchByteCount != 48 {
		t.Fatalf("unexpected f32 bridge plan: %#v", plan)
	}
}

func TestBuildLlamaCPPEmbeddingBatchPlanRejectsInvalidBytes(t *testing.T) {
	_, err := BuildLlamaCPPEmbeddingBatchPlan(TensorEnvelope{
		Protocol:  TensorEnvelopeV1,
		DType:     "f16",
		Shape:     []int{1, 2, 4},
		ByteCount: 15,
	})
	if err == nil || !strings.Contains(err.Error(), "byte count mismatch") {
		t.Fatalf("expected byte count mismatch, got %v", err)
	}
}
