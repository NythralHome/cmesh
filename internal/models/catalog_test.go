package models

import (
	"strings"
	"testing"
)

func TestCatalogEntriesAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, model := range Catalog() {
		if model.ID == "" {
			t.Fatal("model ID is required")
		}
		if seen[model.ID] {
			t.Fatalf("duplicate model ID %q", model.ID)
		}
		seen[model.ID] = true
		if model.Name == "" || model.Family == "" || model.Repo == "" || model.File == "" {
			t.Fatalf("model %q has incomplete metadata: %#v", model.ID, model)
		}
		if model.Runtime != RuntimeLlamaCPP {
			t.Fatalf("model %q has unsupported runtime %q", model.ID, model.Runtime)
		}
		if !strings.HasPrefix(model.URL, "https://huggingface.co/") || !strings.Contains(model.URL, "/resolve/main/") {
			t.Fatalf("model %q has invalid URL %q", model.ID, model.URL)
		}
		if model.DiskBytes == 0 || model.MemoryBytes == 0 {
			t.Fatalf("model %q must declare disk and memory requirements", model.ID)
		}
		preset := QualityPresetFor(model)
		if preset.Temperature == "" || preset.MaxTokens <= 0 || preset.SystemPrompt == "" {
			t.Fatalf("model %q has invalid quality preset: %#v", model.ID, preset)
		}
	}
}

func TestQualityPresetSpecializesCoderAndDeepSeek(t *testing.T) {
	coder := QualityPresetFor(Model{ID: "qwen2.5-coder-7b-instruct-q4-k-m", Family: "Qwen"})
	if coder.Temperature != "0.2" || !strings.Contains(coder.SystemPrompt, "precise code") {
		t.Fatalf("expected coder preset, got %#v", coder)
	}

	deepseek := QualityPresetFor(Model{ID: "deepseek-r1-distill-qwen-32b-q4-k-m", Family: "Qwen"})
	if deepseek.Temperature != "0.3" || !strings.Contains(deepseek.SystemPrompt, "final answer") {
		t.Fatalf("expected deepseek preset, got %#v", deepseek)
	}
}

func TestQwenCatalogDeclaresAdaptersAndPinnedSources(t *testing.T) {
	qwen := QwenCatalog()
	if len(qwen) < 8 {
		t.Fatalf("expected qwen validation catalog to include 1.5B, 3B, and existing larger Qwen models, got %d", len(qwen))
	}

	seen := map[string]bool{}
	for _, model := range qwen {
		seen[model.ID] = true
		if model.Adapter == "" {
			t.Fatalf("qwen model %q must declare adapter", model.ID)
		}
		if _, ok := AdapterForModel(model); !ok {
			t.Fatalf("qwen model %q references unknown adapter %q", model.ID, model.Adapter)
		}
		if model.RepoSHA == "" {
			t.Fatalf("qwen model %q must pin upstream repo sha for update detection", model.ID)
		}
		if model.Layers <= 0 {
			t.Fatalf("qwen model %q must declare layer count for distributed planning", model.ID)
		}
	}

	for _, id := range []string{
		"qwen2.5-1.5b-instruct-q4-k-m",
		"qwen2.5-3b-instruct-q4-k-m",
	} {
		if !seen[id] {
			t.Fatalf("expected Qwen validation target %q in catalog", id)
		}
	}
}

func TestLinuxProductionCatalogDeclaresSlicedModel(t *testing.T) {
	production := LinuxProductionCatalog()
	if len(production) != 1 {
		t.Fatalf("expected exactly one Linux production model for this release, got %d", len(production))
	}

	model := production[0]
	if model.ID != "qwen2.5-14b-instruct-q4-k-m" {
		t.Fatalf("unexpected production model %q", model.ID)
	}
	if model.SHA256 != "d989c91de35f32c18bdb8bec96a4b9fff2c3e5bca066503c63a5ca54dd537a4b" {
		t.Fatalf("missing or changed production model checksum: %q", model.SHA256)
	}
	if model.Production == nil {
		t.Fatal("production model must declare production support metadata")
	}
	if !model.Production.SlicedExecution || model.Production.Layers != 48 {
		t.Fatalf("production model must declare 48-layer sliced execution metadata: %#v", model.Production)
	}
	if model.Production.MinStages != 3 || model.Production.RecommendedStages != 3 {
		t.Fatalf("expected 3-stage production support, got %#v", model.Production)
	}
	if model.Production.MinWorkerMemoryBytes < 6*gb || model.Production.PlacementPolicy != "memory_disk_weighted_layers" {
		t.Fatalf("production placement metadata is not conservative enough: %#v", model.Production)
	}
	if !strings.Contains(model.Production.Evidence, "cmesh-cdip-real-gguf-e2e") {
		t.Fatalf("production model must reference sliced E2E evidence, got %q", model.Production.Evidence)
	}
}
