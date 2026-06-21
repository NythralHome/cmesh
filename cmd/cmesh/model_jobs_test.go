package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/protocol"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/runtimes"
	"github.com/cmesh/cmesh/internal/transport"
	"github.com/cmesh/cmesh/internal/workerstatus"
)

type panicStageSessionBackend struct{}

func (panicStageSessionBackend) Kind() string {
	return "panic-test"
}

func (panicStageSessionBackend) NativeKV() bool {
	return true
}

func (panicStageSessionBackend) Status() stageDaemonBackendStatus {
	return stageDaemonBackendStatus{Kind: "panic-test", Ready: true, DecodeReady: true}
}

func (panicStageSessionBackend) Prepare(_ context.Context, session runtimes.StageSession, _ stageDaemonSessionRequest) (runtimes.StageSession, error) {
	session.RuntimeBackend = "panic-test"
	session.RuntimeStatus = "ready"
	session.Ready = true
	return session, nil
}

func (panicStageSessionBackend) Decode(context.Context, runtimes.StageSession, stageDaemonDecodeRequest) (stageDaemonBackendDecodeResult, error) {
	panic("decode exploded")
}

func (panicStageSessionBackend) Close(context.Context, runtimes.StageSession) error {
	return nil
}

func TestDistributedGenerateJobTypeIsKnownButNotExecutableYet(t *testing.T) {
	if !isModelJobType(models.JobGenerateDistributed) {
		t.Fatalf("expected %s to be tracked as a model job", models.JobGenerateDistributed)
	}
	_, err := executeWorkerJob(jobs.Job{
		Type:  models.JobGenerateDistributed,
		Input: `{"model_id":"qwen2.5-7b-instruct-q4-k-m","prompt":"hello"}`,
	}, cluster.ResourceSnapshot{}, t.TempDir(), "node-a", time.Now().UTC(), "")
	if err == nil {
		t.Fatal("expected distributed parent job to be blocked on workers")
	}
	if !strings.Contains(err.Error(), "coordinator-owned") {
		t.Fatalf("expected coordinator-owned parent job error, got %v", err)
	}
}

func TestExecuteDistributedRPCGenerateAddsRPCArgument(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake llama-cli test is unix-only")
	}
	dir := t.TempDir()
	cli := filepath.Join(dir, "llama-cli")
	argsPath := filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CMESH_FAKE_LLAMA_ARGS\"\nprintf 'hello from rpc\\n'\n"
	if err := os.WriteFile(cli, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CMESH_FAKE_LLAMA_ARGS", argsPath)
	t.Setenv("CMESH_MODEL_GENERATE_TIMEOUT", "5s")

	cacheDir := filepath.Join(dir, "cache")
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	path := modelPath(cacheDir, model)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(models.DistributedRPCGenerateInput{
		ModelID:      model.ID,
		Prompt:       "hello",
		MaxTokens:    8,
		Temperature:  "0.1",
		RPCEndpoints: []string{"10.0.0.10:50052", "10.0.0.11:50052"},
		ExecutionPlan: protocol.DistributedRPCExecutionPlan{
			Protocol:          protocol.DistributedRPCProtocol,
			ProtocolVersion:   protocol.DistributedRPCProtocolVersion,
			PlanSchemaVersion: protocol.DistributedRPCPlanSchemaVersion,
			Mode:              "llama.cpp-rpc",
			ModelID:           model.ID,
			CoordinatorNodeID: "node-a",
			RPCEndpoints:      []string{"10.0.0.10:50052", "10.0.0.11:50052"},
			Backends: []protocol.DistributedRPCBackend{{
				NodeID:   "node-b",
				Endpoint: "10.0.0.10:50052",
			}, {
				NodeID:   "node-c",
				Endpoint: "10.0.0.11:50052",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		Type:  models.JobGenerateDistributedRPC,
		Input: string(input),
	}, cluster.ResourceSnapshot{}, cacheDir, "node-a", time.Now().UTC(), "")
	if err != nil {
		t.Fatal(err)
	}
	var result modelGenerateResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != models.JobGenerateDistributedRPC || result.Output != "hello from rpc" {
		t.Fatalf("unexpected distributed rpc result: %#v", result)
	}
	if result.RPCEndpointCount != 2 || len(result.RPCEndpoints) != 2 || result.RPCEndpoints[0] != "10.0.0.10:50052" || result.RPCEndpoints[1] != "10.0.0.11:50052" {
		t.Fatalf("unexpected distributed rpc trace: %#v", result)
	}
	if result.ExecutionResult.Protocol != protocol.DistributedRPCProtocol || result.ExecutionResult.ProtocolVersion != protocol.DistributedRPCProtocolVersion {
		t.Fatalf("unexpected distributed rpc execution result protocol: %#v", result.ExecutionResult)
	}
	if result.ExecutionResult.Kind != models.JobGenerateDistributedRPC || result.ExecutionResult.ModelID != model.ID || result.ExecutionResult.Output != "hello from rpc" {
		t.Fatalf("unexpected distributed rpc execution result: %#v", result.ExecutionResult)
	}
	if result.ExecutionResult.RPCEndpointCount != 2 || len(result.ExecutionResult.RPCEndpoints) != 2 {
		t.Fatalf("unexpected distributed rpc execution result endpoints: %#v", result.ExecutionResult)
	}
	if !result.ExecutionResult.RPCEnabled || result.ExecutionResult.CoordinatorNodeID != "node-a" || len(result.ExecutionResult.Backends) != 2 {
		t.Fatalf("unexpected distributed rpc execution trace: %#v", result.ExecutionResult)
	}
	if result.ExecutionResult.ModelPath == "" || result.ExecutionResult.ModelBytes <= 0 || result.ExecutionResult.Timings.TotalMS < 0 {
		t.Fatalf("missing distributed rpc model/timing trace: %#v", result.ExecutionResult)
	}
	argsBytes, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(argsBytes)), "\n")
	if !containsAdjacentArgs(args, "--rpc", "10.0.0.10:50052,10.0.0.11:50052") {
		t.Fatalf("expected --rpc argument, got %#v", args)
	}
}

func TestLlamaRuntimeEnvPrependsSiblingLibDir(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("library path helper is only active on unix-like desktop/server targets")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	libDir := filepath.Join(dir, "lib")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	key := "LD_LIBRARY_PATH"
	if runtime.GOOS == "darwin" {
		key = "DYLD_LIBRARY_PATH"
	}
	env := llamaRuntimeEnv([]string{key + "=/existing"}, filepath.Join(binDir, llamaBinaryNameForTest()))
	got := ""
	for _, item := range env {
		if strings.HasPrefix(item, key+"=") {
			got = strings.TrimPrefix(item, key+"=")
		}
	}
	if !strings.HasPrefix(got, libDir+string(os.PathListSeparator)) {
		t.Fatalf("expected %s to be prepended to %s, got %q", libDir, key, got)
	}
}

func llamaBinaryNameForTest() string {
	if runtime.GOOS == "windows" {
		return "llama-cli.exe"
	}
	return "llama-cli"
}

func TestExecuteDistributedRPCGenerateRejectsUnsupportedProtocolVersion(t *testing.T) {
	input, err := json.Marshal(models.DistributedRPCGenerateInput{
		ModelID:      "qwen2.5-0.5b-instruct-q4-k-m",
		Prompt:       "hello",
		RPCEndpoints: []string{"10.0.0.10:50052"},
		ExecutionPlan: protocol.DistributedRPCExecutionPlan{
			Protocol:          protocol.DistributedRPCProtocol,
			ProtocolVersion:   protocol.DistributedRPCProtocolVersion + 1,
			PlanSchemaVersion: protocol.DistributedRPCPlanSchemaVersion,
			Mode:              "llama.cpp-rpc",
			ModelID:           "qwen2.5-0.5b-instruct-q4-k-m",
			CoordinatorNodeID: "node-a",
			RPCEndpoints:      []string{"10.0.0.10:50052"},
			Backends: []protocol.DistributedRPCBackend{{
				NodeID:   "node-b",
				Endpoint: "10.0.0.10:50052",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executeWorkerJob(jobs.Job{
		Type:  models.JobGenerateDistributedRPC,
		Input: string(input),
	}, cluster.ResourceSnapshot{}, t.TempDir(), "node-a", time.Now().UTC(), "")
	if err == nil || !strings.Contains(err.Error(), "unsupported distributed rpc protocol_version") {
		t.Fatalf("expected unsupported protocol version error, got %v", err)
	}
}

func TestExecuteDistributedStageJobReturnsReadyResult(t *testing.T) {
	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID: "job-parent",
		ModelID:     "qwen2.5-7b-instruct-q4-k-m",
		Stage: models.DistributedStageInput{
			Index:      0,
			NodeID:     "node-a",
			LayerStart: 0,
			LayerEnd:   13,
			Layers:     14,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 13},
			Runtime:         string(models.RuntimeLlamaCPP),
			SourceArtifact:  "https://example.test/model.gguf",
			TargetArtifact:  "qwen.stage-0.layers-0-13",
			Materialization: cdip.ShardLogicalLayers,
		},
		DownstreamNodeID: "node-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		Type:  models.JobGenerateStage,
		Input: string(input),
	}, cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-7b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}, t.TempDir(), "node-a", time.Now().UTC(), "")
	if err != nil {
		t.Fatal(err)
	}
	var result distributedStageResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_ready" || result.StageIndex != 0 || result.ActivationProtocol != "activation-stream-v1" {
		t.Fatalf("unexpected distributed stage result: %#v", result)
	}
}

func TestExecuteDistributedStagePrepareUsesStageRunnerWhenConfigured(t *testing.T) {
	workDir := t.TempDir()
	marker := filepath.Join(workDir, "runner-called")
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
echo ran > "$CMESH_TEST_STAGE_RUNNER_MARKER"
command=""
output_file=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --command)
      command="$2"
      shift 2
      ;;
    --output-file)
      output_file="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
if [ "$command" = "write-shard-bundle" ]; then
  printf 'CMESH_SHARD_BUNDLE_V1\npayload' > "$output_file"
  cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_shard_bundle",
  "status": "bundle_ready_not_loadable_gguf",
  "runtime": "llama.cpp",
  "protocol": "cdip.cmesh-shard-bundle-v1",
  "output_file": "$output_file",
  "stage_index": 0,
  "stage_start": 0,
  "stage_end": 13,
  "selected_tensor_count": 3,
  "selected_bytes": 96,
  "bundle_bytes": 29,
  "loadable_gguf": false
}
JSON
  exit 0
fi
cat <<'JSON'
{
  "kind": "cmesh.llamacpp_stage_prepare",
  "status": "metadata_ready",
  "runtime": "llama.cpp",
  "model_path": "/models/qwen.gguf",
  "model_name": "Qwen2.5 0.5B",
  "stage_index": 0,
  "stage_start": 0,
  "stage_end": 13,
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
	t.Setenv("CMESH_TEST_STAGE_RUNNER_MARKER", marker)
	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID:    "job-parent",
		ModelID:        "qwen2.5-7b-instruct-q4-k-m",
		StageRunnerBin: runner,
		ModelPath:      "/models/qwen.gguf",
		Stage: models.DistributedStageInput{
			Index:      0,
			NodeID:     "node-a",
			LayerStart: 0,
			LayerEnd:   13,
			Layers:     14,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 13},
			Runtime:         string(models.RuntimeLlamaCPP),
			SourceArtifact:  "https://example.test/model.gguf",
			TargetArtifact:  "qwen.stage-0.layers-0-13",
			Materialization: cdip.ShardLogicalLayers,
		},
		DownstreamNodeID: "node-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-0",
		Type:  models.JobGenerateStage,
		Input: string(input),
	}, cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-7b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Path:    "/models/qwen.gguf",
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}, workDir, "node-a", time.Now().UTC(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("expected stage runner prepare to execute: %v", err)
	}
	var result distributedStageResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_ready" || result.Runtime != "llama.cpp" || result.StageIndex != 0 {
		t.Fatalf("unexpected distributed stage prepare result: %#v", result)
	}
}

func TestExecuteDistributedStageJobRequiresRuntimeAndModel(t *testing.T) {
	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID: "job-parent",
		ModelID:     "qwen2.5-7b-instruct-q4-k-m",
		Stage:       models.DistributedStageInput{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 13},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 13},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executeWorkerJob(jobs.Job{Type: models.JobGenerateStage, Input: string(input)}, cluster.ResourceSnapshot{}, t.TempDir(), "node-a", time.Now().UTC(), "")
	if err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Fatalf("expected runtime readiness error, got %v", err)
	}
	_, err = executeWorkerJob(jobs.Job{Type: models.JobGenerateStage, Input: string(input)}, cluster.ResourceSnapshot{
		Runtimes: []cluster.RuntimeResource{{Name: string(models.RuntimeLlamaCPP), Ready: true}},
	}, t.TempDir(), "node-a", time.Now().UTC(), "")
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("expected model readiness error, got %v", err)
	}
}

func TestStageRunnerPrepareLogicalMode(t *testing.T) {
	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID: "job-parent",
		ModelID:     "qwen2.5-7b-instruct-q4-k-m",
		Stage:       models.DistributedStageInput{Index: 1, NodeID: "node-b", LayerStart: 14, LayerEnd: 27},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 1, NodeID: "node-b", LayerStart: 14, LayerEnd: 27},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
			SourceArtifact:  "https://example.test/qwen.gguf",
			TargetArtifact:  "qwen.stage-1",
		},
		UpstreamNodeID: "node-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeStageRunnerPrepare(input, "logical", "")
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerPrepareResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Executable || result.RunnerMode != "logical" || result.Kind != "cdip.stage_ready" || result.LayerStart != 14 || result.ActivationProtocol != "activation-stream-v1" {
		t.Fatalf("unexpected stage runner result: %#v", result)
	}
}

func TestStageRunnerPrepareLlamaCPPStageExecsMetadataPrepare(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "cmesh-stage-runner")
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
  "stage_end": 13,
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
	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID: "job-parent",
		ModelID:     "qwen2.5-7b-instruct-q4-k-m",
		ModelPath:   "/models/qwen.gguf",
		Stage:       models.DistributedStageInput{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 13},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 13},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeStageRunnerPrepare(input, "llama.cpp-stage", binary)
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerPrepareResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Executable || result.Kind != "cdip.stage_ready" || result.RunnerMode != "llama.cpp-stage" || result.Runtime != "llama.cpp" {
		t.Fatalf("unexpected llama.cpp-stage prepare result: %#v", result)
	}
}

func TestWriteStagePrepareMaterializationArtifact(t *testing.T) {
	workDir := t.TempDir()
	prepared := runtimes.StagePrepareResult{
		Kind:            "cdip.stage_ready",
		ParentJobID:     "job-parent",
		StageIndex:      2,
		ModelID:         "qwen2.5-7b-instruct-q4-k-m",
		Runtime:         string(models.RuntimeLlamaCPP),
		LayerStart:      14,
		LayerEnd:        27,
		Materialization: string(cdip.ShardLogicalLayers),
		Artifact: cdip.ShardArtifact{
			Protocol:   "cdip.shard-artifact-v1",
			Status:     "planned",
			LayerStart: 14,
			LayerEnd:   27,
		},
		MaterializationPlan: &runtimes.StageMaterializationPlan{
			Protocol:            runtimes.StageMaterializationPlanV1,
			Runtime:             string(models.RuntimeLlamaCPP),
			Source:              "gguf metadata",
			ModelID:             "qwen2.5-7b-instruct-q4-k-m",
			ModelPath:           "/models/qwen.gguf",
			StageIndex:          2,
			LayerStart:          14,
			LayerEnd:            27,
			ManifestOnly:        true,
			TotalTensorCount:    4,
			SelectedTensorCount: 2,
			StageTensorCount:    1,
			BoundaryTensorCount: 1,
			SelectedBytes:       96,
			Tensors: []runtimes.StageTensorRef{
				{Name: "blk.14.attn_q.weight", Type: "Q4_K", Bytes: 64},
				{Name: "output_norm.weight", Type: "F32", Bytes: 32, Boundary: true},
			},
		},
	}
	updated, err := writeStagePrepareMaterializationArtifact(prepared, workDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Artifact.URI == "" || !strings.HasPrefix(updated.Artifact.URI, "file://") {
		t.Fatalf("expected file artifact URI, got %#v", updated.Artifact)
	}
	if updated.PhysicalShardPlan == nil {
		t.Fatalf("expected physical shard plan")
	}
	if updated.PhysicalShardPlan.Protocol != runtimes.PhysicalShardPlanV1 || updated.PhysicalShardPlan.PhysicalArtifactReady || updated.PhysicalShardPlan.Status != "blocked_missing_physical_shard_writer" {
		t.Fatalf("unexpected physical shard plan: %#v", updated.PhysicalShardPlan)
	}
	if updated.PhysicalShardPlan.SelectedTensorManifestURI != updated.Artifact.URI || updated.PhysicalShardPlan.SelectedTensorManifestChecksum != updated.Artifact.Checksum {
		t.Fatalf("physical shard plan should reference selected tensor manifest artifact: %#v", updated.PhysicalShardPlan)
	}
	if updated.Artifact.Status != "selected_tensor_manifest_ready" || updated.Artifact.ExpectedBytes != 96 || updated.Artifact.PhysicalArtifactReady {
		t.Fatalf("unexpected artifact metadata: %#v", updated.Artifact)
	}
	artifactPath := filepath.Join(workDir, "stage-2-materialization-plan.json")
	body, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if updated.Artifact.Checksum != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("artifact checksum mismatch: got %s", updated.Artifact.Checksum)
	}
	var plan runtimes.StageMaterializationPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.StageIndex != 2 || plan.SelectedTensorCount != 2 || plan.SelectedBytes != 96 {
		t.Fatalf("unexpected materialization plan: %#v", plan)
	}
	physicalPath := filepath.Join(workDir, "stage-2-physical-shard-plan.json")
	physicalBody, err := os.ReadFile(physicalPath)
	if err != nil {
		t.Fatal(err)
	}
	var physicalPlan runtimes.PhysicalShardPlan
	if err := json.Unmarshal(physicalBody, &physicalPlan); err != nil {
		t.Fatal(err)
	}
	if physicalPlan.PlanURI == "" || physicalPlan.TargetURI != "" || len(physicalPlan.Blockers) == 0 || physicalPlan.SelectedBytes != 96 {
		t.Fatalf("unexpected physical shard plan file: %#v", physicalPlan)
	}
}

func TestWriteStagePrepareMaterializationArtifactRunsShardBundleWriter(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-file) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf 'CMESH_SHARD_BUNDLE_V1\npayload' > "$out"
cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_shard_bundle",
  "status": "bundle_ready_not_loadable_gguf",
  "runtime": "llama.cpp",
  "protocol": "cdip.cmesh-shard-bundle-v1",
  "output_file": "$out",
  "stage_index": 0,
  "stage_start": 0,
  "stage_end": 1,
  "selected_tensor_count": 2,
  "selected_bytes": 64,
  "bundle_bytes": 29,
  "loadable_gguf": false
}
JSON
`), 0o755); err != nil {
		t.Fatal(err)
	}
	prepared := runtimes.StagePrepareResult{
		Kind:           "cdip.stage_ready",
		ParentJobID:    "job-parent",
		StageIndex:     0,
		ModelID:        "qwen2.5-7b-instruct-q4-k-m",
		Runtime:        string(models.RuntimeLlamaCPP),
		LayerStart:     0,
		LayerEnd:       1,
		TargetArtifact: "qwen.stage-0",
		MaterializationPlan: &runtimes.StageMaterializationPlan{
			Protocol:            runtimes.StageMaterializationPlanV1,
			Runtime:             string(models.RuntimeLlamaCPP),
			Source:              "gguf metadata",
			ModelID:             "qwen2.5-7b-instruct-q4-k-m",
			ModelPath:           "/models/qwen.gguf",
			StageIndex:          0,
			LayerStart:          0,
			LayerEnd:            1,
			TotalLayers:         24,
			ManifestOnly:        true,
			TotalTensorCount:    4,
			SelectedTensorCount: 2,
			StageTensorCount:    1,
			BoundaryTensorCount: 1,
			SelectedBytes:       64,
			Tensors: []runtimes.StageTensorRef{
				{Name: "token_embd.weight", Type: "Q4_K", Bytes: 32, Boundary: true},
				{Name: "blk.0.attn_q.weight", Type: "Q4_K", Bytes: 32},
			},
		},
	}
	updated, err := writeStagePrepareMaterializationArtifact(prepared, workDir, runner)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PhysicalShardPlan == nil || !updated.PhysicalShardPlan.PhysicalArtifactReady || updated.PhysicalShardPlan.ArtifactKind != "cmesh_shard_bundle" || updated.PhysicalShardPlan.LoadableGGUF {
		t.Fatalf("expected ready non-loadable CMesh shard bundle plan, got %#v", updated.PhysicalShardPlan)
	}
	if updated.PhysicalShardPlan.PlanURI == "" || updated.PhysicalShardPlan.TargetURI == "" || updated.PhysicalShardPlan.TargetChecksum == "" || updated.PhysicalShardPlan.ArtifactBytes == 0 {
		t.Fatalf("expected bundle URI/checksum/bytes, got %#v", updated.PhysicalShardPlan)
	}
}

func TestStageRunnerPrefillCommand(t *testing.T) {
	resultBody, err := executeStageRunnerCommand(stageRunnerCommandOptions{
		Action:      "prefill",
		Mode:        "logical",
		ParentJobID: "job-parent",
		StageJobID:  "job-stage-0",
		StageIndex:  0,
		Step:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerCommandResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Executable || result.Kind != "cdip.stage_prefill" || result.Step != 1 {
		t.Fatalf("unexpected prefill command result: %#v", result)
	}
}

func TestStageRunnerDecodeCommandSendsHTTPActivation(t *testing.T) {
	sent := make(chan transport.ActivationFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var frame transport.ActivationFrame
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			t.Fatal(err)
		}
		sent <- frame
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	resultBody, err := executeStageRunnerCommand(stageRunnerCommandOptions{
		Action:            "decode",
		Mode:              "logical",
		ParentJobID:       "job-parent",
		StageJobID:        "job-stage-0",
		StageIndex:        0,
		Step:              7,
		UpstreamStageID:   "job-stage-0",
		DownstreamStageID: "job-stage-1",
		DownstreamNodeID:  "node-b",
		Payload:           "activation",
		DType:             "f16",
		Shape:             "1,1,5",
		Checksum:          "mock:7",
		ManagerURL:        server.URL,
		NodeID:            "node-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerCommandResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_decode" || result.ActivationFrame == nil || result.ActivationBytes != len("activation") || result.ActivationRelay != "http" {
		t.Fatalf("unexpected decode command result: %#v", result)
	}
	if result.TensorEnvelope == nil || result.TensorEnvelope.Protocol != runtimes.TensorEnvelopeV1 || result.TensorEnvelope.DType != "f16" || result.TensorEnvelope.ByteCount != len("activation") || result.TensorEnvelope.DownstreamStageJobID != "job-stage-1" {
		t.Fatalf("unexpected tensor envelope: %#v", result.TensorEnvelope)
	}
	frame := <-sent
	if frame.Header.Type != cdip.MessageActivationChunk || frame.Header.Sequence != 7 || frame.Header.Checksum != "mock:7" || string(frame.Payload) != "activation" {
		t.Fatalf("unexpected activation frame: %#v", frame)
	}
}

func TestStageRunnerReceiveCommandValidatesTensorEnvelope(t *testing.T) {
	payload := []byte("activation")
	sum := sha256.Sum256(payload)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		frame := transport.ActivationFrame{
			Header: cdip.ActivationChunk{
				Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
				ParentJobID:  "job-parent",
				StageJobID:   "job-stage-0",
				Sequence:     9,
				ContentType:  "application/vnd.cmesh.activation+binary",
				Encoding:     "raw",
				Shape:        []int{1, 1, 5},
				DType:        "f16",
				PayloadBytes: uint64(len(payload)),
				Checksum:     checksum,
			},
			Payload: payload,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(frame); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	resultBody, err := executeStageRunnerReceive(stageRunnerReceiveOptions{
		ParentJobID:       "job-parent",
		StageJobID:        "job-stage-0",
		StageIndex:        1,
		UpstreamStageID:   "job-stage-0",
		DownstreamStageID: "job-stage-1",
		ManagerURL:        server.URL,
		NodeID:            "node-b",
		TimeoutMS:         100,
		ExpectedDType:     "f16",
		ExpectedShape:     "1,1,5",
		ExpectedChecksum:  checksum,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerReceiveResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_receive" || result.ActivationBytes != len(payload) || result.TensorEnvelope == nil {
		t.Fatalf("unexpected receive result: %#v", result)
	}
	if result.TensorEnvelope.Checksum != checksum || result.TensorEnvelope.StageIndex != 1 || result.TensorEnvelope.DownstreamStageJobID != "job-stage-1" {
		t.Fatalf("unexpected receive envelope: %#v", result.TensorEnvelope)
	}
}

func TestStageRunnerRelayDecodeReceivesRunsAndSendsActivation(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "fake-stage-runner")
	runnerScript := `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-file) out="$2"; shift 2 ;;
    *) shift ;;
  esac
done
printf "abcd" > "$out"
cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_decode",
  "status": "executed",
  "runtime": "llama.cpp",
  "stage_index": 1,
  "stage_start": 1,
  "stage_end": 1,
  "input_tensor": {"dtype": "f32", "shape": [1, 1, 1], "bytes": 4},
  "output_tensor": {"dtype": "f32", "shape": [1, 1, 1], "bytes": 4, "path": "$out"},
  "decode_status": 0
}
JSON
`
	if err := os.WriteFile(runner, []byte(runnerScript), 0o755); err != nil {
		t.Fatal(err)
	}

	inputPayload := []byte{0, 0, 0, 0}
	inputSum := sha256.Sum256(inputPayload)
	inputChecksum := "sha256:" + hex.EncodeToString(inputSum[:])
	sent := make(chan transport.ActivationFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			frame := transport.ActivationFrame{
				Header: cdip.ActivationChunk{
					Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
					ParentJobID:  "job-parent",
					StageJobID:   "job-stage-0",
					Sequence:     11,
					ContentType:  "application/vnd.cmesh.activation+binary",
					Encoding:     "raw",
					Shape:        []int{1, 1, 1},
					DType:        "f32",
					PayloadBytes: uint64(len(inputPayload)),
					Checksum:     inputChecksum,
				},
				Payload: inputPayload,
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(frame); err != nil {
				t.Fatal(err)
			}
		case http.MethodPost:
			var frame transport.ActivationFrame
			if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
				t.Fatal(err)
			}
			sent <- frame
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	resultBody, err := executeStageRunnerRelayDecode(stageRunnerRelayDecodeOptions{
		ParentJobID:       "job-parent",
		UpstreamStageID:   "job-stage-0",
		StageJobID:        "job-stage-1",
		StageIndex:        1,
		DownstreamStageID: "job-stage-2",
		DownstreamNodeID:  "node-c",
		ManagerURL:        server.URL,
		NodeID:            "node-b",
		TimeoutMS:         100,
		RunnerBin:         runner,
		ModelPath:         "/models/qwen.gguf",
		StageStart:        1,
		StageEnd:          1,
		WorkDir:           workDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerRelayDecodeResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_relay_decode" || result.InputBytes != 4 || result.OutputBytes != 4 || result.OutputEnvelope == nil || result.RunnerReport == nil {
		t.Fatalf("unexpected relay decode result: %#v", result)
	}
	frame := <-sent
	if frame.Header.ParentJobID != "job-parent" || frame.Header.StageJobID != "job-stage-1" || frame.Header.DType != "f32" || string(frame.Payload) != "abcd" {
		t.Fatalf("unexpected downstream activation frame: %#v", frame)
	}
	if !strings.HasPrefix(frame.Header.Checksum, "sha256:") {
		t.Fatalf("expected sha256 checksum, got %#v", frame.Header)
	}
}

func TestExecuteDistributedStageRelayDecodeJobRunsStageRunner(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "fake-stage-runner")
	runnerScript := `#!/bin/sh
set -eu
out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-file)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
printf "efgh" > "$out"
cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_decode",
  "status": "executed",
  "runtime": "llama.cpp",
  "model_path": "model.gguf",
  "stage_index": 1,
  "stage_start": 1,
  "stage_end": 1,
  "input_tensor": {"dtype":"f32","shape":[1,1,1],"bytes":4},
  "output_tensor": {"dtype":"f32","shape":[1,1,1],"bytes":4}
}
JSON
`
	if err := os.WriteFile(runner, []byte(runnerScript), 0o755); err != nil {
		t.Fatal(err)
	}

	payload := []byte{0, 0, 0, 0}
	sum := sha256.Sum256(payload)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	sent := make(chan transport.ActivationFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			frame := transport.ActivationFrame{
				Header: cdip.ActivationChunk{
					Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
					ParentJobID:  "job-parent",
					StageJobID:   "job-stage-0",
					Sequence:     5,
					ContentType:  "application/vnd.cmesh.activation+binary",
					Encoding:     "raw",
					Shape:        []int{1, 1, 1},
					DType:        "f32",
					PayloadBytes: uint64(len(payload)),
					Checksum:     checksum,
				},
				Payload: payload,
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(frame); err != nil {
				t.Fatal(err)
			}
		case http.MethodPost:
			var frame transport.ActivationFrame
			if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
				t.Fatal(err)
			}
			sent <- frame
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()

	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID:       "job-parent",
		StageJobID:        "job-stage-1",
		StageCommand:      "relay_decode",
		ModelID:           "qwen2.5-7b-instruct-q4-k-m",
		UpstreamStageID:   "job-stage-0",
		DownstreamStageID: "job-stage-2",
		DownstreamNodeID:  "node-c",
		StageRunnerBin:    runner,
		ModelPath:         filepath.Join(workDir, "model.gguf"),
		WorkDir:           filepath.Join(workDir, "stage-work"),
		TimeoutMS:         100,
		Stage: models.DistributedStageInput{
			Index:      1,
			NodeID:     "node-b",
			LayerStart: 1,
			LayerEnd:   1,
			Layers:     1,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 1, NodeID: "node-b", LayerStart: 1, LayerEnd: 1},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-1",
		Type:  models.JobGenerateStage,
		Input: string(input),
	}, cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-7b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Path:    filepath.Join(workDir, "model.gguf"),
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}, workDir, "node-b", time.Now().UTC(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerRelayDecodeResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_relay_decode" || result.StageJobID != "job-stage-1" || result.InputBytes != 4 || result.OutputBytes != 4 {
		t.Fatalf("unexpected relay decode result: %#v", result)
	}
	frame := <-sent
	if frame.Header.StageJobID != "job-stage-1" || string(frame.Payload) != "efgh" || frame.Header.DType != "f32" {
		t.Fatalf("unexpected downstream frame: %#v", frame)
	}
}

func TestExecuteDistributedStageTerminalDecodeJobRunsStageRunner(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "fake-stage-runner")
	runnerScript := `#!/bin/sh
set -eu
cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_terminal_decode",
  "status": "executed",
  "runtime": "llama.cpp",
  "model_path": "model.gguf",
  "stage_index": 2,
  "stage_start": 2,
  "stage_end": 2,
  "input_tensor": {"dtype":"f32","shape":[1,1,1],"bytes":4},
  "logits": {"dtype":"f32","shape":[1,32000],"bytes":128000},
  "next_token_id": 42,
  "next_token_text": " hello",
  "tokens": [42, 43],
  "output": " hello there",
  "final": true,
  "decode_status": 0
}
JSON
`
	if err := os.WriteFile(runner, []byte(runnerScript), 0o755); err != nil {
		t.Fatal(err)
	}

	payload := []byte{0, 0, 0, 0}
	sum := sha256.Sum256(payload)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		frame := transport.ActivationFrame{
			Header: cdip.ActivationChunk{
				Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
				ParentJobID:  "job-parent",
				StageJobID:   "job-stage-1",
				Sequence:     5,
				ContentType:  "application/vnd.cmesh.activation+binary",
				Encoding:     "raw",
				Shape:        []int{1, 1, 1},
				DType:        "f32",
				PayloadBytes: uint64(len(payload)),
				Checksum:     checksum,
			},
			Payload: payload,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(frame); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID:     "job-parent",
		StageJobID:      "job-stage-2",
		StageCommand:    "terminal_decode",
		ModelID:         "qwen2.5-7b-instruct-q4-k-m",
		UpstreamStageID: "job-stage-1",
		StageRunnerBin:  runner,
		ModelPath:       filepath.Join(workDir, "model.gguf"),
		WorkDir:         filepath.Join(workDir, "stage-work"),
		TimeoutMS:       100,
		Stage: models.DistributedStageInput{
			Index:      2,
			NodeID:     "node-c",
			LayerStart: 2,
			LayerEnd:   2,
			Layers:     1,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 2, NodeID: "node-c", LayerStart: 2, LayerEnd: 2},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-2",
		Type:  models.JobGenerateStage,
		Input: string(input),
	}, cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-7b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Path:    filepath.Join(workDir, "model.gguf"),
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}, workDir, "node-c", time.Now().UTC(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerTerminalDecodeResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_terminal_decode" || result.NextTokenID != 42 || result.NextTokenText != " hello" || result.Output != " hello there" || len(result.Tokens) != 2 || !result.Final || result.InputBytes != 4 || result.RunnerReport == nil {
		t.Fatalf("unexpected terminal decode result: %#v", result)
	}
}

func TestExecuteDistributedStageTerminalDecodeJobUsesPreviousOutputWithStageDaemon(t *testing.T) {
	workDir := t.TempDir()
	payload := []byte{0, 0, 0, 0}
	sum := sha256.Sum256(payload)
	checksum := "sha256:" + hex.EncodeToString(sum[:])
	managerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		frame := transport.ActivationFrame{
			Header: cdip.ActivationChunk{
				Envelope:     cdip.NewEnvelope(cdip.MessageActivationChunk),
				ParentJobID:  "job-parent",
				StageJobID:   "job-stage-1",
				Sequence:     3,
				ContentType:  "application/vnd.cmesh.activation+binary",
				Encoding:     "raw",
				Shape:        []int{1, 1, 1},
				DType:        "f32",
				PayloadBytes: uint64(len(payload)),
				Checksum:     checksum,
			},
			Payload: payload,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(frame); err != nil {
			t.Fatal(err)
		}
	}))
	defer managerServer.Close()

	daemonServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var req stageDaemonDecodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if req.PreviousTokenText != "C" {
			t.Fatalf("expected previous output feedback, got %#v", req)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind":                    "cmesh.stage_daemon_decode",
			"session_id":              "stage-2-test",
			"ready":                   true,
			"persistent_model":        true,
			"persistent_kv_in_memory": true,
			"next_token_id":           14194,
			"next_token_text":         "Mesh",
			"final":                   true,
		})
	}))
	defer daemonServer.Close()

	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID:       "job-parent",
		StageJobID:        "job-stage-2",
		StageCommand:      "terminal_decode",
		ModelID:           "qwen2.5-3b-instruct-q4-k-m",
		UpstreamStageID:   "job-stage-1",
		StageDaemonURL:    daemonServer.URL,
		StageSessionID:    "stage-2-test",
		PreviousTokenText: " C",
		ModelPath:         filepath.Join(workDir, "stage-2.gguf"),
		WorkDir:           filepath.Join(workDir, "stage-work"),
		Stage: models.DistributedStageInput{
			Index:      2,
			NodeID:     "node-c",
			LayerStart: 24,
			LayerEnd:   35,
			Layers:     12,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 2, NodeID: "node-c", LayerStart: 24, LayerEnd: 35},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-2",
		Type:  models.JobGenerateStage,
		Input: string(input),
	}, cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-3b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Path:    filepath.Join(workDir, "stage-2.gguf"),
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}, workDir, "node-c", time.Now().UTC(), managerServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerTerminalDecodeResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_terminal_decode" || result.NextTokenText != "Mesh" || result.Output != " CMesh" {
		t.Fatalf("expected cumulative terminal output, got %#v", result)
	}
}

func TestStageRunnerJSONReportIgnoresRuntimeLogs(t *testing.T) {
	noisy := []byte(`ggml_metal_device_init: GPU name: MTL0
llama_model_loader: tokenizer.chat_template str = {%- if tools %}
{
  "kind": "cmesh.llamacpp_stage_source_decode",
  "status": "executed"
}`)
	var report llamaCPPStageDecodeReport
	if err := json.Unmarshal(stageRunnerJSONReport(noisy, "cmesh.llamacpp_stage_source_decode"), &report); err != nil {
		t.Fatalf("expected noisy stage runner output to parse: %v", err)
	}
	if report.Kind != "cmesh.llamacpp_stage_source_decode" || report.Status != "executed" {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestExecuteDistributedStageSourceDecodeJobSendsActivation(t *testing.T) {
	sent := make(chan transport.ActivationFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var frame transport.ActivationFrame
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			t.Fatal(err)
		}
		sent <- frame
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID:       "job-parent",
		StageJobID:        "job-stage-0",
		StageCommand:      "source_decode",
		ModelID:           "qwen2.5-7b-instruct-q4-k-m",
		Prompt:            "hello distributed source",
		DownstreamStageID: "job-stage-1",
		DownstreamNodeID:  "node-b",
		Stage: models.DistributedStageInput{
			Index:      0,
			NodeID:     "node-a",
			LayerStart: 0,
			LayerEnd:   1,
			Layers:     2,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 1},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-0",
		Type:  models.JobGenerateStage,
		Input: string(input),
	}, cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-7b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Path:    filepath.Join(t.TempDir(), "model.gguf"),
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}, t.TempDir(), "node-a", time.Now().UTC(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerCommandResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_source_decode" || result.ActivationBytes != 4 || result.TensorEnvelope == nil {
		t.Fatalf("unexpected source decode result: %#v", result)
	}
	frame := <-sent
	if frame.Header.ParentJobID != "job-parent" || frame.Header.StageJobID != "job-stage-0" || frame.Header.DType != "f32" || len(frame.Payload) != 4 {
		t.Fatalf("unexpected source activation frame: %#v", frame)
	}
	if !strings.HasPrefix(frame.Header.Checksum, "sha256:") {
		t.Fatalf("expected sha256 checksum, got %#v", frame.Header)
	}
}

func TestExecuteDistributedStageJobUsesStageDaemonSession(t *testing.T) {
	sent := make(chan transport.ActivationFrame, 1)
	managerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var frame transport.ActivationFrame
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			t.Fatal(err)
		}
		sent <- frame
		w.WriteHeader(http.StatusAccepted)
	}))
	defer managerServer.Close()

	daemonServer := httptest.NewServer(newStageRunnerDaemonHandler(t.TempDir()))
	defer daemonServer.Close()

	stageInput := models.DistributedStageJobInput{
		ParentJobID:       "job-parent",
		StageJobID:        "job-stage-0",
		ModelID:           "qwen2.5-7b-instruct-q4-k-m",
		StageDaemonURL:    daemonServer.URL,
		StageSessionID:    "stage-0-resident-test",
		KVCacheKey:        "conversation-1:kv",
		DownstreamStageID: "job-stage-1",
		DownstreamNodeID:  "node-b",
		Prompt:            "hello resident stage",
		Stage: models.DistributedStageInput{
			Index:      0,
			NodeID:     "node-a",
			LayerStart: 0,
			LayerEnd:   1,
			Layers:     2,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 1},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	}
	prepareBody, err := json.Marshal(stageInput)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-7b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Path:    filepath.Join(t.TempDir(), "model.gguf"),
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}
	prepareResult, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-0",
		Type:  models.JobGenerateStage,
		Input: string(prepareBody),
	}, snapshot, t.TempDir(), "node-a", time.Now().UTC(), managerServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	var prepared distributedStageResult
	if err := json.Unmarshal([]byte(prepareResult), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.StageSession == nil || prepared.StageSession.SessionID != "stage-0-resident-test" || !prepared.StageSession.PersistentKVInMemory {
		t.Fatalf("expected resident daemon session in prepare result, got %#v", prepared.StageSession)
	}
	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/sessions/stage-0-resident-test", nil)
	deleteRec := httptest.NewRecorder()
	daemonServer.Config.Handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected daemon session delete before decode, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	stageInput.StageCommand = "source_decode"
	stageInput.Step = 1
	decodeBody, err := json.Marshal(stageInput)
	if err != nil {
		t.Fatal(err)
	}
	decodeResult, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-0",
		Type:  models.JobGenerateStage,
		Input: string(decodeBody),
	}, snapshot, t.TempDir(), "node-a", time.Now().UTC(), managerServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	var decoded stageRunnerCommandResult
	if err := json.Unmarshal([]byte(decodeResult), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.StageDaemonDecode == nil || decoded.StageDaemonDecode["session_id"] != "stage-0-resident-test" || decoded.StageDaemonDecode["decode_steps"] != float64(1) {
		t.Fatalf("expected stage daemon decode trace, got %#v", decoded.StageDaemonDecode)
	}
	frame := <-sent
	if frame.Header.StageJobID != "job-stage-0" || len(frame.Payload) == 0 {
		t.Fatalf("expected activation frame from source stage, got %#v", frame)
	}
}

func TestExecuteDistributedStageSourceDecodeJobRunsStageRunner(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "fake-stage-runner")
	runnerScript := `#!/bin/sh
set -eu
out=""
token_id=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-file)
      out="$2"
      shift 2
      ;;
    --token-id)
      token_id="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
[ "$token_id" = "42" ] || { echo "missing token feedback: $token_id" >&2; exit 64; }
printf "ijkl" > "$out"
cat <<JSON
{
  "kind": "cmesh.llamacpp_stage_source_decode",
  "status": "executed",
  "runtime": "llama.cpp",
  "model_path": "model.gguf",
  "stage_index": 0,
  "stage_start": 0,
  "stage_end": 1,
  "token_count": 1,
  "output_tensor": {"dtype":"f32","shape":[1,1,1],"bytes":4}
}
JSON
`
	if err := os.WriteFile(runner, []byte(runnerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMESH_STAGE_RUNNER_BIN", runner)
	modelPath := filepath.Join(workDir, "model.gguf")
	sent := make(chan transport.ActivationFrame, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		var frame transport.ActivationFrame
		if err := json.NewDecoder(r.Body).Decode(&frame); err != nil {
			t.Fatal(err)
		}
		sent <- frame
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	tokenID := 42
	input, err := json.Marshal(models.DistributedStageJobInput{
		ParentJobID:       "job-parent",
		StageJobID:        "job-stage-0",
		StageCommand:      "source_decode",
		ModelID:           "qwen2.5-7b-instruct-q4-k-m",
		Prompt:            "hello source runner",
		PreviousTokenID:   &tokenID,
		PreviousTokenText: " hello",
		DownstreamStageID: "job-stage-1",
		DownstreamNodeID:  "node-b",
		WorkDir:           filepath.Join(workDir, "stage-work"),
		Stage: models.DistributedStageInput{
			Index:      0,
			NodeID:     "node-a",
			LayerStart: 0,
			LayerEnd:   1,
			Layers:     2,
		},
		Shard: cdip.ModelShard{
			Stage:           cdip.Stage{Index: 0, NodeID: "node-a", LayerStart: 0, LayerEnd: 1},
			Runtime:         string(models.RuntimeLlamaCPP),
			Materialization: cdip.ShardLogicalLayers,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeWorkerJob(jobs.Job{
		ID:    "job-stage-0",
		Type:  models.JobGenerateStage,
		Input: string(input),
	}, cluster.ResourceSnapshot{
		Models: []cluster.ModelResource{{
			ID:      "qwen2.5-7b-instruct-q4-k-m",
			Runtime: string(models.RuntimeLlamaCPP),
			Path:    modelPath,
			Ready:   true,
		}},
		Runtimes: []cluster.RuntimeResource{{
			Name:  string(models.RuntimeLlamaCPP),
			Ready: true,
		}},
	}, workDir, "node-a", time.Now().UTC(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	var result stageRunnerSourceDecodeResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "cdip.stage_source_decode" || result.RunnerMode != "llama.cpp-stage" || result.OutputBytes != 4 || result.OutputEnvelope == nil || result.RunnerReport == nil {
		t.Fatalf("unexpected source decode runner result: %#v", result)
	}
	if result.PreviousTokenID == nil || *result.PreviousTokenID != 42 || result.PreviousTokenText != "hello" {
		t.Fatalf("expected source decode result to record token feedback, got %#v", result)
	}
	frame := <-sent
	if frame.Header.StageJobID != "job-stage-0" || string(frame.Payload) != "ijkl" || frame.Header.DType != "f32" {
		t.Fatalf("unexpected source activation frame: %#v", frame)
	}
}

func TestStageRunnerLlamaCPPStageCommandReportsMissingHooks(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "llama-cli")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := executeStageRunnerCommand(stageRunnerCommandOptions{
		Action:      "decode",
		Mode:        "llama.cpp-stage",
		LlamaCLI:    binary,
		ParentJobID: "job-parent",
		StageJobID:  "job-stage-0",
		StageIndex:  0,
		Step:        1,
	})
	if err == nil || !strings.Contains(err.Error(), "not executable yet") {
		t.Fatalf("expected llama.cpp stage command blocker, got %v", err)
	}
}

func TestExecuteModelDeleteJobRemovesModelDirectory(t *testing.T) {
	cacheDir := t.TempDir()
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	path := modelPath(cacheDir, model)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(filepath.Dir(path), "download.tmp")
	if err := os.WriteFile(sidecar, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, err := json.Marshal(models.DeleteInput{ModelID: model.ID})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeModelDeleteJob(string(input), cacheDir)
	if err != nil {
		t.Fatal(err)
	}

	var result modelDeleteResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Removed {
		t.Fatalf("expected removed=true, got %#v", result)
	}
	if result.FreedBytes != int64(len("model")+len("partial")) {
		t.Fatalf("expected freed bytes to include model directory contents, got %#v", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected model file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("expected sidecar file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("expected model directory to be removed, stat err=%v", err)
	}
}

func TestExecuteModelInstallJobWritesManifestForExistingModel(t *testing.T) {
	cacheDir := t.TempDir()
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	path := modelPath(cacheDir, model)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, err := json.Marshal(models.InstallInput{ModelID: model.ID})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeModelInstallJob(string(input), cacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result modelInstallResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.ModelID != model.ID || result.Bytes != int64(len("model")) {
		t.Fatalf("unexpected install result: %#v", result)
	}
	manifestPath := resources.ModelManifestPath(cacheDir, model.ID)
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("expected manifest to be written, stat err=%v", err)
	}
	installed := resources.DiscoverInstalledModels(cacheDir)
	if len(installed) != 1 || installed[0].Runtime != string(model.Runtime) || installed[0].Family != model.Family {
		t.Fatalf("expected installed model inventory with manifest metadata, got %#v", installed)
	}
}

func TestExecuteModelInstallJobProbesLayerCountWithStageRunner(t *testing.T) {
	cacheDir := t.TempDir()
	runner := filepath.Join(t.TempDir(), "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
cat <<'JSON'
{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","n_layer":28}
JSON
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMESH_STAGE_RUNNER_BIN", runner)
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	path := modelPath(cacheDir, model)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, err := json.Marshal(models.InstallInput{ModelID: model.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executeModelInstallJob(string(input), cacheDir, nil); err != nil {
		t.Fatal(err)
	}
	installed := resources.DiscoverInstalledModels(cacheDir)
	if len(installed) != 1 || installed[0].Layers != 28 {
		t.Fatalf("expected stage-runner probed layer count, got %#v", installed)
	}
}

func TestExecuteModelRepairJobRepairsManifestAndCleansPartialDownload(t *testing.T) {
	cacheDir := t.TempDir()
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	path := modelPath(cacheDir, model)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("model"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	input, err := json.Marshal(models.RepairInput{ModelID: model.ID})
	if err != nil {
		t.Fatal(err)
	}
	resultBody, err := executeModelRepairJob(string(input), cacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var result modelRepairResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.ModelID != model.ID || !result.ManifestRepaired || !result.TempCleaned || result.Reinstalled {
		t.Fatalf("unexpected repair result: %#v", result)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatalf("expected partial download to be removed, stat err=%v", err)
	}
	installed := resources.DiscoverInstalledModels(cacheDir)
	if len(installed) != 1 || !installed[0].Ready || installed[0].Error != "" {
		t.Fatalf("expected clean repaired inventory, got %#v", installed)
	}
}

func TestExecuteModelCleanupJobRemovesPartialOrphanAndStaleManifest(t *testing.T) {
	cacheDir := t.TempDir()
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Dir(modelPath(cacheDir, model))
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := resources.ModelManifestPath(cacheDir, model.ID)
	if err := os.WriteFile(manifestPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpPath := filepath.Join(modelDir, model.File+".tmp")
	if err := os.WriteFile(tmpPath, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	orphanDir := filepath.Join(cacheDir, "models", "unknown-model")
	if err := os.MkdirAll(orphanDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, "unknown.gguf"), []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	resultBody, err := executeModelCleanupJob(`{"scope":"cache"}`, cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	var result modelCleanupResult
	if err := json.Unmarshal([]byte(resultBody), &result); err != nil {
		t.Fatal(err)
	}
	if result.PartialFilesRemoved != 1 || result.OrphanDirsRemoved != 1 || result.StaleManifestsRemoved != 1 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	if result.TotalBytesRemoved != int64(len("partial")+len("orphan")) {
		t.Fatalf("unexpected cleanup bytes: %#v", result)
	}
	for _, path := range []string{tmpPath, manifestPath, orphanDir, modelDir} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", path, err)
		}
	}
}

func TestModelInstallProgressWriterPostsToManager(t *testing.T) {
	cacheDir := t.TempDir()
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs/job-progress/progress" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	startedAt := time.Now().UTC()
	writer := modelInstallProgressWriter(server.URL, cacheDir, "node-a", jobs.Job{ID: "job-progress", Type: models.JobInstall, Input: `{"model_id":"qwen"}`}, startedAt)
	if writer == nil {
		t.Fatal("expected writer")
	}
	writer(1024, 2048)

	if received["node_id"] != "node-a" || received["progress_label"] != "Downloading model" {
		t.Fatalf("unexpected progress payload: %#v", received)
	}
	if received["progress_percent"] != float64(50) {
		t.Fatalf("unexpected progress percent: %#v", received)
	}
	status, ok := workerstatus.Read(cacheDir)
	if !ok {
		t.Fatal("expected local worker status")
	}
	if status.ProgressBytes != 1024 || status.TotalBytes != 2048 || status.ProgressPercent != 50 {
		t.Fatalf("unexpected local status: %#v", status)
	}
}

func TestCleanLlamaOutputRemovesRuntimeBanner(t *testing.T) {
	output := `
Loading model...

▄▄ ▄▄
build      : b9672-74ade5274
model      : qwen2.5-0.5b-instruct-q4_k_m.gguf
modalities : text

available commands:
  /exit or Ctrl+C     stop or exit

> Привіт

CMesh is a cluster for sharing compute across connected workers.

Exiting...
`

	got := cleanLlamaOutput(output, "Привіт")
	want := "CMesh is a cluster for sharing compute across connected workers."
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCleanLlamaOutputRemovesChatTemplateTokens(t *testing.T) {
	output := `<|im_end|>
</|im_start|>
</|im_end|>
how are you?
<|im_end|>
<|im_start|>help
<|im_end|>`

	got := cleanLlamaOutput(output, "how are you?")
	if strings.Contains(got, "<|im_") || strings.Contains(got, "</|im_") {
		t.Fatalf("expected chat template tokens to be removed, got %q", got)
	}
	if strings.Contains(got, "how are you?") {
		t.Fatalf("expected echoed prompt to be removed, got %q", got)
	}
	if !strings.Contains(got, "help") {
		t.Fatalf("expected remaining text, got %q", got)
	}
}

func TestCleanLlamaOutputRemovesFullQwenPromptEcho(t *testing.T) {
	model, err := models.MustFind("qwen2.5-1.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	prompt := modelPrompt(model, models.GenerateInput{
		Prompt:       "Що тестує CMesh?",
		SystemPrompt: "Answer in Ukrainian.",
	})
	output := prompt + "CMesh тестує локальну і розподілену генерацію Qwen."

	got := cleanLlamaOutput(output, prompt)
	want := "CMesh тестує локальну і розподілену генерацію Qwen."
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestCleanLlamaOutputRemovesDisplayedQwenPromptEcho(t *testing.T) {
	model, err := models.MustFind("qwen2.5-3b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	req := models.GenerateInput{
		Prompt: "You are running inside CMesh. Answer in one concise Ukrainian sentence: what is CMesh testing right now?",
	}
	prompt := modelPrompt(model, req)
	output := modelSystemPrompt(model) + "\n" + req.Prompt + "\nassist ... (truncated)\n\nCMesh тестує розподілений запуск Qwen.\n"

	got := cleanLlamaOutput(output, prompt)
	want := "CMesh тестує розподілений запуск Qwen."
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestSanitizeModelTextRemovesFamilyTemplateTokens(t *testing.T) {
	input := `<start_of_turn>model
Hello from Gemma.<end_of_turn>
<|im_start|>assistant
Hello from Qwen.<|im_end|>`

	got := sanitizeModelText(input)
	for _, token := range []string{"<start_of_turn>", "<end_of_turn>", "<|im_start|>", "<|im_end|>", "model\n"} {
		if strings.Contains(got, token) {
			t.Fatalf("expected %q to remove token %q", got, token)
		}
	}
	if !strings.Contains(got, "Hello from Gemma.") || !strings.Contains(got, "Hello from Qwen.") {
		t.Fatalf("expected model text to remain, got %q", got)
	}
}

func TestModelSystemPromptUsesQwenGuardrails(t *testing.T) {
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	got := modelSystemPrompt(model)
	if strings.Contains(got, "<|im_start|>") || strings.Contains(got, "<|im_end|>") {
		t.Fatalf("expected no manual chat template tokens, got %q", got)
	}
	if !strings.Contains(got, "Do not print role names") {
		t.Fatalf("expected qwen guardrail prompt, got %q", got)
	}
}

func TestModelAdapterSelectsDeepSeekBeforeQwenFamily(t *testing.T) {
	model := models.Model{
		ID:      "deepseek-r1-distill-qwen-32b-q4-k-m",
		Family:  "qwen",
		Context: 32768,
	}
	adapter := modelAdapterFor(model)
	if adapter.Name != "deepseek-qwen" {
		t.Fatalf("expected deepseek qwen adapter, got %q", adapter.Name)
	}
	prompt := adapter.SystemPrompt(model)
	if !strings.Contains(prompt, "Return only the final answer") {
		t.Fatalf("expected deepseek reasoning guardrail, got %q", prompt)
	}
}

func TestModelStopSequencesAreFamilySpecific(t *testing.T) {
	qwenModel := models.Model{ID: "qwen2.5-0.5b-instruct-q4-k-m", Family: "qwen"}
	gemmaModel := models.Model{ID: "gemma-3-12b-it-q4-k-m", Family: "gemma"}

	qwenStops := strings.Join(modelStopSequences(qwenModel), "\n")
	if !strings.Contains(qwenStops, "<|im_end|>") || strings.Contains(qwenStops, "<end_of_turn>") {
		t.Fatalf("expected qwen-only stops, got %q", qwenStops)
	}

	gemmaStops := strings.Join(modelStopSequences(gemmaModel), "\n")
	if !strings.Contains(gemmaStops, "<end_of_turn>") || strings.Contains(gemmaStops, "<|im_end|>") {
		t.Fatalf("expected gemma-only stops, got %q", gemmaStops)
	}
}

func TestModelPromptIncludesChatHistory(t *testing.T) {
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	got := modelPrompt(model, models.GenerateInput{
		SystemPrompt: "Remember user details.",
		Prompt:       "Як мене звати?",
		Messages: []models.ChatMessage{
			{Role: "user", Content: "Мене звати Сергій."},
			{Role: "assistant", Content: "Запамʼятав."},
			{Role: "user", Content: "Як мене звати?"},
		},
	})
	if !strings.Contains(got, "Мене звати Сергій.") || !strings.Contains(got, "Як мене звати?") {
		t.Fatalf("expected prompt to include chat history, got %q", got)
	}
	if !strings.HasSuffix(got, "<|im_start|>assistant\n") {
		t.Fatalf("expected qwen assistant turn suffix, got %q", got)
	}
}

func TestModelContextSizeDefaultsToFourKForLargeCatalogContext(t *testing.T) {
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMESH_MODEL_CONTEXT_SIZE", "")
	if got := modelContextSize(model); got != 4096 {
		t.Fatalf("expected default context 4096, got %d", got)
	}
}

func TestModelContextSizeKeepsSmallCatalogContext(t *testing.T) {
	model, err := models.MustFind("tinyllama-1.1b-chat-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMESH_MODEL_CONTEXT_SIZE", "")
	if got := modelContextSize(model); got != 2048 {
		t.Fatalf("expected tiny model context 2048, got %d", got)
	}
}

func TestStageRunnerDaemonSessionLifecycle(t *testing.T) {
	sessionDir := t.TempDir()
	handler := newStageRunnerDaemonHandler(sessionDir)

	createBody := strings.NewReader(`{
		"parent_job_id":"job-parent",
		"stage_job_id":"job-stage-0",
		"model_id":"qwen2.5-14b-instruct-q4-k-m",
		"stage_index":0,
		"layer_start":0,
		"layer_end":15,
		"kv_cache_key":"cdip-session-job-parent:kv"
	}`)
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", createBody)
	createReq.Host = "127.0.0.1:19781"
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created runtimes.StageSession
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Protocol != runtimes.StageSessionV1 || created.Mode != runtimes.StageSessionModeDaemon || !created.PersistentModel || !created.PersistentKVInMemory || !created.Ready {
		t.Fatalf("unexpected daemon session: %#v", created)
	}
	if created.RuntimeBackend != "mock-resident" || created.RuntimeStatus != "resident-session-scaffold" {
		t.Fatalf("expected daemon backend metadata, got %#v", created)
	}
	if err := created.Validate(); err != nil {
		t.Fatalf("expected created session to validate: %v", err)
	}
	recordPath := filepath.Join(sessionDir, url.PathEscape(created.SessionID)+".json")
	if _, err := os.Stat(recordPath); err != nil {
		t.Fatalf("expected persisted session record: %v", err)
	}

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+created.SessionID, nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected session get 200, got %d: %s", getRec.Code, getRec.Body.String())
	}

	for step := 1; step <= 2; step++ {
		decodeRec := httptest.NewRecorder()
		decodeBody := strings.NewReader(fmt.Sprintf(`{"step":%d,"tensor_envelope":{"protocol":"%s","dtype":"f32","shape":[1,1,896],"byte_count":3584,"checksum":"mock","sequence":%d,"stage_index":0}}`, step, runtimes.TensorEnvelopeV1, step))
		handler.ServeHTTP(decodeRec, httptest.NewRequest(http.MethodPost, "/v1/sessions/"+created.SessionID+"/decode", decodeBody))
		if decodeRec.Code != http.StatusOK {
			t.Fatalf("expected session decode 200, got %d: %s", decodeRec.Code, decodeRec.Body.String())
		}
		var decoded struct {
			Kind                 string `json:"kind"`
			Step                 uint64 `json:"step"`
			DecodeSteps          uint64 `json:"decode_steps"`
			LastPayloadBytes     int    `json:"last_payload_bytes"`
			Backend              string `json:"backend"`
			NativeKV             bool   `json:"native_kv"`
			LastSequence         uint64 `json:"last_sequence"`
			LastChecksum         string `json:"last_checksum"`
			PersistentKVInMemory bool   `json:"persistent_kv_in_memory"`
		}
		if err := json.Unmarshal(decodeRec.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Kind != "cmesh.stage_daemon_decode" || decoded.Step != uint64(step) || decoded.DecodeSteps != uint64(step) || !decoded.PersistentKVInMemory {
			t.Fatalf("unexpected daemon decode response: %#v", decoded)
		}
		if decoded.Backend != "mock-resident" || decoded.NativeKV || decoded.LastSequence != uint64(step) || decoded.LastChecksum != "mock" {
			t.Fatalf("expected backend decode metadata, got %#v", decoded)
		}
		if decoded.LastPayloadBytes != 0 {
			t.Fatalf("expected no payload bytes for metadata-only decode, got %#v", decoded)
		}
	}
	recordBody, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("expected persisted decode record: %v", err)
	}
	var persisted stageDaemonSessionRecord
	if err := json.Unmarshal(recordBody, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.DecodeSteps != 2 || persisted.LastStep != 2 || persisted.LastSequence != 2 || persisted.LastChecksum != "mock" {
		t.Fatalf("unexpected persisted daemon record: %#v", persisted)
	}

	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, httptest.NewRequest(http.MethodDelete, "/v1/sessions/"+created.SessionID, nil))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("expected session delete 200, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}

	missingRec := httptest.NewRecorder()
	handler.ServeHTTP(missingRec, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+created.SessionID, nil))
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("expected deleted session 404, got %d: %s", missingRec.Code, missingRec.Body.String())
	}
	if _, err := os.Stat(recordPath); !os.IsNotExist(err) {
		t.Fatalf("expected persisted session record to be removed, got err=%v", err)
	}
}

func TestStageRunnerDaemonRejectsInvalidActivationPayloadChecksum(t *testing.T) {
	handler := newStageRunnerDaemonHandler(t.TempDir())

	createBody := strings.NewReader(`{
		"parent_job_id":"job-parent",
		"stage_job_id":"job-stage-0",
		"model_id":"qwen2.5-14b-instruct-q4-k-m",
		"stage_index":0,
		"layer_start":0,
		"layer_end":15
	}`)
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", createBody)
	createReq.Host = "127.0.0.1:19781"
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created runtimes.StageSession
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	payload := []byte("activation")
	decodeBody, err := json.Marshal(map[string]any{
		"step":                      1,
		"stage_command":             "relay_decode",
		"activation_payload_base64": base64.StdEncoding.EncodeToString(payload),
		"tensor_envelope": map[string]any{
			"protocol":    runtimes.TensorEnvelopeV1,
			"dtype":       "f32",
			"shape":       []int{1, len(payload)},
			"byte_count":  len(payload),
			"checksum":    "sha256:not-the-payload",
			"sequence":    1,
			"stage_index": 0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decodeRec := httptest.NewRecorder()
	handler.ServeHTTP(decodeRec, httptest.NewRequest(http.MethodPost, "/v1/sessions/"+created.SessionID+"/decode", strings.NewReader(string(decodeBody))))
	if decodeRec.Code != http.StatusBadRequest || !strings.Contains(decodeRec.Body.String(), "checksum mismatch") {
		t.Fatalf("expected payload checksum rejection, got %d: %s", decodeRec.Code, decodeRec.Body.String())
	}
}

func TestPostStageDaemonDecodeReportsEmptySuccessBody(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer daemon.Close()

	_, err := postStageDaemonDecodeWithPayload(
		context.Background(),
		daemon.URL,
		"stage-0-empty-body",
		7,
		"source_decode",
		nil,
		nil,
		"",
		nil,
		"",
		0,
	)
	if err == nil {
		t.Fatal("expected empty stage daemon response error")
	}
	got := err.Error()
	for _, want := range []string{"empty response body", "stage-0-empty-body", "source_decode", "step=7"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in error %q", want, got)
		}
	}
}

func TestPostStageDaemonDecodeAcceptsLargeActivationResponse(t *testing.T) {
	largePayload := strings.Repeat("a", (2<<20)+128)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeStageDaemonJSON(w, http.StatusOK, map[string]any{
			"kind":                  "cmesh.stage_daemon_decode",
			"session_id":            "stage-0-large-body",
			"output_payload_base64": largePayload,
			"output_bytes":          len(largePayload),
			"ready":                 true,
		})
	}))
	defer daemon.Close()

	decoded, err := postStageDaemonDecodeWithPayload(
		context.Background(),
		daemon.URL,
		"stage-0-large-body",
		1,
		"source_decode",
		nil,
		nil,
		"",
		nil,
		"",
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if decoded["output_payload_base64"] != largePayload {
		t.Fatalf("large response payload was not preserved")
	}
}

func TestStageDaemonDecodePanicReturnsJSONError(t *testing.T) {
	handler := newStageRunnerDaemonHandlerWithBackend(t.TempDir(), panicStageSessionBackend{})

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{
		"parent_job_id":"job-parent",
		"stage_job_id":"job-stage-0",
		"model_id":"qwen2.5-32b-instruct-q4-k-m",
		"stage_index":0,
		"layer_start":0,
		"layer_end":21
	}`))
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected session create 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created runtimes.StageSession
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	decodeRec := httptest.NewRecorder()
	decodeReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/"+created.SessionID+"/decode", strings.NewReader(`{"step":1,"stage_command":"source_decode"}`))
	handler.ServeHTTP(decodeRec, decodeReq)
	if decodeRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected panic to become 500, got %d: %s", decodeRec.Code, decodeRec.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(decodeRec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("expected JSON panic error, got %q: %v", decodeRec.Body.String(), err)
	}
	if decoded["kind"] != "cmesh.stage_daemon_error" || !strings.Contains(fmt.Sprint(decoded["error"]), "decode exploded") {
		t.Fatalf("unexpected panic response: %#v", decoded)
	}
}

func TestStageRunnerDaemonLlamaCPPResidentBackendReportsMissingHooks(t *testing.T) {
	handler := newStageRunnerDaemonHandlerWithBackend(t.TempDir(), &llamaCPPResidentStageBackend{})

	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected health 200, got %d: %s", healthRec.Code, healthRec.Body.String())
	}
	var health struct {
		Backend       string                   `json:"backend"`
		NativeKV      bool                     `json:"native_kv"`
		BackendStatus stageDaemonBackendStatus `json:"backend_status"`
	}
	if err := json.Unmarshal(healthRec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health.Backend != "llama.cpp-resident" || !health.NativeKV {
		t.Fatalf("expected resident backend health, got %#v", health)
	}
	if health.BackendStatus.Kind != "llama.cpp-resident" || health.BackendStatus.Ready || !health.BackendStatus.NativeKV || len(health.BackendStatus.MissingHooks) != 3 {
		t.Fatalf("expected resident backend missing-hooks status, got %#v", health.BackendStatus)
	}

	createBody := strings.NewReader(`{
		"parent_job_id":"job-parent",
		"stage_job_id":"job-stage-0",
		"model_id":"qwen2.5-14b-instruct-q4-k-m",
		"stage_index":0,
		"layer_start":0,
		"layer_end":15
	}`)
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", createBody)
	createReq.Host = "127.0.0.1:19781"
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusConflict {
		t.Fatalf("expected resident backend 409 until native hooks exist, got %d: %s", createRec.Code, createRec.Body.String())
	}
	if !strings.Contains(createRec.Body.String(), "requires --runner-bin") {
		t.Fatalf("expected missing hooks error, got %q", createRec.Body.String())
	}
}

func TestStageRunnerDaemonLlamaCPPResidentBackendChecksRunnerBinary(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	handler := newStageRunnerDaemonHandlerWithBackend(dir, &llamaCPPResidentStageBackend{runnerBin: runner})

	healthRec := httptest.NewRecorder()
	handler.ServeHTTP(healthRec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if healthRec.Code != http.StatusOK {
		t.Fatalf("expected health 200, got %d: %s", healthRec.Code, healthRec.Body.String())
	}
	var health struct {
		BackendStatus stageDaemonBackendStatus `json:"backend_status"`
	}
	if err := json.Unmarshal(healthRec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if !health.BackendStatus.RunnerReady || health.BackendStatus.RunnerBin != runner {
		t.Fatalf("expected runner-ready resident backend status, got %#v", health.BackendStatus)
	}

	createBody := strings.NewReader(`{
		"parent_job_id":"job-parent",
		"stage_job_id":"job-stage-0",
		"model_id":"qwen2.5-14b-instruct-q4-k-m",
		"stage_index":0,
		"layer_start":0,
		"layer_end":15
	}`)
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", createBody)
	createReq.Host = "127.0.0.1:19781"
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusConflict {
		t.Fatalf("expected resident backend 409 until native hooks exist, got %d: %s", createRec.Code, createRec.Body.String())
	}
	if !strings.Contains(createRec.Body.String(), "requires model_path") {
		t.Fatalf("expected model path error, got %q", createRec.Body.String())
	}
}

func TestStageRunnerDaemonLlamaCPPResidentBackendRunsPrepareProbe(t *testing.T) {
	dir := t.TempDir()
	runner := filepath.Join(dir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/usr/bin/env sh
set -eu
stage_index=0
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--stage-index" ]; then
    shift
    stage_index="$1"
  fi
  shift
done
printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","stage_index":%s}\n' "$stage_index"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(dir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := newStageRunnerDaemonHandlerWithBackend(dir, &llamaCPPResidentStageBackend{runnerBin: runner})

	createBody := strings.NewReader(fmt.Sprintf(`{
		"parent_job_id":"job-parent",
		"stage_job_id":"job-stage-0",
		"model_id":"qwen2.5-14b-instruct-q4-k-m",
		"model_path":%q,
		"stage_index":0,
		"layer_start":0,
		"layer_end":15
	}`, modelPath))
	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", createBody)
	createReq.Host = "127.0.0.1:19781"
	handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected resident prepare probe session 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created runtimes.StageSession
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.RuntimeBackend != "llama.cpp-resident" || created.RuntimeStatus != "prepare_probe_ready_missing_native_decode_hooks" || created.Ready {
		t.Fatalf("unexpected resident prepare-probed session: %#v", created)
	}
	if created.ModelPath != modelPath {
		t.Fatalf("expected session model path %q, got %#v", modelPath, created)
	}
}

func containsAdjacentArgs(args []string, key string, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
