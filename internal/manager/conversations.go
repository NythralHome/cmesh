package manager

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/cmesh/cmesh/internal/models"
)

const maxConversationMessages = 40

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
