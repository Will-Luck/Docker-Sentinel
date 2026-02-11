package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// DiscordSettings holds configuration for a Discord webhook notification channel.
type DiscordSettings struct {
	WebhookURL string `json:"webhook_url"`
}

// Discord sends notifications to a Discord webhook.
type Discord struct {
	webhookURL string
	client     *http.Client
}

// NewDiscord creates a Discord notifier for the given webhook URL.
func NewDiscord(webhookURL string) *Discord {
	return &Discord{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider name for logging.
func (d *Discord) Name() string { return "discord" }

// Send posts a notification message to a Discord webhook.
func (d *Discord) Send(ctx context.Context, event Event) error {
	content := formatTitle(event.Type) + "\n" + formatMessage(event)
	body, err := json.Marshal(discordPayload{Content: content})
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("send discord request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord returned %s", resp.Status)
	}
	return nil
}

type discordPayload struct {
	Content string `json:"content"`
}
