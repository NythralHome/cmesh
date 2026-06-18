package runtimes

import (
	"context"
	"fmt"
	"os"
	"strings"
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

func NewLlamaCPPStageRuntime(binaryPath string) LlamaCPPStageRuntime {
	return LlamaCPPStageRuntime{BinaryPath: strings.TrimSpace(binaryPath)}
}

func (r LlamaCPPStageRuntime) Probe(ctx context.Context) StageRuntimeProbe {
	probe := StageRuntimeProbe{
		Name:    LlamaCPPStageRuntimeName,
		Runtime: LlamaCPPName,
		Ready:   false,
		RequiredHooks: []string{
			"load logical layer range",
			"accept upstream hidden-state activation",
			"emit downstream hidden-state activation",
			"persist per-stage KV/cache state",
			"emit final token deltas from terminal stage",
		},
	}
	if err := ctx.Err(); err != nil {
		probe.Blockers = append(probe.Blockers, err.Error())
		return probe
	}
	if r.BinaryPath == "" {
		probe.Blockers = append(probe.Blockers, "llama-cli path is required")
		return probe
	}
	probe.BinaryPath = r.BinaryPath
	info, err := os.Stat(r.BinaryPath)
	if err != nil {
		probe.Blockers = append(probe.Blockers, "llama-cli is not accessible: "+err.Error())
		return probe
	}
	if info.IsDir() {
		probe.Blockers = append(probe.Blockers, "llama-cli path points to a directory")
		return probe
	}
	probe.CLIReady = true
	probe.Blockers = append(probe.Blockers, "public llama-cli can run full-model inference, but does not expose CDIP layer-stage activation hooks")
	return probe
}

func (r LlamaCPPStageRuntime) PrepareStage(ctx context.Context, req StagePrepareRequest) (StagePrepareResult, error) {
	probe := r.Probe(ctx)
	if !probe.CLIReady {
		return StagePrepareResult{}, fmt.Errorf("llama.cpp stage runtime is not ready: %s", strings.Join(probe.Blockers, "; "))
	}
	return StagePrepareResult{}, fmt.Errorf("llama.cpp stage runtime is experimental and not executable yet: %s", strings.Join(probe.Blockers, "; "))
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

func llamaCPPStageUnsupported(ctx context.Context, runtime LlamaCPPStageRuntime) error {
	probe := runtime.Probe(ctx)
	if !probe.CLIReady {
		return fmt.Errorf("llama.cpp stage runtime is not ready: %s", strings.Join(probe.Blockers, "; "))
	}
	return fmt.Errorf("llama.cpp stage runtime is experimental and not executable yet: %s", strings.Join(probe.Blockers, "; "))
}
