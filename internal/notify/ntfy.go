package notify

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// NtfySettings holds configuration for an ntfy notification channel.
type NtfySettings struct {
	Server   string `json:"server"`
	Topic    string `json:"topic"`
	Priority int    `json:"priority"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// Ntfy sends notifications to an ntfy server.
type Ntfy struct {
	server   string
	topic    string
	priority int
	token    string
	username string
	password string
	client   *http.Client
}

// NewNtfy creates an ntfy notifier.
// Server should be the base URL (e.g. "https://ntfy.sh").
// Priority maps to ntfy levels: 1=min, 2=low, 3=default, 4=high, 5=urgent.
func NewNtfy(server, topic string, priority int, token, username, password string) *Ntfy {
	return &Ntfy{
		server:   strings.TrimRight(server, "/"),
		topic:    topic,
		priority: priority,
		token:    token,
		username: username,
		password: password,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider name for logging.
func (n *Ntfy) Name() string { return "ntfy" }

// Send posts a notification message to the ntfy topic.
func (n *Ntfy) Send(ctx context.Context, event Event) error {
	endpoint := n.server + "/" + n.topic
	message := formatMessage(event)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(message))
	if err != nil {
		return fmt.Errorf("create ntfy request: %w", err)
	}
	if n.token != "" {
		req.Header.Set("Authorization", "Bearer "+n.token)
	} else if n.username != "" {
		req.SetBasicAuth(n.username, n.password)
	}
	req.Header.Set("X-Title", formatTitle(event.Type))
	req.Header.Set("X-Priority", strconv.Itoa(n.priority))

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send ntfy request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy returned %s", resp.Status)
	}
	return nil
}
