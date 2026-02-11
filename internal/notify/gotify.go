package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GotifySettings holds configuration for a Gotify notification channel.
type GotifySettings struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// Gotify sends notifications to a Gotify server via its REST API.
type Gotify struct {
	url    string
	token  string
	client *http.Client
}

// NewGotify creates a Gotify notifier.
// URL should be the base Gotify server URL (e.g. "http://gotify.example.com").
// Token is the application token used for authentication.
func NewGotify(url, token string) *Gotify {
	return &Gotify{
		url:    strings.TrimRight(url, "/"),
		token:  token,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider name for logging.
func (g *Gotify) Name() string { return "gotify" }

// Send posts a notification message to Gotify.
func (g *Gotify) Send(ctx context.Context, event Event) error {
	body, err := json.Marshal(gotifyMessage{
		Title:    formatTitle(event.Type),
		Message:  formatMessage(event),
		Priority: priority(event.Type),
	})
	if err != nil {
		return fmt.Errorf("marshal gotify payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url+"/message", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create gotify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", g.token)

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("send gotify request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gotify returned %s", resp.Status)
	}
	return nil
}

type gotifyMessage struct {
	Title    string `json:"title"`
	Message  string `json:"message"`
	Priority int    `json:"priority"`
}

// formatTitle produces a human-readable notification title.
func formatTitle(t EventType) string {
	readable := strings.ReplaceAll(string(t), "_", " ")
	// Title-case each word.
	words := strings.Fields(readable)
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return "Sentinel: " + strings.Join(words, " ")
}

// formatMessage builds the notification body from event fields.
func formatMessage(e Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Container: %s\n", e.ContainerName)
	if e.OldImage != "" {
		fmt.Fprintf(&b, "Old image: %s\n", e.OldImage)
	}
	if e.NewImage != "" {
		fmt.Fprintf(&b, "New image: %s\n", e.NewImage)
	}
	if e.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", e.Error)
	}
	return b.String()
}

// priority returns Gotify priority: 8 for failures, 5 for everything else.
func priority(t EventType) int {
	switch t {
	case EventUpdateFailed, EventRollbackFailed:
		return 8
	default:
		return 5
	}
}
