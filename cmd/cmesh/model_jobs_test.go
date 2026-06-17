package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cmesh/cmesh/internal/models"
)

func TestExecuteModelDeleteJobRemovesModelFileAndEmptyDirectory(t *testing.T) {
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
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected model file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("expected empty model directory to be removed, stat err=%v", err)
	}
}
