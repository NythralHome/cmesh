package models

import (
	"fmt"
	"strings"

	"github.com/cmesh/cmesh/internal/cdip"
)

const (
	JobInstall                = "model.install"
	JobDelete                 = "model.delete"
	JobGenerate               = "model.generate"
	JobGenerateDistributedRPC = "model.generate.distributed_rpc"
	JobGenerateDistributed    = "model.generate.distributed"
	JobGenerateStage          = "model.generate.distributed.stage"
	JobRepair                 = "model.repair"
	JobCleanup                = "model.cleanup"
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

type QualityPreset struct {
	Temperature  string `json:"temperature"`
	MaxTokens    int    `json:"max_tokens"`
	SystemPrompt string `json:"system_prompt"`
}

type InstallInput struct {
	ModelID  string `json:"model_id"`
	CacheDir string `json:"cache_dir,omitempty"`
}

type DeleteInput struct {
	ModelID  string `json:"model_id"`
	CacheDir string `json:"cache_dir,omitempty"`
}

type RepairInput struct {
	ModelID  string `json:"model_id"`
	CacheDir string `json:"cache_dir,omitempty"`
}

type CleanupInput struct {
	Scope string `json:"scope,omitempty"`
}

type GenerateInput struct {
	ModelID        string        `json:"model_id"`
	Prompt         string        `json:"prompt"`
	Messages       []ChatMessage `json:"messages,omitempty"`
	SystemPrompt   string        `json:"system_prompt,omitempty"`
	ConversationID string        `json:"conversation_id,omitempty"`
	MaxTokens      int           `json:"max_tokens,omitempty"`
	Temperature    string        `json:"temperature,omitempty"`
	CacheDir       string        `json:"cache_dir,omitempty"`
}

type DistributedGenerateInput struct {
	ModelID        string                  `json:"model_id"`
	Prompt         string                  `json:"prompt"`
	Messages       []ChatMessage           `json:"messages,omitempty"`
	SystemPrompt   string                  `json:"system_prompt,omitempty"`
	ConversationID string                  `json:"conversation_id,omitempty"`
	MaxTokens      int                     `json:"max_tokens,omitempty"`
	Temperature    string                  `json:"temperature,omitempty"`
	Mode           string                  `json:"mode"`
	Stages         []DistributedStageInput `json:"stages"`
	Shards         []cdip.ModelShard       `json:"shards,omitempty"`
}

type DistributedRPCGenerateInput struct {
	ModelID        string                      `json:"model_id"`
	Prompt         string                      `json:"prompt"`
	Messages       []ChatMessage               `json:"messages,omitempty"`
	SystemPrompt   string                      `json:"system_prompt,omitempty"`
	ConversationID string                      `json:"conversation_id,omitempty"`
	MaxTokens      int                         `json:"max_tokens,omitempty"`
	Temperature    string                      `json:"temperature,omitempty"`
	RPCEndpoints   []string                    `json:"rpc_endpoints"`
	ExecutionPlan  DistributedRPCExecutionPlan `json:"execution_plan,omitempty"`
}

type DistributedRPCExecutionPlan struct {
	ID                  string                  `json:"id,omitempty"`
	Mode                string                  `json:"mode"`
	ModelID             string                  `json:"model_id"`
	CoordinatorNodeID   string                  `json:"coordinator_node_id,omitempty"`
	CoordinatorNodeName string                  `json:"coordinator_node_name,omitempty"`
	RPCEndpoints        []string                `json:"rpc_endpoints"`
	Backends            []DistributedRPCBackend `json:"backends,omitempty"`
	HealthChecked       bool                    `json:"health_checked"`
	PlannedAt           string                  `json:"planned_at,omitempty"`
}

type DistributedRPCBackend struct {
	NodeID       string `json:"node_id"`
	NodeName     string `json:"node_name"`
	Runtime      string `json:"runtime"`
	Endpoint     string `json:"endpoint"`
	HealthStatus string `json:"health_status,omitempty"`
	LatencyMS    int64  `json:"latency_ms,omitempty"`
	Error        string `json:"error,omitempty"`
}

type DistributedStageInput struct {
	Index      int    `json:"index"`
	NodeID     string `json:"node_id"`
	NodeName   string `json:"node_name,omitempty"`
	LayerStart int    `json:"layer_start"`
	LayerEnd   int    `json:"layer_end"`
	Layers     int    `json:"layers"`
}

type DistributedStageJobInput struct {
	ParentJobID      string                `json:"parent_job_id"`
	ModelID          string                `json:"model_id"`
	ConversationID   string                `json:"conversation_id,omitempty"`
	Stage            DistributedStageInput `json:"stage"`
	Shard            cdip.ModelShard       `json:"shard"`
	UpstreamNodeID   string                `json:"upstream_node_id,omitempty"`
	DownstreamNodeID string                `json:"downstream_node_id,omitempty"`
	Prompt           string                `json:"prompt,omitempty"`
	Messages         []ChatMessage         `json:"messages,omitempty"`
	SystemPrompt     string                `json:"system_prompt,omitempty"`
	MaxTokens        int                   `json:"max_tokens,omitempty"`
	Temperature      string                `json:"temperature,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func QualityPresetFor(model Model) QualityPreset {
	base := "You are CMesh's local AI assistant. Continue the conversation using the provided history. Answer the latest user message directly. If the user shared personal details earlier in this conversation, remember and use them. Do not print role names, chat template tokens, or hidden reasoning."
	id := strings.ToLower(model.ID)
	family := strings.ToLower(model.Family)
	preset := QualityPreset{
		Temperature:  "0.6",
		MaxTokens:    512,
		SystemPrompt: base,
	}
	if strings.Contains(id, "deepseek") {
		preset.Temperature = "0.3"
		preset.MaxTokens = 768
		preset.SystemPrompt += " Return only the final answer unless the user explicitly asks for reasoning."
		return preset
	}
	if strings.Contains(id, "coder") {
		preset.Temperature = "0.2"
		preset.MaxTokens = 768
		preset.SystemPrompt += " Prefer precise code, commands, and short explanations."
		return preset
	}
	switch family {
	case "qwen":
		preset.Temperature = "0.5"
		preset.MaxTokens = 768
		preset.SystemPrompt += " Prefer concise, natural answers."
	case "gemma":
		preset.Temperature = "0.7"
		preset.MaxTokens = 512
		preset.SystemPrompt += " Keep answers clear and conversational."
	case "mistral":
		preset.Temperature = "0.4"
		preset.MaxTokens = 768
		preset.SystemPrompt += " Be practical and concise."
	case "phi":
		preset.Temperature = "0.3"
		preset.MaxTokens = 512
		preset.SystemPrompt += " Keep answers short unless more detail is needed."
	case "llama":
		preset.Temperature = "0.6"
		preset.MaxTokens = 512
	}
	return preset
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
	{
		ID:          "phi-3.5-mini-instruct-q4-k-m",
		Name:        "Phi-3.5 Mini Instruct",
		Family:      "Phi",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/Phi-3.5-mini-instruct-GGUF",
		File:        "Phi-3.5-mini-instruct-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/Phi-3.5-mini-instruct-GGUF/resolve/main/Phi-3.5-mini-instruct-Q4_K_M.gguf",
		Parameters:  "3.8B",
		Quant:       "Q4_K_M",
		Context:     4096,
		DiskBytes:   3 * gb,
		MemoryBytes: 5 * gb,
		License:     "MIT",
		Description: "Small instruct model that should be more useful than the tiny smoke-test models.",
	},
	{
		ID:          "gemma-3-4b-it-q4-k-m",
		Name:        "Gemma 3 4B IT",
		Family:      "Gemma",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "ggml-org/gemma-3-4b-it-GGUF",
		File:        "gemma-3-4b-it-Q4_K_M.gguf",
		URL:         "https://huggingface.co/ggml-org/gemma-3-4b-it-GGUF/resolve/main/gemma-3-4b-it-Q4_K_M.gguf",
		Parameters:  "4B",
		Quant:       "Q4_K_M",
		Context:     8192,
		DiskBytes:   4 * gb,
		MemoryBytes: 6 * gb,
		License:     "Gemma",
		Description: "Modern Gemma instruct model for better local chat tests.",
	},
	{
		ID:          "mistral-7b-instruct-v0.3-q4-k-m",
		Name:        "Mistral 7B Instruct v0.3",
		Family:      "Mistral",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/Mistral-7B-Instruct-v0.3-GGUF",
		File:        "Mistral-7B-Instruct-v0.3-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/Mistral-7B-Instruct-v0.3-GGUF/resolve/main/Mistral-7B-Instruct-v0.3-Q4_K_M.gguf",
		Parameters:  "7B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   5 * gb,
		MemoryBytes: 8 * gb,
		License:     "Apache-2.0",
		Description: "Reliable general-purpose 7B instruct baseline.",
	},
	{
		ID:          "qwen2.5-7b-instruct-q4-k-m",
		Name:        "Qwen2.5 7B Instruct",
		Family:      "Qwen",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/Qwen2.5-7B-Instruct-GGUF",
		File:        "Qwen2.5-7B-Instruct-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/Qwen2.5-7B-Instruct-GGUF/resolve/main/Qwen2.5-7B-Instruct-Q4_K_M.gguf",
		Parameters:  "7B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   5 * gb,
		MemoryBytes: 8 * gb,
		License:     "Apache-2.0",
		Description: "Stronger default chat candidate for a 48 GB Mac.",
	},
	{
		ID:          "qwen2.5-coder-7b-instruct-q4-k-m",
		Name:        "Qwen2.5 Coder 7B Instruct",
		Family:      "Qwen",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "Qwen/Qwen2.5-Coder-7B-Instruct-GGUF",
		File:        "qwen2.5-coder-7b-instruct-q4_k_m.gguf",
		URL:         "https://huggingface.co/Qwen/Qwen2.5-Coder-7B-Instruct-GGUF/resolve/main/qwen2.5-coder-7b-instruct-q4_k_m.gguf",
		Parameters:  "7B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   5 * gb,
		MemoryBytes: 8 * gb,
		License:     "Apache-2.0",
		Description: "Code-focused model for testing developer prompts on the cluster.",
	},
	{
		ID:          "gemma-3-12b-it-q4-k-m",
		Name:        "Gemma 3 12B IT",
		Family:      "Gemma",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "ggml-org/gemma-3-12b-it-GGUF",
		File:        "gemma-3-12b-it-Q4_K_M.gguf",
		URL:         "https://huggingface.co/ggml-org/gemma-3-12b-it-GGUF/resolve/main/gemma-3-12b-it-Q4_K_M.gguf",
		Parameters:  "12B",
		Quant:       "Q4_K_M",
		Context:     8192,
		DiskBytes:   8 * gb,
		MemoryBytes: 14 * gb,
		License:     "Gemma",
		Description: "Heavier Gemma option for larger local machines.",
	},
	{
		ID:          "qwen2.5-14b-instruct-q4-k-m",
		Name:        "Qwen2.5 14B Instruct",
		Family:      "Qwen",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/Qwen2.5-14B-Instruct-GGUF",
		File:        "Qwen2.5-14B-Instruct-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/Qwen2.5-14B-Instruct-GGUF/resolve/main/Qwen2.5-14B-Instruct-Q4_K_M.gguf",
		Parameters:  "14B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   10 * gb,
		MemoryBytes: 16 * gb,
		License:     "Apache-2.0",
		Description: "Large local instruct model for high-memory Macs.",
	},
	{
		ID:          "mistral-small-24b-instruct-2501-q4-k-m",
		Name:        "Mistral Small 24B Instruct 2501",
		Family:      "Mistral",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/Mistral-Small-24B-Instruct-2501-GGUF",
		File:        "Mistral-Small-24B-Instruct-2501-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/Mistral-Small-24B-Instruct-2501-GGUF/resolve/main/Mistral-Small-24B-Instruct-2501-Q4_K_M.gguf",
		Parameters:  "24B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   15 * gb,
		MemoryBytes: 26 * gb,
		License:     "Apache-2.0",
		Description: "Heavy general-purpose instruct model for high-memory local workers.",
	},
	{
		ID:          "gemma-3-27b-it-q4-k-m",
		Name:        "Gemma 3 27B IT",
		Family:      "Gemma",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/google_gemma-3-27b-it-GGUF",
		File:        "google_gemma-3-27b-it-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/google_gemma-3-27b-it-GGUF/resolve/main/google_gemma-3-27b-it-Q4_K_M.gguf",
		Parameters:  "27B",
		Quant:       "Q4_K_M",
		Context:     8192,
		DiskBytes:   18 * gb,
		MemoryBytes: 30 * gb,
		License:     "Gemma",
		Description: "Large Gemma instruct model for stronger local reasoning tests.",
	},
	{
		ID:          "qwen2.5-32b-instruct-q4-k-m",
		Name:        "Qwen2.5 32B Instruct",
		Family:      "Qwen",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/Qwen2.5-32B-Instruct-GGUF",
		File:        "Qwen2.5-32B-Instruct-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/Qwen2.5-32B-Instruct-GGUF/resolve/main/Qwen2.5-32B-Instruct-Q4_K_M.gguf",
		Parameters:  "32B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   21 * gb,
		MemoryBytes: 34 * gb,
		License:     "Apache-2.0",
		Description: "Very large Qwen instruct model; recommended upper-end test for 48 GB Macs.",
	},
	{
		ID:          "qwen2.5-coder-32b-instruct-q4-k-m",
		Name:        "Qwen2.5 Coder 32B Instruct",
		Family:      "Qwen",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/Qwen2.5-Coder-32B-Instruct-GGUF",
		File:        "Qwen2.5-Coder-32B-Instruct-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/Qwen2.5-Coder-32B-Instruct-GGUF/resolve/main/Qwen2.5-Coder-32B-Instruct-Q4_K_M.gguf",
		Parameters:  "32B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   21 * gb,
		MemoryBytes: 34 * gb,
		License:     "Apache-2.0",
		Description: "Large code-focused model for serious local developer prompts.",
	},
	{
		ID:          "deepseek-r1-distill-qwen-32b-q4-k-m",
		Name:        "DeepSeek R1 Distill Qwen 32B",
		Family:      "Qwen",
		Runtime:     RuntimeLlamaCPP,
		Repo:        "bartowski/DeepSeek-R1-Distill-Qwen-32B-GGUF",
		File:        "DeepSeek-R1-Distill-Qwen-32B-Q4_K_M.gguf",
		URL:         "https://huggingface.co/bartowski/DeepSeek-R1-Distill-Qwen-32B-GGUF/resolve/main/DeepSeek-R1-Distill-Qwen-32B-Q4_K_M.gguf",
		Parameters:  "32B",
		Quant:       "Q4_K_M",
		Context:     32768,
		DiskBytes:   21 * gb,
		MemoryBytes: 34 * gb,
		License:     "MIT",
		Description: "Experimental reasoning-oriented 32B model; expect slower responses.",
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
