package models

import "fmt"

const (
	JobInstall  = "model.install"
	JobDelete   = "model.delete"
	JobGenerate = "model.generate"
)

type Runtime string

const RuntimeLlamaCPP Runtime = "llama.cpp"

type Model struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Family      string  `json:"family"`
	Runtime     Runtime `json:"runtime"`
	Repo        string  `json:"repo"`
	File        string  `json:"file"`
	URL         string  `json:"url"`
	Parameters  string  `json:"parameters"`
	Quant       string  `json:"quant"`
	Context     int     `json:"context"`
	DiskBytes   uint64  `json:"disk_bytes"`
	MemoryBytes uint64  `json:"memory_bytes"`
	VRAMBytes   uint64  `json:"vram_bytes,omitempty"`
	License     string  `json:"license"`
	Description string  `json:"description"`
}

type InstallInput struct {
	ModelID  string `json:"model_id"`
	CacheDir string `json:"cache_dir,omitempty"`
}

type DeleteInput struct {
	ModelID  string `json:"model_id"`
	CacheDir string `json:"cache_dir,omitempty"`
}

type GenerateInput struct {
	ModelID     string `json:"model_id"`
	Prompt      string `json:"prompt"`
	MaxTokens   int    `json:"max_tokens,omitempty"`
	Temperature string `json:"temperature,omitempty"`
	CacheDir    string `json:"cache_dir,omitempty"`
}

const gb = 1024 * 1024 * 1024

var catalog = []Model{
	{
		ID:          "qwen2.5-0.5b-instruct-q4-k-m",
		Name:        "Qwen2.5 0.5B Instruct",
		Family:      "Qwen",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "Qwen/Qwen2.5-0.5B-Instruct-GGUF",
		File:        "qwen2.5-0.5b-instruct-q4_k_m.gguf",
		URL:         "https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct-GGUF/resolve/main/qwen2.5-0.5b-instruct-q4_k_m.gguf",
		Parameters:  "0.5B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   1 * gb,
		MemoryBytes: 2 * gb,
		License:     "Apache-2.0",
		Description: "Small default chat model for first cluster inference tests.",
	},
	{
		ID:          "tinyllama-1.1b-chat-q4-k-m",
		Name:        "TinyLlama 1.1B Chat",
		Family:      "Llama",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF",
		File:        "tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf",
		URL:         "https://huggingface.co/TheBloke/TinyLlama-1.1B-Chat-v1.0-GGUF/resolve/main/tinyllama-1.1b-chat-v1.0.Q4_K_M.gguf",
		Parameters:  "1.1B",
		Quant:       "Q4_K_M",
		Context:     2048,
		DiskBytes:   2 * gb,
		MemoryBytes: 3 * gb,
		License:     "Apache-2.0",
		Description: "Compact chat model with a tiny disk footprint.",
	},
	{
		ID:          "gemma-2b-q4-k-m",
		Name:        "Gemma 2B",
		Family:      "Gemma",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "MaziyarPanahi/gemma-2b-GGUF",
		File:        "gemma-2b.Q4_K_M.gguf",
		URL:         "https://huggingface.co/MaziyarPanahi/gemma-2b-GGUF/resolve/main/gemma-2b.Q4_K_M.gguf",
		Parameters:  "2B",
		Quant:       "Q4_K_M",
		Context:     8192,
		DiskBytes:   3 * gb,
		MemoryBytes: 5 * gb,
		License:     "Gemma",
		Description: "Larger first-run option for stronger local Macs.",
	},
}

func Catalog() []Model {
	out := make([]Model, len(catalog))
	copy(out, catalog)
	return out
}

func Find(id string) (Model, bool) {
	for _, model := range catalog {
		if model.ID == id {
			return model, true
		}
	}
	return Model{}, false
}

func MustFind(id string) (Model, error) {
	model, ok := Find(id)
	if !ok {
		return Model{}, fmt.Errorf("unknown model %q", id)
	}
	return model, nil
}
