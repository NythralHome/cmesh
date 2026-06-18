package manager

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/cmesh/cmesh/internal/models"
)

const (
	maxConversationMessages      = 40
	defaultPromptContextTokens   = 4096
	promptContextOverheadTokens  = 128
	minPromptHistoryBudgetTokens = 256
)

type Conversation struct {
	ID           string               `json:"id"`
	ModelID      string               `json:"model_id"`
	NodeID       string               `json:"node_id"`
	SystemPrompt string               `json:"system_prompt"`
	Messages     []models.ChatMessage `json:"messages"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

type Memory struct {
	ID             string    `json:"id"`
	ModelID        string    `json:"model_id"`
	Key            string    `json:"key"`
	Value          string    `json:"value"`
	Source         string    `json:"source"`
	ConversationID string    `json:"conversation_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type PromptContextPreview struct {
	ModelID               string               `json:"model_id"`
	ContextTokens         int                  `json:"context_tokens"`
	OutputReserveTokens   int                  `json:"output_reserve_tokens"`
	SystemPromptTokens    int                  `json:"system_prompt_tokens"`
	HistoryBudgetTokens   int                  `json:"history_budget_tokens"`
	IncludedMessageTokens int                  `json:"included_message_tokens"`
	TotalMessages         int                  `json:"total_messages"`
	IncludedMessages      []models.ChatMessage `json:"included_messages"`
	DroppedMessages       int                  `json:"dropped_messages"`
	EffectiveSystemPrompt string               `json:"effective_system_prompt"`
}

func newConversationID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "conv-unknown"
	}
	return "conv-" + hex.EncodeToString(buf[:])
}

func newMemoryID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "mem-unknown"
	}
	return "mem-" + hex.EncodeToString(buf[:])
}

func normalizeChatRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "assistant":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return "user"
	}
}

func normalizeChatMessage(message models.ChatMessage) models.ChatMessage {
	return models.ChatMessage{
		Role:    normalizeChatRole(message.Role),
		Content: strings.TrimSpace(message.Content),
	}
}

func trimConversationMessages(messages []models.ChatMessage) []models.ChatMessage {
	filtered := make([]models.ChatMessage, 0, len(messages))
	for _, message := range messages {
		message = normalizeChatMessage(message)
		if message.Content == "" {
			continue
		}
		filtered = append(filtered, message)
	}
	if len(filtered) <= maxConversationMessages {
		return filtered
	}
	return append([]models.ChatMessage(nil), filtered[len(filtered)-maxConversationMessages:]...)
}

func budgetConversationMessages(model models.Model, systemPrompt string, messages []models.ChatMessage, maxTokens int) []models.ChatMessage {
	return promptContextPreview(model, systemPrompt, messages, maxTokens).IncludedMessages
}

func promptContextPreview(model models.Model, systemPrompt string, messages []models.ChatMessage, maxTokens int) PromptContextPreview {
	filtered := trimConversationMessages(messages)
	contextTokens, outputReserve, historyBudget := promptContextBudget(model, systemPrompt, maxTokens)
	preview := PromptContextPreview{
		ModelID:               model.ID,
		ContextTokens:         contextTokens,
		OutputReserveTokens:   outputReserve,
		SystemPromptTokens:    estimateTextTokens(systemPrompt),
		HistoryBudgetTokens:   historyBudget,
		TotalMessages:         len(filtered),
		EffectiveSystemPrompt: systemPrompt,
	}
	if len(filtered) == 0 {
		return preview
	}

	selected := make([]models.ChatMessage, 0, len(filtered))
	used := 0
	for i := len(filtered) - 1; i >= 0; i-- {
		message := filtered[i]
		cost := estimateMessageTokens(message)
		if len(selected) > 0 && used+cost > historyBudget {
			continue
		}
		if len(selected) == 0 && cost > historyBudget {
			message = truncateChatMessage(message, historyBudget)
			cost = estimateMessageTokens(message)
		}
		selected = append(selected, message)
		used += cost
	}

	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	preview.IncludedMessages = selected
	preview.IncludedMessageTokens = used
	preview.DroppedMessages = len(filtered) - len(selected)
	return preview
}

func promptHistoryBudgetTokens(model models.Model, systemPrompt string, maxTokens int) int {
	_, _, budget := promptContextBudget(model, systemPrompt, maxTokens)
	return budget
}

func promptContextBudget(model models.Model, systemPrompt string, maxTokens int) (int, int, int) {
	context := model.Context
	if context <= 0 || context > defaultPromptContextTokens {
		context = defaultPromptContextTokens
	}
	outputReserve := maxTokens
	if outputReserve <= 0 {
		outputReserve = 256
	}
	if outputReserve > 2048 {
		outputReserve = 2048
	}
	budget := context - outputReserve - promptContextOverheadTokens - estimateTextTokens(systemPrompt)
	if budget < minPromptHistoryBudgetTokens {
		budget = minPromptHistoryBudgetTokens
	}
	return context, outputReserve, budget
}

func estimateMessageTokens(message models.ChatMessage) int {
	return 4 + estimateTextTokens(message.Role) + estimateTextTokens(message.Content)
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := len([]rune(text))
	tokens := (runes + 3) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func truncateChatMessage(message models.ChatMessage, tokenBudget int) models.ChatMessage {
	message = normalizeChatMessage(message)
	if tokenBudget <= 0 {
		message.Content = ""
		return message
	}
	allowedRunes := tokenBudget * 4
	runes := []rune(message.Content)
	if len(runes) <= allowedRunes {
		return message
	}
	message.Content = strings.TrimSpace(string(runes[len(runes)-allowedRunes:]))
	return message
}

func cloneConversations(in map[string]Conversation) map[string]Conversation {
	out := make(map[string]Conversation, len(in))
	for key, value := range in {
		value.Messages = append([]models.ChatMessage(nil), value.Messages...)
		out[key] = value
	}
	return out
}

func cloneMemories(in map[string]Memory) map[string]Memory {
	out := make(map[string]Memory, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func memoryContext(modelID string, memories []Memory) string {
	filtered := make([]string, 0, len(memories))
	for _, memory := range memories {
		if memory.ModelID != modelID || strings.TrimSpace(memory.Value) == "" {
			continue
		}
		filtered = append(filtered, "- "+memory.Key+": "+memory.Value)
	}
	if len(filtered) == 0 {
		return ""
	}
	return "Known memory for this model:\n" + strings.Join(filtered, "\n")
}

func extractMemories(modelID string, conversationID string, content string, now time.Time) []Memory {
	content = strings.TrimSpace(content)
	if modelID == "" || content == "" {
		return nil
	}
	candidates := []struct {
		key     string
		pattern *regexp.Regexp
	}{
		{key: "user.name", pattern: regexp.MustCompile(`(?i)\bmy name is\s+([^.!?\n,]{1,80})`)},
		{key: "user.name", pattern: regexp.MustCompile(`(?i)\bi am called\s+([^.!?\n,]{1,80})`)},
		{key: "user.name", pattern: regexp.MustCompile(`(?i)мене звати\s+([^.!?\n,]{1,80})`)},
		{key: "user.name", pattern: regexp.MustCompile(`(?i)моє ім['’]?я\s+([^.!?\n,]{1,80})`)},
		{key: "user.preference", pattern: regexp.MustCompile(`(?i)\bi like\s+([^.!?\n]{1,120})`)},
		{key: "user.preference", pattern: regexp.MustCompile(`(?i)мені подобається\s+([^.!?\n]{1,120})`)},
		{key: "user.preference", pattern: regexp.MustCompile(`(?i)я люблю\s+([^.!?\n]{1,120})`)},
	}
	out := make([]Memory, 0, 2)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		match := candidate.pattern.FindStringSubmatch(content)
		if len(match) < 2 {
			continue
		}
		value := strings.Trim(strings.TrimSpace(match[1]), ` "'“”«»`)
		if value == "" {
			continue
		}
		lookup := candidate.key + "\x00" + strings.ToLower(value)
		if seen[lookup] {
			continue
		}
		seen[lookup] = true
		out = append(out, Memory{
			ID:             newMemoryID(),
			ModelID:        modelID,
			Key:            candidate.key,
			Value:          value,
			Source:         content,
			ConversationID: conversationID,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}
	return out
}
