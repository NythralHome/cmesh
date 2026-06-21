package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/runtimes"
)

const gb = 1024 * 1024 * 1024

func TestNewMatrixMultiplyInput(t *testing.T) {
	input, err := newMatrixMultiplyInput(64, 2)
	if err != nil {
		t.Fatal(err)
	}

	var decoded matrixMultiplyInput
	if err := json.Unmarshal([]byte(input), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Size != 64 || decoded.Iterations != 2 {
		t.Fatalf("unexpected input %#v", decoded)
	}
}

func TestStageDaemonHTTPTimeoutUsesJobTimeoutForHeavyStages(t *testing.T) {
	if got := stageDaemonHTTPTimeout(120000); got != 120*time.Second {
		t.Fatalf("expected 120s stage daemon timeout, got %s", got)
	}
	if got := stageDaemonHTTPTimeout(0); got != 2*time.Minute {
		t.Fatalf("expected default 2m stage daemon timeout, got %s", got)
	}
	if got := stageDaemonHTTPTimeout(20 * 60 * 1000); got != 10*time.Minute {
		t.Fatalf("expected capped 10m stage daemon timeout, got %s", got)
	}
}

func TestExecuteMatrixMultiplyJob(t *testing.T) {
	result, err := executeJob(jobs.Job{
		Type:  "compute.matrix_multiply",
		Input: `{"size":32,"iterations":2}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	var decoded matrixMultiplyResult
	if err := json.Unmarshal([]byte(result), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != "matrix_multiply" {
		t.Fatalf("unexpected result kind %q", decoded.Kind)
	}
	if decoded.Size != 32 || decoded.Iterations != 2 {
		t.Fatalf("unexpected result %#v", decoded)
	}
	if decoded.Operations != int64(2*32*32*32*2) {
		t.Fatalf("unexpected operations %d", decoded.Operations)
	}
	if decoded.GFLOPS <= 0 {
		t.Fatalf("expected positive gflops, got %f", decoded.GFLOPS)
	}
	if decoded.WorkerRuntime == "" {
		t.Fatalf("expected worker runtime")
	}
}

func TestExecuteMatrixMultiplyJobRejectsInvalidInput(t *testing.T) {
	_, err := executeJob(jobs.Job{
		Type:  "compute.matrix_multiply",
		Input: `{"size":8,"iterations":1}`,
	})
	if err == nil {
		t.Fatal("expected invalid size error")
	}

	_, err = executeJob(jobs.Job{
		Type:  "compute.matrix_multiply",
		Input: `{"size":32,"iterations":101}`,
	})
	if err == nil {
		t.Fatal("expected invalid iterations error")
	}
}

func TestParseWorkerOptionsRPCBackend(t *testing.T) {
	options, err := parseWorkerOptions("worker run", []string{
		"--manager", "http://manager:8080/",
		"--token", "join-token",
		"--rpc",
		"--rpc-host", "0.0.0.0",
		"--rpc-advertise-host", "10.0.0.25",
		"--rpc-port", "50123",
		"--rpc-cache=false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.managerURL != "http://manager:8080" || !options.rpcEnabled {
		t.Fatalf("unexpected rpc worker options: %#v", options)
	}
	if options.rpcHost != "0.0.0.0" || options.rpcAdvertiseHost != "10.0.0.25" || options.rpcPort != 50123 || options.rpcCache {
		t.Fatalf("unexpected rpc config: %#v", options)
	}
}

func TestParseWorkerOptionsStageDaemonURL(t *testing.T) {
	options, err := parseWorkerOptions("worker run", []string{
		"--stage-daemon-url", "http://127.0.0.1:19781/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.stageDaemonURL != "http://127.0.0.1:19781" {
		t.Fatalf("unexpected stage daemon url: %#v", options)
	}
}

func TestValidateManagerSecurityOptionsAllowsLocalhostWithoutTokens(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8080", "localhost:8080", "http://127.0.0.1:8080"} {
		if err := validateManagerSecurityOptions(managerSecurityOptions{Addr: addr}); err != nil {
			t.Fatalf("expected %s to be treated as local dev manager: %v", addr, err)
		}
	}
}

func TestValidateManagerSecurityOptionsRejectsPublicManagerWithoutTokens(t *testing.T) {
	for _, input := range []managerSecurityOptions{
		{Addr: ":8080"},
		{Addr: "0.0.0.0:8080"},
		{Addr: "10.0.0.10:8080"},
		{Addr: "127.0.0.1:8080", PublicURL: "https://alpha.cmesh.example.com"},
	} {
		err := validateManagerSecurityOptions(input)
		if err == nil {
			t.Fatalf("expected public manager options to require tokens: %#v", input)
		}
		if !strings.Contains(err.Error(), "CMESH_JOIN_TOKEN") || !strings.Contains(err.Error(), "CMESH_OPERATOR_TOKEN") {
			t.Fatalf("expected actionable token error, got %v", err)
		}
	}
}

func TestValidateManagerSecurityOptionsAllowsPublicManagerWithTokens(t *testing.T) {
	err := validateManagerSecurityOptions(managerSecurityOptions{
		Addr:          "0.0.0.0:8080",
		PublicURL:     "https://alpha.cmesh.example.com",
		JoinToken:     "join-secret",
		OperatorToken: "operator-secret",
	})
	if err != nil {
		t.Fatalf("expected public manager with tokens to pass: %v", err)
	}
}

func TestValidateManagerSecurityOptionsAllowsExplicitInsecurePublicDev(t *testing.T) {
	err := validateManagerSecurityOptions(managerSecurityOptions{
		Addr:                "0.0.0.0:8080",
		AllowInsecurePublic: true,
	})
	if err != nil {
		t.Fatalf("expected explicit insecure dev override to pass: %v", err)
	}
}

func TestApplyWorkerRuntimeModelOverrides(t *testing.T) {
	snapshot := cluster.ResourceSnapshot{}
	updated := applyWorkerRuntimeModelOverrides(snapshot, workerOptions{
		modelID:     "qwen2.5-0.5b-instruct-q4-k-m",
		modelPath:   "/var/lib/cmesh/models/qwen.gguf",
		modelLayers: 24,
		runtimeName: string(models.RuntimeLlamaCPP),
	})

	if len(updated.Runtimes) != 1 || !updated.Runtimes[0].Ready || updated.Runtimes[0].Name != string(models.RuntimeLlamaCPP) {
		t.Fatalf("expected ready runtime override, got %#v", updated.Runtimes)
	}
	if len(updated.Models) != 1 {
		t.Fatalf("expected model override, got %#v", updated.Models)
	}
	model := updated.Models[0]
	if model.ID != "qwen2.5-0.5b-instruct-q4-k-m" || model.Path != "/var/lib/cmesh/models/qwen.gguf" || model.Layers != 24 || !model.Ready {
		t.Fatalf("unexpected model override: %#v", model)
	}

	again := applyWorkerRuntimeModelOverrides(updated, workerOptions{
		modelID:     "qwen2.5-0.5b-instruct-q4-k-m",
		modelPath:   "/var/lib/cmesh/models/qwen.gguf",
		modelLayers: 24,
		runtimeName: string(models.RuntimeLlamaCPP),
	})
	if len(again.Runtimes) != 1 || len(again.Models) != 1 {
		t.Fatalf("override should not duplicate existing resources: runtimes=%#v models=%#v", again.Runtimes, again.Models)
	}
}

func TestApplyWorkerRuntimeModelOverridesAdvertisesStageDaemon(t *testing.T) {
	daemon := httptest.NewServer(newStageRunnerDaemonHandler(t.TempDir()))
	defer daemon.Close()

	updated := applyWorkerRuntimeModelOverrides(cluster.ResourceSnapshot{}, workerOptions{
		runtimeName:    string(models.RuntimeLlamaCPP),
		stageDaemonURL: daemon.URL,
	})
	if len(updated.Runtimes) != 1 {
		t.Fatalf("expected runtime with stage daemon, got %#v", updated.Runtimes)
	}
	runtimeStatus := updated.Runtimes[0]
	if !runtimeStatus.Ready || len(runtimeStatus.StageRuntimes) != 1 {
		t.Fatalf("expected ready stage daemon runtime, got %#v", runtimeStatus)
	}
	stageRuntime := runtimeStatus.StageRuntimes[0]
	if !stageRuntime.Ready || stageRuntime.Endpoint != daemon.URL || stageRuntime.Protocol != runtimes.StageSessionV1 {
		t.Fatalf("unexpected stage daemon runtime: %#v", stageRuntime)
	}
}

func TestApplyWorkerRuntimeModelOverridesBlocksUnreadyStageDaemonBackend(t *testing.T) {
	daemon := httptest.NewServer(newStageRunnerDaemonHandlerWithBackend(t.TempDir(), &llamaCPPResidentStageBackend{}))
	defer daemon.Close()

	updated := applyWorkerRuntimeModelOverrides(cluster.ResourceSnapshot{}, workerOptions{
		runtimeName:    string(models.RuntimeLlamaCPP),
		stageDaemonURL: daemon.URL,
	})
	if len(updated.Runtimes) != 1 || len(updated.Runtimes[0].StageRuntimes) != 1 {
		t.Fatalf("expected runtime with stage daemon probe, got %#v", updated.Runtimes)
	}
	runtimeStatus := updated.Runtimes[0]
	if !runtimeStatus.Ready {
		t.Fatalf("base runtime should remain ready while stage daemon probe is blocked, got %#v", runtimeStatus)
	}
	stageRuntime := runtimeStatus.StageRuntimes[0]
	if stageRuntime.Ready || len(stageRuntime.Blockers) == 0 {
		t.Fatalf("expected blocked stage daemon runtime, got %#v", stageRuntime)
	}
	if !strings.Contains(stageRuntime.Blockers[0], "native hooks") {
		t.Fatalf("expected native hooks blocker, got %#v", stageRuntime.Blockers)
	}
}

func TestLlamaCPPResidentBackendPrepareProbeDoesNotClaimDecodeReady(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
set -eu
stage_index=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --stage-index) shift; stage_index="$1" ;;
  esac
  shift
done
printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","stage_index":%s}\n' "$stage_index"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(workDir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := llamaCPPResidentStageBackend{runnerBin: runner}
	session, err := backend.Prepare(context.Background(), runtimes.StageSession{
		Protocol:       runtimes.StageSessionV1,
		Mode:           runtimes.StageSessionModeDaemon,
		SessionID:      "stage-0-test",
		ModelID:        "qwen2.5-0.5b-instruct-q4-k-m",
		StageIndex:     0,
		LayerStart:     0,
		LayerEnd:       7,
		Endpoint:       "http://127.0.0.1:19781",
		RuntimeBackend: "llama.cpp-resident",
	}, stageDaemonSessionRequest{
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Ready || session.PersistentModel || session.PersistentKVInMemory {
		t.Fatalf("resident prepare probe must not claim native decode readiness: %#v", session)
	}
	if session.RuntimeStatus != "prepare_probe_ready_missing_native_decode_hooks" {
		t.Fatalf("expected explicit missing-hooks status, got %#v", session)
	}
	if _, err := backend.Decode(context.Background(), session, stageDaemonDecodeRequest{}); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("expected resident decode to stay blocked until native hooks exist, got %v", err)
	}
}

func TestLlamaCPPResidentBackendUsesResidentLoopNativePrepareWithoutClaimingDecodeReady(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
set -eu
command=""
stage_index=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --command) shift; command="$1" ;;
    --stage-index) shift; stage_index="$1" ;;
  esac
  shift
done
case "$command" in
  prepare)
    printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","stage_index":%s}\n' "$stage_index"
    ;;
  resident-capabilities)
    printf '{"kind":"cmesh.llamacpp_resident_capabilities","protocol":"cdip.llamacpp-resident-runner-v1","ready":false,"native_kv":true,"persistent_model":false,"persistent_kv_in_memory":false,"prepare_hook":true,"decode_hook":false}\n'
    ;;
  resident-loop)
    sessions=0
    while IFS= read -r line; do
      case "$line" in
        command=capabilities)
          printf '{"kind":"cmesh.llamacpp_resident_loop_capabilities","protocol":"cdip.llamacpp-resident-loop-v1","runner_protocol":"cdip.llamacpp-resident-runner-v1","ready":false,"persistent_process":true,"native_kv":true,"persistent_model":false,"persistent_kv_in_memory":false,"prepare_hook":true,"decode_hook":false,"session_count":%s}\n' "$sessions"
          ;;
        command=prepare*native_prepare=1*)
          sessions=1
          printf '{"kind":"cmesh.llamacpp_resident_loop_prepare","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_model_context_ready_missing_decode_hooks","session_registered":true,"session_id":"stage-0-test","persistent_model":true,"persistent_kv_in_memory":true,"n_layer":24,"n_embd":896,"selected_tensor_count":13,"selected_bytes":104476416}\n'
          ;;
        command=decode*stage_command=terminal_decode*)
          printf '{"kind":"cmesh.llamacpp_resident_loop_decode","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_terminal_decoded","session_id":"stage-0-test","session_found":true,"decode_steps":1,"persistent_model":true,"persistent_kv_in_memory":true,"activation_file":"","payload_bytes":0,"output_file":"","output_bytes":0,"dtype":"","shape":"","token_id":-1,"input_mode":"activation_file","position_offset":0,"token_count":1,"next_token_id":42,"next_token_text":" hello","next_token_logit":1.5,"final":false,"decode_status":0,"error":"","blocker":""}\n'
          ;;
        command=shutdown)
          printf '{"kind":"cmesh.llamacpp_resident_loop_shutdown","protocol":"cdip.llamacpp-resident-loop-v1","status":"closing","session_count":%s}\n' "$sessions"
          exit 0
          ;;
        *)
          printf '{"kind":"cmesh.llamacpp_resident_loop_error","protocol":"cdip.llamacpp-resident-loop-v1","status":"unsupported_command"}\n'
          ;;
      esac
    done
    ;;
  *)
    echo "unsupported command $command" >&2
    exit 2
    ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(workDir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := llamaCPPResidentStageBackend{runnerBin: runner}
	status := backend.Status()
	if status.Ready || status.DecodeReady || !status.RunnerReady || status.ResidentProtocol != "cdip.llamacpp-resident-runner-v1" {
		t.Fatalf("expected prepare-only resident backend status, got %#v", status)
	}
	if len(status.MissingHooks) == 0 || !strings.Contains(status.Blocker, "decode hooks") {
		t.Fatalf("expected decode blocker after prepare hook is present, got %#v", status)
	}
	session, err := backend.Prepare(context.Background(), runtimes.StageSession{
		Protocol:   runtimes.StageSessionV1,
		Mode:       runtimes.StageSessionModeDaemon,
		SessionID:  "stage-0-test",
		ModelID:    "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
		Endpoint:   "http://127.0.0.1:19781",
	}, stageDaemonSessionRequest{
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background(), session)
	if session.Ready || !session.PersistentModel || !session.PersistentKVInMemory || session.RuntimeStatus != "resident_loop_model_context_ready_missing_decode_hooks" {
		t.Fatalf("expected resident-loop model/context without decode readiness, got %#v", session)
	}
	result, err := backend.Decode(context.Background(), session, stageDaemonDecodeRequest{Step: 1, StageCommand: "terminal_decode"})
	if err != nil {
		t.Fatalf("expected resident terminal decode to be passed through, got %v", err)
	}
	if result.NextTokenID == nil || *result.NextTokenID != 42 || result.NextTokenText != " hello" || result.Final == nil || *result.Final {
		t.Fatalf("expected resident terminal token result, got %#v", result)
	}
}

func TestLlamaCPPResidentBackendUsesRunnerResidentDecodeContract(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
set -eu
command=""
session_id=""
step="1"
stage_index=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --command) shift; command="$1" ;;
    --session-id) shift; session_id="$1" ;;
    --step) shift; step="$1" ;;
    --stage-index) shift; stage_index="$1" ;;
  esac
  shift
done
case "$command" in
  prepare)
    printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","stage_index":%s}\n' "$stage_index"
    ;;
  resident-capabilities)
    printf '{"kind":"cmesh.llamacpp_resident_capabilities","protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true}\n'
    ;;
  resident-loop)
    sessions=0
    while IFS= read -r line; do
      case "$line" in
        command=capabilities)
          printf '{"kind":"cmesh.llamacpp_resident_loop_capabilities","protocol":"cdip.llamacpp-resident-loop-v1","runner_protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"persistent_process":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true,"session_count":%s}\n' "$sessions"
          ;;
        command=prepare*native_prepare=1*)
          sessions=1
          printf '{"kind":"cmesh.llamacpp_resident_loop_prepare","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_ready","session_registered":true,"session_id":"stage-0-test","persistent_model":true,"persistent_kv_in_memory":true,"n_layer":24,"n_embd":896,"selected_tensor_count":13,"selected_bytes":104476416}\n'
          ;;
        command=decode*)
          line_step="$step"
          case "$line" in
            *" step=2"*) line_step=2 ;;
          esac
          printf '{"kind":"cmesh.llamacpp_resident_loop_decode","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_decoded","session_id":"stage-0-test","session_found":true,"decode_steps":1,"persistent_model":true,"persistent_kv_in_memory":true,"sequence":%s,"checksum":"sha256:resident-%s","payload_bytes":0,"output_bytes":0,"decode_status":0}\n' "$line_step" "$line_step"
          ;;
        command=shutdown)
          printf '{"kind":"cmesh.llamacpp_resident_loop_shutdown","protocol":"cdip.llamacpp-resident-loop-v1","status":"closing","session_count":%s}\n' "$sessions"
          exit 0
          ;;
      esac
    done
    ;;
  resident-decode)
    printf '{"kind":"cmesh.llamacpp_resident_decode","status":"decoded","session_id":"%s","sequence":%s,"checksum":"sha256:resident-%s"}\n' "$session_id" "$step" "$step"
    ;;
  *)
    echo "unsupported command $command" >&2
    exit 2
    ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(workDir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := llamaCPPResidentStageBackend{runnerBin: runner}
	status := backend.Status()
	if !status.Ready || !status.DecodeReady || status.ResidentProtocol != "cdip.llamacpp-resident-runner-v1" {
		t.Fatalf("expected resident backend to report ready runner contract, got %#v", status)
	}
	session, err := backend.Prepare(context.Background(), runtimes.StageSession{
		Protocol:   runtimes.StageSessionV1,
		Mode:       runtimes.StageSessionModeDaemon,
		SessionID:  "stage-0-test",
		ModelID:    "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
		Endpoint:   "http://127.0.0.1:19781",
	}, stageDaemonSessionRequest{
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !session.Ready || !session.PersistentModel || !session.PersistentKVInMemory || session.RuntimeStatus != "resident_loop_ready" {
		t.Fatalf("expected resident-ready session, got %#v", session)
	}
	decoded, err := backend.Decode(context.Background(), session, stageDaemonDecodeRequest{Step: 2})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Sequence != 2 || decoded.Checksum != "sha256:resident-2" {
		t.Fatalf("unexpected resident decode result: %#v", decoded)
	}
}

func TestLlamaCPPResidentBackendDoesNotFallbackToBlockedResidentDecode(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
set -eu
command=""
stage_index=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --command) shift; command="$1" ;;
    --stage-index) shift; stage_index="$1" ;;
  esac
  shift
done
case "$command" in
  prepare)
    printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","stage_index":%s}\n' "$stage_index"
    ;;
  resident-capabilities)
    printf '{"kind":"cmesh.llamacpp_resident_capabilities","protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true}\n'
    ;;
  resident-loop)
    sessions=0
    while IFS= read -r line; do
      case "$line" in
        command=capabilities)
          printf '{"kind":"cmesh.llamacpp_resident_loop_capabilities","protocol":"cdip.llamacpp-resident-loop-v1","runner_protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"persistent_process":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true,"session_count":%s}\n' "$sessions"
          ;;
        command=prepare*)
          sessions=1
          printf '{"kind":"cmesh.llamacpp_resident_loop_prepare","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_ready","session_registered":true,"session_id":"stage-0-test","persistent_model":true,"persistent_kv_in_memory":true}\n'
          ;;
        command=decode*)
          printf '{"kind":"cmesh.llamacpp_resident_loop_decode","protocol":"cdip.llamacpp-resident-loop-v1","status":"llama_decode_failed","session_id":"stage-0-test","session_found":true,"decode_steps":1,"persistent_model":true,"persistent_kv_in_memory":true,"decode_status":1,"error":"context too small"}\n'
          ;;
        command=shutdown)
          printf '{"kind":"cmesh.llamacpp_resident_loop_shutdown","protocol":"cdip.llamacpp-resident-loop-v1","status":"closing","session_count":%s}\n' "$sessions"
          exit 0
          ;;
      esac
    done
    ;;
  resident-decode)
    echo "resident-decode fallback must not be called" >&2
    exit 9
    ;;
  *)
    echo "unsupported command $command" >&2
    exit 2
    ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(workDir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := llamaCPPResidentStageBackend{runnerBin: runner}
	session, err := backend.Prepare(context.Background(), runtimes.StageSession{
		Protocol:   runtimes.StageSessionV1,
		Mode:       runtimes.StageSessionModeDaemon,
		SessionID:  "stage-0-test",
		ModelID:    "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
		Endpoint:   "http://127.0.0.1:19781",
	}, stageDaemonSessionRequest{
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background(), session)

	_, err = backend.Decode(context.Background(), session, stageDaemonDecodeRequest{Step: 1, StageCommand: "source_decode"})
	if err == nil {
		t.Fatal("expected resident-loop decode error")
	}
	if !strings.Contains(err.Error(), "context too small") || strings.Contains(err.Error(), "resident-decode fallback") {
		t.Fatalf("expected original resident-loop error without fallback, got %v", err)
	}
}

func TestResidentLoopPrepareContextSizeDefaultFitsNormalPrompts(t *testing.T) {
	t.Setenv("CMESH_RESIDENT_STAGE_CTX", "")
	if got := residentLoopPrepareContextSize(); got != 2048 {
		t.Fatalf("expected default resident stage ctx 2048, got %d", got)
	}
	t.Setenv("CMESH_RESIDENT_STAGE_CTX", "4096")
	if got := residentLoopPrepareContextSize(); got != 4096 {
		t.Fatalf("expected env resident stage ctx 4096, got %d", got)
	}
	t.Setenv("CMESH_RESIDENT_STAGE_CTX", "bad")
	if got := residentLoopPrepareContextSize(); got != 2048 {
		t.Fatalf("expected invalid env to fall back to 2048, got %d", got)
	}
}

func TestLlamaCPPResidentBackendUsesResidentLoopWhenAvailable(t *testing.T) {
	workDir := t.TempDir()
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
set -eu
command=""
stage_index=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --command) shift; command="$1" ;;
    --stage-index) shift; stage_index="$1" ;;
  esac
  shift
done
case "$command" in
  prepare)
    printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","stage_index":%s}\n' "$stage_index"
    ;;
  resident-capabilities)
    printf '{"kind":"cmesh.llamacpp_resident_capabilities","protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true}\n'
    ;;
  resident-loop)
    sessions=0
    while IFS= read -r line; do
      case "$line" in
        command=capabilities)
          printf '{"kind":"cmesh.llamacpp_resident_loop_capabilities","protocol":"cdip.llamacpp-resident-loop-v1","runner_protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"persistent_process":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"session_count":%s}\n' "$sessions"
          ;;
        command=prepare*)
          sessions=1
          printf '{"kind":"cmesh.llamacpp_resident_loop_prepare","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_ready","session_registered":true,"session_id":"stage-0-test","persistent_model":true,"persistent_kv_in_memory":true}\n'
          ;;
        command=decode*)
          printf '{"kind":"cmesh.llamacpp_resident_loop_decode","protocol":"cdip.llamacpp-resident-loop-v1","status":"decoded","session_id":"stage-0-test","session_found":true,"decode_steps":1,"sequence":3,"checksum":"sha256:loop-3"}\n'
          ;;
        command=shutdown)
          printf '{"kind":"cmesh.llamacpp_resident_loop_shutdown","protocol":"cdip.llamacpp-resident-loop-v1","status":"closing","session_count":%s}\n' "$sessions"
          exit 0
          ;;
        *)
          printf '{"kind":"cmesh.llamacpp_resident_loop_error","protocol":"cdip.llamacpp-resident-loop-v1","status":"unsupported_command"}\n'
          ;;
      esac
    done
    ;;
  *)
    echo "unsupported command $command" >&2
    exit 2
    ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	modelPath := filepath.Join(workDir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := llamaCPPResidentStageBackend{runnerBin: runner}
	session, err := backend.Prepare(context.Background(), runtimes.StageSession{
		Protocol:   runtimes.StageSessionV1,
		Mode:       runtimes.StageSessionModeDaemon,
		SessionID:  "stage-0-test",
		ModelID:    "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
		Endpoint:   "http://127.0.0.1:19781",
	}, stageDaemonSessionRequest{
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background(), session)
	if !session.Ready || !session.PersistentModel || !session.PersistentKVInMemory || session.RuntimeStatus != "resident_loop_ready" {
		t.Fatalf("expected resident-loop-ready session, got %#v", session)
	}
	decoded, err := backend.Decode(context.Background(), session, stageDaemonDecodeRequest{Step: 3, StageCommand: "source_decode"})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Sequence != 3 || decoded.Checksum != "sha256:loop-3" {
		t.Fatalf("unexpected resident-loop decode result: %#v", decoded)
	}
}

func TestLlamaCPPResidentBackendForwardsDecodeInputsToResidentLoop(t *testing.T) {
	workDir := t.TempDir()
	seenFile := filepath.Join(workDir, "seen.env")
	runner := filepath.Join(workDir, "cmesh-stage-runner")
	if err := os.WriteFile(runner, []byte(`#!/bin/sh
set -eu
command=""
stage_index=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --command) shift; command="$1" ;;
    --stage-index) shift; stage_index="$1" ;;
  esac
  shift
done
case "$command" in
  prepare)
    printf '{"kind":"cmesh.llamacpp_stage_prepare","status":"metadata_ready","stage_index":%s}\n' "$stage_index"
    ;;
  resident-capabilities)
    printf '{"kind":"cmesh.llamacpp_resident_capabilities","protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true}\n'
    ;;
  resident-loop)
    sessions=0
    while IFS= read -r line; do
      case "$line" in
        command=capabilities)
          printf '{"kind":"cmesh.llamacpp_resident_loop_capabilities","protocol":"cdip.llamacpp-resident-loop-v1","runner_protocol":"cdip.llamacpp-resident-runner-v1","ready":true,"persistent_process":true,"native_kv":true,"persistent_model":true,"persistent_kv_in_memory":true,"prepare_hook":true,"decode_hook":true,"session_count":%s}\n' "$sessions"
          ;;
        command=prepare*)
          sessions=1
          printf '{"kind":"cmesh.llamacpp_resident_loop_prepare","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_ready","session_registered":true,"session_id":"stage-0-test","persistent_model":true,"persistent_kv_in_memory":true}\n'
          ;;
        command=decode*)
          activation_file=""
          output_file=""
          dtype=""
          shape=""
          token_id=""
          for part in $line; do
            case "$part" in
              activation_file=*) activation_file="${part#activation_file=}" ;;
              output_file=*) output_file="${part#output_file=}" ;;
              dtype=*) dtype="${part#dtype=}" ;;
              shape=*) shape="${part#shape=}" ;;
              token_id=*) token_id="${part#token_id=}" ;;
            esac
          done
          bytes="$(wc -c < "$activation_file" | tr -d ' ')"
          if [ -n "$output_file" ]; then
            printf 'out!' > "$output_file"
          fi
          {
            printf 'activation_file=%s\n' "$activation_file"
            printf 'output_file=%s\n' "$output_file"
            printf 'dtype=%s\n' "$dtype"
            printf 'shape=%s\n' "$shape"
            printf 'token_id=%s\n' "$token_id"
            printf 'bytes=%s\n' "$bytes"
          } > "$CMESH_TEST_SEEN_FILE"
          printf '{"kind":"cmesh.llamacpp_resident_loop_decode","protocol":"cdip.llamacpp-resident-loop-v1","status":"resident_source_decoded","session_id":"stage-0-test","session_found":true,"decode_steps":1,"sequence":7,"checksum":"sha256:loop-forward","payload_bytes":%s,"output_file":"%s","output_bytes":4}\n' "$bytes" "$output_file"
          ;;
        command=shutdown)
          printf '{"kind":"cmesh.llamacpp_resident_loop_shutdown","protocol":"cdip.llamacpp-resident-loop-v1","status":"closing","session_count":%s}\n' "$sessions"
          exit 0
          ;;
        *)
          printf '{"kind":"cmesh.llamacpp_resident_loop_error","protocol":"cdip.llamacpp-resident-loop-v1","status":"unsupported_command"}\n'
          ;;
      esac
    done
    ;;
  *)
    echo "unsupported command $command" >&2
    exit 2
    ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMESH_TEST_SEEN_FILE", seenFile)
	modelPath := filepath.Join(workDir, "model.gguf")
	if err := os.WriteFile(modelPath, []byte("fake model"), 0o644); err != nil {
		t.Fatal(err)
	}

	backend := llamaCPPResidentStageBackend{runnerBin: runner}
	session, err := backend.Prepare(context.Background(), runtimes.StageSession{
		Protocol:   runtimes.StageSessionV1,
		Mode:       runtimes.StageSessionModeDaemon,
		SessionID:  "stage-0-test",
		ModelID:    "qwen2.5-0.5b-instruct-q4-k-m",
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
		Endpoint:   "http://127.0.0.1:19781",
	}, stageDaemonSessionRequest{
		ModelPath:  modelPath,
		StageIndex: 0,
		LayerStart: 0,
		LayerEnd:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close(context.Background(), session)

	payload := []byte("0123456789abcdef")
	sum := sha256.Sum256(payload)
	tokenID := 42
	decoded, err := backend.Decode(context.Background(), session, stageDaemonDecodeRequest{
		Step:                    7,
		StageCommand:            "source_decode",
		ActivationPayloadBase64: base64.StdEncoding.EncodeToString(payload),
		PreviousTokenID:         &tokenID,
		TensorEnvelope: &runtimes.TensorEnvelope{
			Protocol:   runtimes.TensorEnvelopeV1,
			DType:      "f32",
			Shape:      []int{1, 4},
			ByteCount:  len(payload),
			Checksum:   "sha256:" + hex.EncodeToString(sum[:]),
			Sequence:   7,
			StageIndex: 0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Sequence != 7 || decoded.Checksum != "sha256:loop-forward" || decoded.OutputBytes != 4 || decoded.OutputPayloadBase64 != base64.StdEncoding.EncodeToString([]byte("out!")) {
		t.Fatalf("unexpected resident-loop decode result: %#v", decoded)
	}
	seen, err := os.ReadFile(seenFile)
	if err != nil {
		t.Fatal(err)
	}
	seenText := string(seen)
	for _, want := range []string{"cmesh-resident-loop-output-", "dtype=f32", "shape=1,4", "token_id=42", "bytes=16"} {
		if !strings.Contains(seenText, want) {
			t.Fatalf("resident-loop did not receive %s; seen:\n%s", want, seenText)
		}
	}
}

func TestStartWorkerRPCBackendWritesState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake rpc-server shell process test is unix-only")
	}
	binDir := t.TempDir()
	cli := filepath.Join(binDir, "llama-cli")
	server := filepath.Join(binDir, "rpc-server")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	serverScript := "#!/bin/sh\ntrap 'exit 0' INT TERM\nwhile true; do sleep 1; done\n"
	if err := os.WriteFile(server, []byte(serverScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	cacheDir := t.TempDir()
	process, err := startWorkerRPCBackend(context.Background(), workerOptions{
		managerURL:       "http://10.0.0.1:8080",
		cacheDir:         cacheDir,
		rpcEnabled:       true,
		rpcHost:          "127.0.0.1",
		rpcAdvertiseHost: "10.0.0.25",
		rpcPort:          50123,
		rpcCache:         false,
	})
	if err != nil {
		t.Fatal(err)
	}
	state, ok := resources.ReadLlamaCPPRPCState(cacheDir)
	if !ok {
		t.Fatal("expected rpc state to be written")
	}
	if state.Endpoint != "10.0.0.25:50123" || state.BindEndpoint != "127.0.0.1:50123" || state.PID == 0 {
		t.Fatalf("unexpected rpc state: %#v", state)
	}
	process.Stop()
	if _, ok := resources.ReadLlamaCPPRPCState(cacheDir); ok {
		t.Fatal("expected rpc state to be cleared on stop")
	}
}

func TestWorkerResourceGuardAllowsMatchingJob(t *testing.T) {
	result, err := executeJobWithResources(jobs.Job{
		Type:  "echo",
		Input: "hello",
		Requirements: jobs.Requirements{
			CPUCores:    2,
			MemoryBytes: 1 * gb,
		},
	}, cluster.ResourceSnapshot{
		CPU: cluster.CPUResources{
			CoresAllowed: 2,
		},
		Memory: cluster.MemoryResources{
			AllowedBytes: 2 * gb,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result != "hello" {
		t.Fatalf("unexpected result %q", result)
	}
}

func TestWorkerResourceGuardRejectsInsufficientMemory(t *testing.T) {
	_, err := executeJobWithResources(jobs.Job{
		Type: "echo",
		Requirements: jobs.Requirements{
			MemoryBytes: 4 * gb,
		},
	}, cluster.ResourceSnapshot{
		Memory: cluster.MemoryResources{
			AllowedBytes: 2 * gb,
		},
	})
	if err == nil {
		t.Fatal("expected memory guard error")
	}
	if !strings.Contains(err.Error(), "requires 4.0 GB RAM") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func TestWorkerResourceGuardRejectsModelInstallOverQuota(t *testing.T) {
	_, err := executeJobWithResources(jobs.Job{
		Type: models.JobInstall,
		Requirements: jobs.Requirements{
			DiskBytes: 4 * gb,
		},
	}, cluster.ResourceSnapshot{
		Storage: cluster.StorageResources{
			AllowedBytes:      8 * gb,
			UsedByModelsBytes: 6 * gb,
			FreeBytes:         20 * gb,
		},
	})
	if err == nil {
		t.Fatal("expected model quota guard error")
	}
	if !strings.Contains(err.Error(), "remaining model quota") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func TestWorkerResourceGuardRejectsMissingGPU(t *testing.T) {
	_, err := executeJobWithResources(jobs.Job{
		Type: "echo",
		Requirements: jobs.Requirements{
			GPURequired: true,
		},
	}, cluster.ResourceSnapshot{})
	if err == nil {
		t.Fatal("expected GPU guard error")
	}
	if !strings.Contains(err.Error(), "requires compute GPU") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func TestWorkerResourceGuardRejectsInsufficientVRAM(t *testing.T) {
	_, err := executeJobWithResources(jobs.Job{
		Type: "echo",
		Requirements: jobs.Requirements{
			VRAMBytes: 8 * gb,
		},
	}, cluster.ResourceSnapshot{
		GPU: []cluster.GPUResources{
			{
				Name:              "Test GPU",
				ComputeCompatible: true,
				AllowedVRAMBytes:  4 * gb,
			},
		},
	})
	if err == nil {
		t.Fatal("expected VRAM guard error")
	}
	if !strings.Contains(err.Error(), "requires compute GPU with 8.0 GB VRAM") {
		t.Fatalf("unexpected error %q", err.Error())
	}
}

func TestSanitizeModelTextRemovesChatTemplateNoise(t *testing.T) {
	input := strings.Join([]string{
		"<|im_start|>assistant",
		"<|im_end|>",
		"</|im_start|>",
		"user",
		"<start_of_turn>model",
		"You will answer the user's question.",
		"Привіт, Сергію.",
		"<end_of_turn>",
		"assistant:",
		"Як я можу допомогти?",
	}, "\n")

	got := sanitizeModelText(input)
	if strings.Contains(got, "<|") || strings.Contains(got, "start_of_turn") || strings.Contains(strings.ToLower(got), "assistant:") || strings.Contains(strings.ToLower(got), "user") {
		t.Fatalf("template noise was not removed: %q", got)
	}
	if !strings.Contains(got, "Привіт, Сергію.") || !strings.Contains(got, "Як я можу допомогти?") {
		t.Fatalf("expected useful text to remain, got %q", got)
	}
}
