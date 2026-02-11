package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PushoverSettings holds configuration for a Pushover notification channel.
type PushoverSettings struct {
	AppToken string `json:"app_token"`
	UserKey  string `json:"user_key"`
}

// Pushover sends notifications via the Pushover API.
type Pushover struct {
	appToken string
	userKey  string
	client   *http.Client
}

// NewPushover creates a Pushover notifier for the given application token and user key.
func NewPushover(appToken, userKey string) *Pushover {
	return &Pushover{
		appToken: appToken,
		userKey:  userKey,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Name returns the provider name for logging.
func (p *Pushover) Name() string { return "pushover" }

// Send posts a notification message to the Pushover API.
func (p *Pushover) Send(ctx context.Context, event Event) error {
	endpoint := "https://api.pushover.net/1/messages.json"

	form := url.Values{
		"token":   {p.appToken},
		"user":    {p.userKey},
		"title":   {formatTitle(event.Type)},
		"message": {formatMessage(event)},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create pushover request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("send pushover request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pushover returned %s", resp.Status)
	}
	return nil
}
