package main

import (
	"encoding/json"
	"testing"

	"github.com/cmesh/cmesh/internal/jobs"
)

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
