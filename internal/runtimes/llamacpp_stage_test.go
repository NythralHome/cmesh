package runtimes

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestLlamaCPPStageRuntimeProbeRequiresCLI(t *testing.T) {
	probe := NewLlamaCPPStageRuntime("").Probe(context.Background())
	if probe.Ready || probe.CLIReady {
		t.Fatalf("expected probe to be blocked without CLI, got %#v", probe)
	}
	if len(probe.Blockers) == 0 || !strings.Contains(strings.Join(probe.Blockers, " "), "llama-cli path is required") {
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
	if len(probe.RequiredHooks) == 0 || !strings.Contains(strings.Join(probe.Blockers, " "), "does not expose CDIP layer-stage activation hooks") {
		t.Fatalf("expected stage hook blocker, got %#v", probe)
	}
}

func TestLlamaCPPStageRuntimePrepareStageIsNotExecutableYet(t *testing.T) {
	binary := t.TempDir() + "/llama-cli"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := NewLlamaCPPStageRuntime(binary).PrepareStage(context.Background(), StagePrepareRequest{})
	if err == nil || !strings.Contains(err.Error(), "not executable yet") {
		t.Fatalf("expected experimental blocker, got %v", err)
	}
}
