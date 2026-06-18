package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmesh/cmesh/internal/jobs"
	"github.com/cmesh/cmesh/internal/models"
	"github.com/cmesh/cmesh/internal/resources"
	"github.com/cmesh/cmesh/internal/workerstatus"
)

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
	if !strings.Contains(got, "how are you?") {
		t.Fatalf("expected remaining text, got %q", got)
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
