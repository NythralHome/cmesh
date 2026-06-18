package runtimes

import (
	"context"
	"strings"
	"testing"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/models"
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
