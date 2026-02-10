package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Webhook sends the full Event as JSON to a configurable URL.
type Webhook struct {
	url     string
	headers map[string]string
	client  *http.Client
}

// NewWebhook creates a generic webhook notifier.
// Custom headers (e.g. Authorization) are sent with every request.
func NewWebhook(url string, headers map[string]string) *Webhook {
	return &Webhook{
		url:     url,
		headers: headers,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider name for logging.
func (w *Webhook) Name() string { return "webhook" }

// Send posts the event as JSON to the configured URL.
func (w *Webhook) Send(ctx context.Context, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %s", resp.Status)
	}
	return nil
}
