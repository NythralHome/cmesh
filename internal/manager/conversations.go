package manager

import (
	"crypto/rand"
	"encoding/hex"
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

func newConversationID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "conv-unknown"
	}
	return "conv-" + hex.EncodeToString(buf[:])
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
