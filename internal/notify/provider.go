package notify

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ProviderType identifies a notification provider backend.
type ProviderType string

const (
	ProviderGotify   ProviderType = "gotify"
	ProviderWebhook  ProviderType = "webhook"
	ProviderSlack    ProviderType = "slack"
	ProviderDiscord  ProviderType = "discord"
	ProviderNtfy     ProviderType = "ntfy"
	ProviderTelegram ProviderType = "telegram"
	ProviderPushover ProviderType = "pushover"
	ProviderSMTP     ProviderType = "smtp"
)

// Channel represents a single notification channel with typed settings.
type Channel struct {
	ID       string          `json:"id"`
	Type     ProviderType    `json:"type"`
	Name     string          `json:"name"`
	Enabled  bool            `json:"enabled"`
	Settings json.RawMessage `json:"settings"`
	Events   []string        `json:"events,omitempty"` // which event types this channel receives; nil/empty = all
}

// GenerateID returns a random 16-character hex string suitable for channel IDs.
func GenerateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

// BuildFilteredNotifier constructs a Notifier from a Channel, wrapping it with
// an event type filter if the channel has a non-empty Events list.
// Channels with no Events filter receive all event types (backwards compatible).
func BuildFilteredNotifier(ch Channel) (Notifier, error) {
	n, err := BuildNotifier(ch)
	if err != nil {
		return nil, err
	}
	if len(ch.Events) == 0 {
		return n, nil
	}
	return newFilteredNotifier(n, ch.Events), nil
}

// BuildNotifier constructs a Notifier from a Channel's type and settings.
func BuildNotifier(ch Channel) (Notifier, error) {
	switch ch.Type {
	case ProviderGotify:
		var s GotifySettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal gotify settings: %w", err)
		}
		return NewGotify(s.URL, s.Token), nil

	case ProviderWebhook:
		var s WebhookSettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal webhook settings: %w", err)
		}
		return NewWebhook(s.URL, s.Headers), nil

	case ProviderSlack:
		var s SlackSettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal slack settings: %w", err)
		}
		return NewSlack(s.WebhookURL), nil

	case ProviderDiscord:
		var s DiscordSettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal discord settings: %w", err)
		}
		return NewDiscord(s.WebhookURL), nil

	case ProviderNtfy:
		var s NtfySettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal ntfy settings: %w", err)
		}
		return NewNtfy(s.Server, s.Topic, s.Priority, s.Token, s.Username, s.Password), nil

	case ProviderTelegram:
		var s TelegramSettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal telegram settings: %w", err)
		}
		return NewTelegram(s.BotToken, s.ChatID), nil

	case ProviderPushover:
		var s PushoverSettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal pushover settings: %w", err)
		}
		return NewPushover(s.AppToken, s.UserKey), nil

	case ProviderSMTP:
		var s SMTPSettings
		if err := json.Unmarshal(ch.Settings, &s); err != nil {
			return nil, fmt.Errorf("unmarshal smtp settings: %w", err)
		}
		return NewSMTP(s.Host, s.Port, s.From, s.To, s.Username, s.Password, s.TLS), nil

	default:
		return nil, fmt.Errorf("unknown provider type: %q", ch.Type)
	}
}

// MaskSecrets returns a copy of the channel with sensitive fields partially redacted.
// The original channel is not modified.
func MaskSecrets(ch Channel) Channel {
	masked := ch
	switch ch.Type {
	case ProviderGotify:
		masked.Settings = maskGotifySecrets(ch.Settings)
	case ProviderWebhook:
		masked.Settings = maskWebhookSecrets(ch.Settings)
	case ProviderSlack:
		masked.Settings = maskWebhookURLSecret(ch.Settings, "webhook_url")
	case ProviderDiscord:
		masked.Settings = maskWebhookURLSecret(ch.Settings, "webhook_url")
	case ProviderTelegram:
		masked.Settings = maskStringField(ch.Settings, "bot_token")
	case ProviderPushover:
		masked.Settings = maskStringField(ch.Settings, "app_token")
	case ProviderNtfy:
		masked.Settings = maskNtfySecrets(ch.Settings)
	case ProviderSMTP:
		masked.Settings = maskStringField(ch.Settings, "password")
	}
	return masked
}

// maskToken keeps the first 4 characters and replaces the rest with "****".
// Returns "****" if the value is shorter than 5 characters.
func maskToken(s string) string {
	if len(s) < 5 {
		return "****"
	}
	return s[:4] + "****"
}

// maskURL keeps the protocol and host, replacing the path with /****.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "****"
	}
	return u.Scheme + "://" + u.Host + "/****"
}

func maskGotifySecrets(settings json.RawMessage) json.RawMessage {
	var s GotifySettings
	if json.Unmarshal(settings, &s) != nil {
		return settings
	}
	s.Token = maskToken(s.Token)
	out, _ := json.Marshal(s)
	return out
}

func maskWebhookSecrets(settings json.RawMessage) json.RawMessage {
	var s WebhookSettings
	if json.Unmarshal(settings, &s) != nil {
		return settings
	}
	sensitiveWords := []string{"token", "bearer", "key", "secret"}
	for k, v := range s.Headers {
		lower := strings.ToLower(k + " " + v)
		for _, word := range sensitiveWords {
			if strings.Contains(lower, word) {
				s.Headers[k] = maskToken(v)
				break
			}
		}
	}
	out, _ := json.Marshal(s)
	return out
}

func maskWebhookURLSecret(settings json.RawMessage, field string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(settings, &m) != nil {
		return settings
	}
	raw, ok := m[field]
	if !ok {
		return settings
	}
	var val string
	if json.Unmarshal(raw, &val) != nil {
		return settings
	}
	masked, _ := json.Marshal(maskURL(val))
	m[field] = masked
	out, _ := json.Marshal(m)
	return out
}

func maskStringField(settings json.RawMessage, field string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(settings, &m) != nil {
		return settings
	}
	raw, ok := m[field]
	if !ok {
		return settings
	}
	var val string
	if json.Unmarshal(raw, &val) != nil {
		return settings
	}
	masked, _ := json.Marshal(maskToken(val))
	m[field] = masked
	out, _ := json.Marshal(m)
	return out
}

func maskNtfySecrets(settings json.RawMessage) json.RawMessage {
	var s NtfySettings
	if json.Unmarshal(settings, &s) != nil {
		return settings
	}
	if s.Token != "" {
		s.Token = maskToken(s.Token)
	}
	if s.Password != "" {
		s.Password = maskToken(s.Password)
	}
	out, _ := json.Marshal(s)
	return out
}
