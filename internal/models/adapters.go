package models

import "strings"

const (
	AdapterQwen25Instruct = "qwen2.5-instruct"
	AdapterQwen25Coder    = "qwen2.5-coder-instruct"
	AdapterDeepSeekQwen   = "deepseek-r1-distill-qwen"
)

type AdapterSpec struct {
	ID                   string   `json:"id"`
	Family               string   `json:"family"`
	PromptTemplate       string   `json:"prompt_template"`
	RuntimeArchitectures []string `json:"runtime_architectures"`
	UpdatePolicy         string   `json:"update_policy"`
	Compatibility        string   `json:"compatibility"`
	ValidationPrompt     string   `json:"validation_prompt"`
}

var adapterSpecs = map[string]AdapterSpec{
	AdapterQwen25Instruct: {
		ID:                   AdapterQwen25Instruct,
		Family:               "Qwen",
		PromptTemplate:       "qwen2.5-chatml",
		RuntimeArchitectures: []string{"qwen2", "qwen2moe"},
		UpdatePolicy:         "signal on upstream repo SHA change; reuse adapter for Qwen2.5 patch/minor GGUF refreshes unless tokenizer/chat template or architecture metadata changes",
		Compatibility:        "Qwen2.5 instruct GGUF models using ChatML and llama.cpp qwen2 architecture hooks",
		ValidationPrompt:     "You are running inside CMesh. Answer in one concise Ukrainian sentence: what is CMesh testing right now?",
	},
	AdapterQwen25Coder: {
		ID:                   AdapterQwen25Coder,
		Family:               "Qwen",
		PromptTemplate:       "qwen2.5-chatml",
		RuntimeArchitectures: []string{"qwen2"},
		UpdatePolicy:         "signal on upstream repo SHA change; keep adapter for Coder patch/minor refreshes unless tokenizer/chat template, special tokens, or architecture changes",
		Compatibility:        "Qwen2.5 Coder instruct GGUF models using ChatML and llama.cpp qwen2 architecture hooks",
		ValidationPrompt:     "Write a tiny Go function named Add that returns the sum of two integers. Return only code.",
	},
	AdapterDeepSeekQwen: {
		ID:                   AdapterDeepSeekQwen,
		Family:               "Qwen",
		PromptTemplate:       "deepseek-r1-distill-qwen-chatml",
		RuntimeArchitectures: []string{"qwen2"},
		UpdatePolicy:         "signal on upstream repo SHA change; require adapter review for major DeepSeek or tokenizer/template changes",
		Compatibility:        "DeepSeek R1 distill Qwen GGUF models with Qwen-compatible llama.cpp architecture and reasoning-output guardrails",
		ValidationPrompt:     "Answer in one short sentence: what does a distributed AI cluster do?",
	},
}

func AdapterForModel(model Model) (AdapterSpec, bool) {
	if model.Adapter != "" {
		spec, ok := adapterSpecs[model.Adapter]
		return spec, ok
	}
	id := strings.ToLower(model.ID)
	switch {
	case strings.Contains(id, "deepseek") && strings.Contains(id, "qwen"):
		spec, ok := adapterSpecs[AdapterDeepSeekQwen]
		return spec, ok
	case strings.Contains(id, "qwen2.5-coder"):
		spec, ok := adapterSpecs[AdapterQwen25Coder]
		return spec, ok
	case strings.Contains(id, "qwen2.5"):
		spec, ok := adapterSpecs[AdapterQwen25Instruct]
		return spec, ok
	default:
		return AdapterSpec{}, false
	}
}

func AdapterSpecs() []AdapterSpec {
	out := make([]AdapterSpec, 0, len(adapterSpecs))
	for _, spec := range adapterSpecs {
		out = append(out, spec)
	}
	return out
}

func QwenCatalog() []Model {
	var out []Model
	for _, model := range catalog {
		if strings.EqualFold(model.Family, "Qwen") {
			out = append(out, model)
		}
	}
	return out
}
