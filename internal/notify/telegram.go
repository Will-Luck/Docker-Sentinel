package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// TelegramSettings holds configuration for a Telegram bot notification channel.
type TelegramSettings struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

// Telegram sends notifications via the Telegram Bot API.
type Telegram struct {
	botToken string
	chatID   string
	client   *http.Client
}

// NewTelegram creates a Telegram notifier for the given bot token and chat ID.
func NewTelegram(botToken, chatID string) *Telegram {
	return &Telegram{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider name for logging.
func (t *Telegram) Name() string { return "telegram" }

// Send posts a notification message via the Telegram Bot API.
func (t *Telegram) Send(ctx context.Context, event Event) error {
	text := formatTitle(event.Type) + "\n" + formatMessage(event)
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	body, err := json.Marshal(telegramPayload{
		ChatID: t.chatID,
		Text:   text,
	})
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telegram returned %s", resp.Status)
	}
	return nil
}

type telegramPayload struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}
