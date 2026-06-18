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
