package runtimes

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/models"
)

func TestLlamaCPPStageRuntimeProbeRequiresCLI(t *testing.T) {
	probe := NewLlamaCPPStageRuntime("").Probe(context.Background())
	if probe.Ready || probe.CLIReady {
		t.Fatalf("expected probe to be blocked without CLI, got %#v", probe)
	}
	if len(probe.Blockers) == 0 || !strings.Contains(strings.Join(probe.Blockers, " "), "cmesh-stage-runner path is required") {
		t.Fatalf("expected CLI blocker, got %#v", probe)
	}
}

func TestLlamaCPPStageRuntimeProbeFindsCLIButBlocksStageHooks(t *testing.T) {
	binary := t.TempDir() + "/llama-cli"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := NewLlamaCPPStageRuntime(binary).Probe(context.Background())
	if probe.Ready {
		t.Fatalf("stage runtime must remain non-ready until hooks exist: %#v", probe)
	}
	if !probe.CLIReady || probe.BinaryPath != binary {
		t.Fatalf("expected CLI readiness metadata, got %#v", probe)
	}
	if len(probe.RequiredHooks) == 0 || !strings.Contains(strings.Join(probe.Blockers, " "), "daemon-owned model/KV sessions") {
		t.Fatalf("expected stage hook blocker, got %#v", probe)
	}
}

func TestLlamaCPPStageRuntimePrepareStageExecsRunnerMetadata(t *testing.T) {
	binary := t.TempDir() + "/cmesh-stage-runner"
	if err := os.WriteFile(binary, []byte(`#!/bin/sh
cat <<'JSON'
{
  "kind": "cmesh.llamacpp_stage_prepare",
  "status": "metadata_ready",
  "runtime": "llama.cpp",
  "model_path": "/models/qwen.gguf",
  "model_name": "Qwen2.5 0.5B",
  "stage_index": 0,
  "stage_start": 0,
  "stage_end": 1,
  "tensor_manifest": {
    "source": "gguf metadata",
    "manifest_only": true,
    "total_tensor_count": 10,
    "selected_tensor_count": 3,
    "stage_tensor_count": 2,
    "boundary_tensor_count": 1,
    "selected_bytes": 96,
    "tensors": [
      {"name": "token_embd.weight", "type": "Q4_K", "bytes": 32, "boundary": true},
      {"name": "blk.0.attn_q.weight", "type": "Q4_K", "bytes": 32, "boundary": false},
      {"name": "blk.1.attn_q.weight", "type": "Q4_K", "bytes": 32, "boundary": false}
    ]
  },
  "executable": false,
  "guardrail": "metadata prepare only; not real layer sharding yet"
}
JSON
`), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := NewLlamaCPPStageRuntime(binary).PrepareStage(context.Background(), StagePrepareRequest{
		ParentJobID: "job-parent",
		StageJobID:  "job-stage-0",
		ModelID:     "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:   "/models/qwen.gguf",
		Stage:       models.DistributedStageInput{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 1},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 1},
			Runtime:         "llama.cpp",
			Materialization: cdip.ShardLogicalLayers,
			TargetArtifact:  "stage-0",
		},
		DownstreamNodeID: "node-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_ready" || result.Runtime != LlamaCPPName || result.MaterializationPlan == nil || result.MaterializationPlan.SelectedTensorCount != 3 {
		t.Fatalf("unexpected prepare result: %#v", result)
	}
}

func TestParseLlamaCPPStagePrepareReportBuildsMaterializationPlan(t *testing.T) {
	data := []byte(`{
	  "kind": "cmesh.llamacpp_stage_prepare",
	  "status": "metadata_ready",
	  "runtime": "llama.cpp",
	  "model_path": "/models/qwen.gguf",
	  "model_name": "Qwen2.5 0.5B",
	  "stage_index": 0,
	  "stage_start": 0,
	  "stage_end": 1,
	  "tensor_manifest": {
	    "source": "gguf metadata",
	    "manifest_only": true,
	    "total_tensor_count": 10,
	    "selected_tensor_count": 3,
	    "stage_tensor_count": 2,
	    "boundary_tensor_count": 1,
	    "selected_bytes": 96,
	    "tensors": [
	      {"name": "token_embd.weight", "type": "Q4_K", "bytes": 32, "boundary": true},
	      {"name": "blk.0.attn_q.weight", "type": "Q4_K", "bytes": 32, "boundary": false},
	      {"name": "blk.1.attn_q.weight", "type": "Q4_K", "bytes": 32, "boundary": false}
	    ]
	  },
	  "materialization_probe": {
	    "requested": true,
	    "attempted": true,
	    "loaded": true,
	    "status": "loaded",
	    "selected_tensor_count": 3,
	    "selected_bytes": 96,
	    "error": ""
	  },
	  "selected_tensor_materialization_ready": true,
	  "executable": false,
	  "guardrail": "metadata prepare only; not real layer sharding yet"
	}`)

	report, plan, err := ParseLlamaCPPStagePrepareReport(data, "qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatalf("expected parse to succeed: %v", err)
	}
	if report.ModelName != "Qwen2.5 0.5B" {
		t.Fatalf("unexpected report: %#v", report)
	}
	if plan.Protocol != StageMaterializationPlanV1 || plan.ModelID != "qwen2.5-0.5b-instruct-q4-k-m" || plan.SelectedTensorCount != 3 || len(plan.Tensors) != 3 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	if !plan.SelectedTensorMaterializationReady || !plan.MaterializationProbe.Loaded {
		t.Fatalf("expected selected tensor materialization probe to be loaded: %#v", plan.MaterializationProbe)
	}
}

func TestParseLlamaCPPStagePrepareReportIgnoresRuntimeLogs(t *testing.T) {
	data := []byte(`ggml_metal_device_init: GPU name: MTL0
llama_model_loader: - kv 24: tokenizer.chat_template str = {%- if tools %}
{
  "kind": "cmesh.llamacpp_stage_prepare",
  "status": "metadata_ready",
  "runtime": "llama.cpp",
  "model_path": "/models/qwen.gguf",
  "model_name": "Qwen2.5 0.5B",
  "stage_index": 1,
  "stage_start": 12,
  "stage_end": 23,
  "tensor_manifest": {
    "source": "gguf metadata",
    "manifest_only": true,
    "total_tensor_count": 10,
    "selected_tensor_count": 2,
    "stage_tensor_count": 2,
    "boundary_tensor_count": 0,
    "selected_bytes": 64,
    "tensors": [
      {"name": "blk.12.attn_q.weight", "type": "Q4_K", "bytes": 32, "boundary": false},
      {"name": "blk.23.attn_q.weight", "type": "Q4_K", "bytes": 32, "boundary": false}
    ]
  },
  "executable": false,
  "guardrail": "metadata prepare only; not real layer sharding yet"
}`)
	_, plan, err := ParseLlamaCPPStagePrepareReport(data, "qwen")
	if err != nil {
		t.Fatalf("expected noisy prepare output to parse: %v", err)
	}
	if plan.LayerStart != 12 || plan.LayerEnd != 23 || plan.SelectedTensorCount != 2 {
		t.Fatalf("unexpected plan from noisy output: %#v", plan)
	}
}

func TestParseLlamaCPPStagePrepareReportRejectsBlockedStatus(t *testing.T) {
	data := []byte(`{
	  "kind": "cmesh.llamacpp_stage_prepare",
	  "status": "blocked",
	  "runtime": "llama.cpp",
	  "tensor_manifest": {}
	}`)
	_, _, err := ParseLlamaCPPStagePrepareReport(data, "model")
	if err == nil || !strings.Contains(err.Error(), "not metadata_ready") {
		t.Fatalf("expected metadata status error, got %v", err)
	}
}
