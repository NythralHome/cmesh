package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	if !strings.Contains(got, "how are you?") {
		t.Fatalf("expected remaining text, got %q", got)
	}
}

func TestModelPromptUsesQwenChatTemplate(t *testing.T) {
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	got := modelPrompt(model, "hello")
	if !strings.Contains(got, "<|im_start|>user\nhello<|im_end|>") {
		t.Fatalf("expected qwen user template, got %q", got)
	}
	if !strings.HasSuffix(got, "<|im_start|>assistant\n") {
		t.Fatalf("expected assistant prefix, got %q", got)
	}
}

func TestModelContextSizeCapsLargeCatalogContext(t *testing.T) {
	model, err := models.MustFind("qwen2.5-0.5b-instruct-q4-k-m")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CMESH_MODEL_CONTEXT_SIZE", "")
	if got := modelContextSize(model); got != 2048 {
		t.Fatalf("expected context cap 2048, got %d", got)
	}
}
