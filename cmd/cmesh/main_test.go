package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
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
