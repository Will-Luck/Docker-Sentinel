package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SlackSettings holds configuration for a Slack webhook notification channel.
type SlackSettings struct {
	WebhookURL string `json:"webhook_url"`
}

// Slack sends notifications to a Slack incoming webhook.
type Slack struct {
	webhookURL string
	client     *http.Client
}

// NewSlack creates a Slack notifier for the given webhook URL.
func NewSlack(webhookURL string) *Slack {
	return &Slack{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider name for logging.
func (s *Slack) Name() string { return "slack" }

// Send posts a notification message to a Slack webhook.
func (s *Slack) Send(ctx context.Context, event Event) error {
	text := formatTitle(event.Type) + "\n" + formatMessageMarkdown(event)
	body, err := json.Marshal(slackPayload{Text: text})
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("send slack request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned %s", resp.Status)
	}
	return nil
}

type slackPayload struct {
	Text string `json:"text"`
}
