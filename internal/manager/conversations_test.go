package manager

import (
	"strings"
	"testing"

	"github.com/cmesh/cmesh/internal/models"
)

func TestBudgetConversationMessagesKeepsRecentHistoryWithinBudget(t *testing.T) {
	model := models.Model{ID: "tiny", Context: 256}
	messages := []models.ChatMessage{
		{Role: "user", Content: strings.Repeat("old ", 400)},
		{Role: "assistant", Content: strings.Repeat("older ", 400)},
		{Role: "user", Content: "Мене звати Сергій."},
		{Role: "assistant", Content: "Запамʼятав."},
		{Role: "user", Content: "Як мене звати?"},
	}

	got := budgetConversationMessages(model, strings.Repeat("system ", 100), messages, 128)
	if len(got) == 0 {
		t.Fatal("expected budgeted messages")
	}
	if got[len(got)-1].Content != "Як мене звати?" {
		t.Fatalf("expected latest user message to remain, got %#v", got)
	}
	for _, message := range got {
		if strings.Contains(message.Content, "old old") || strings.Contains(message.Content, "older older") {
			t.Fatalf("expected old oversized messages to be dropped, got %#v", got)
		}
	}
}

func TestBudgetConversationMessagesTruncatesOversizedLatestMessage(t *testing.T) {
	model := models.Model{ID: "tiny", Context: 128}
	latest := strings.Repeat("0123456789", 200)

	got := budgetConversationMessages(model, "", []models.ChatMessage{{Role: "user", Content: latest}}, 64)
	if len(got) != 1 {
		t.Fatalf("expected one latest message, got %#v", got)
	}
	if len([]rune(got[0].Content)) >= len([]rune(latest)) {
		t.Fatalf("expected latest message to be truncated, got %d runes", len([]rune(got[0].Content)))
	}
	if !strings.HasSuffix(latest, got[0].Content) {
		t.Fatalf("expected truncation to keep the latest part of the message")
	}
}
