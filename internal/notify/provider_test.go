package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- GenerateID tests ---

func TestGenerateIDFormat(t *testing.T) {
	id := GenerateID()
	if len(id) != 16 {
		t.Errorf("GenerateID() length = %d, want 16", len(id))
	}
	// Should be valid hex.
	for _, c := range id {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("GenerateID() contains non-hex char %q in %q", string(c), id)
		}
	}
}

func TestGenerateIDUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for range 100 {
		id := GenerateID()
		if _, exists := seen[id]; exists {
			t.Fatalf("GenerateID() produced duplicate: %q", id)
		}
		seen[id] = struct{}{}
	}
}

// --- MaskSecrets tests ---

func TestMaskSecrets(t *testing.T) {
	tests := []struct {
		name       string
		channel    Channel
		checkField string // JSON field to check in masked output
		wantValue  string // expected masked value (empty = just check it changed)
	}{
		{
			name: "gotify masks token",
			channel: Channel{
				Type:     ProviderGotify,
				Settings: mustJSON(GotifySettings{URL: "http://gotify.example.com", Token: "super-secret-token"}),
			},
			checkField: "token",
			wantValue:  "supe****",
		},
		{
			name: "gotify preserves URL",
			channel: Channel{
				Type:     ProviderGotify,
				Settings: mustJSON(GotifySettings{URL: "http://gotify.example.com", Token: "super-secret-token"}),
			},
			checkField: "url",
			wantValue:  "http://gotify.example.com",
		},
		{
			name: "gotify short token fully masked",
			channel: Channel{
				Type:     ProviderGotify,
				Settings: mustJSON(GotifySettings{URL: "http://g.com", Token: "abc"}),
			},
			checkField: "token",
			wantValue:  "****",
		},
		{
			name: "webhook masks Authorization header",
			channel: Channel{
				Type: ProviderWebhook,
				Settings: mustJSON(WebhookSettings{
					URL:     "http://example.com/hook",
					Headers: map[string]string{"Authorization": "Bearer my-long-token"},
				}),
			},
		},
		{
			name: "slack masks webhook URL",
			channel: Channel{
				Type:     ProviderSlack,
				Settings: mustJSON(SlackSettings{WebhookURL: "https://hooks.slack.com/services/T00/B00/xxx"}),
			},
			checkField: "webhook_url",
			wantValue:  "https://hooks.slack.com/****",
		},
		{
			name: "discord masks webhook URL",
			channel: Channel{
				Type:     ProviderDiscord,
				Settings: mustJSON(DiscordSettings{WebhookURL: "https://discord.com/api/webhooks/123/secret-token"}),
			},
			checkField: "webhook_url",
			wantValue:  "https://discord.com/****",
		},
		{
			name: "telegram masks bot token",
			channel: Channel{
				Type:     ProviderTelegram,
				Settings: mustJSON(TelegramSettings{BotToken: "12345:ABCdefGhIjKlMnOpQrStUvWxYz", ChatID: "999"}),
			},
			checkField: "bot_token",
			wantValue:  "1234****",
		},
		{
			name: "pushover masks app token",
			channel: Channel{
				Type:     ProviderPushover,
				Settings: mustJSON(PushoverSettings{AppToken: "apptoken12345", UserKey: "userkey12345"}),
			},
			checkField: "app_token",
			wantValue:  "appt****",
		},
		{
			name: "ntfy masks token and password",
			channel: Channel{
				Type: ProviderNtfy,
				Settings: mustJSON(NtfySettings{
					Server:   "https://ntfy.sh",
					Topic:    "alerts",
					Priority: 3,
					Token:    "tk_long_token_here",
					Password: "my-secret-pass",
				}),
			},
		},
		{
			name: "smtp masks password",
			channel: Channel{
				Type: ProviderSMTP,
				Settings: mustJSON(SMTPSettings{
					Host:     "smtp.example.com",
					Port:     587,
					From:     "a@b.com",
					To:       "c@d.com",
					Password: "smtp-secret-pass",
				}),
			},
			checkField: "password",
			wantValue:  "smtp****",
		},
		{
			name: "apprise masks urls",
			channel: Channel{
				Type: ProviderApprise,
				Settings: mustJSON(AppriseSettings{
					URL:  "http://apprise.local",
					Urls: "slack://xoxb-token/channel",
				}),
			},
			checkField: "urls",
			wantValue:  "slac****",
		},
		{
			name: "mqtt masks password",
			channel: Channel{
				Type: ProviderMQTT,
				Settings: mustJSON(MQTTSettings{
					Broker:   "tcp://mqtt.local:1883",
					Topic:    "sentinel/events",
					Password: "mqtt-secret-password",
				}),
			},
			checkField: "password",
			wantValue:  "mqtt****",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			masked := MaskSecrets(tt.channel)

			// Settings should be valid JSON.
			var m map[string]json.RawMessage
			if err := json.Unmarshal(masked.Settings, &m); err != nil {
				t.Fatalf("masked settings is not valid JSON: %v", err)
			}

			// Original should not be modified.
			if string(masked.Settings) == string(tt.channel.Settings) && tt.checkField != "url" {
				t.Error("MaskSecrets() did not modify settings (returned same bytes)")
			}

			if tt.checkField != "" && tt.wantValue != "" {
				raw, ok := m[tt.checkField]
				if !ok {
					t.Fatalf("masked settings missing field %q", tt.checkField)
				}
				var got string
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatalf("unmarshal field %q: %v", tt.checkField, err)
				}
				if got != tt.wantValue {
					t.Errorf("masked %q = %q, want %q", tt.checkField, got, tt.wantValue)
				}
			}
		})
	}
}

func TestMaskSecretsNtfyFields(t *testing.T) {
	ch := Channel{
		Type: ProviderNtfy,
		Settings: mustJSON(NtfySettings{
			Server:   "https://ntfy.sh",
			Topic:    "alerts",
			Priority: 3,
			Token:    "tk_long_token_here",
			Password: "my-secret-pass",
		}),
	}
	masked := MaskSecrets(ch)

	var s NtfySettings
	if err := json.Unmarshal(masked.Settings, &s); err != nil {
		t.Fatalf("unmarshal ntfy settings: %v", err)
	}
	if s.Token != "tk_l****" {
		t.Errorf("token = %q, want %q", s.Token, "tk_l****")
	}
	if s.Password != "my-s****" {
		t.Errorf("password = %q, want %q", s.Password, "my-s****")
	}
	// Non-secret fields should be preserved.
	if s.Server != "https://ntfy.sh" {
		t.Errorf("server = %q, want %q", s.Server, "https://ntfy.sh")
	}
	if s.Topic != "alerts" {
		t.Errorf("topic = %q, want %q", s.Topic, "alerts")
	}
}

func TestMaskSecretsNtfyEmptyCredentials(t *testing.T) {
	ch := Channel{
		Type: ProviderNtfy,
		Settings: mustJSON(NtfySettings{
			Server:   "https://ntfy.sh",
			Topic:    "public-topic",
			Priority: 3,
		}),
	}
	masked := MaskSecrets(ch)

	var s NtfySettings
	if err := json.Unmarshal(masked.Settings, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if s.Token != "" {
		t.Errorf("token = %q, want empty", s.Token)
	}
	if s.Password != "" {
		t.Errorf("password = %q, want empty", s.Password)
	}
}

func TestMaskSecretsWebhookHeaders(t *testing.T) {
	ch := Channel{
		Type: ProviderWebhook,
		Settings: mustJSON(WebhookSettings{
			URL: "http://example.com/hook",
			Headers: map[string]string{
				"Authorization": "Bearer my-long-token",
				"X-Custom":      "not-sensitive",
				"X-Api-Key":     "key-value-12345",
			},
		}),
	}
	masked := MaskSecrets(ch)

	var s WebhookSettings
	if err := json.Unmarshal(masked.Settings, &s); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// "Authorization" contains "bearer" so it should be masked.
	if s.Headers["Authorization"] != "Bear****" {
		t.Errorf("Authorization = %q, want %q", s.Headers["Authorization"], "Bear****")
	}
	// "X-Custom" doesn't contain sensitive words, should be unchanged.
	if s.Headers["X-Custom"] != "not-sensitive" {
		t.Errorf("X-Custom = %q, want %q", s.Headers["X-Custom"], "not-sensitive")
	}
	// "X-Api-Key" contains "key" so it should be masked.
	if s.Headers["X-Api-Key"] != "key-****" {
		t.Errorf("X-Api-Key = %q, want %q", s.Headers["X-Api-Key"], "key-****")
	}
}

func TestMaskSecretsInvalidJSON(t *testing.T) {
	ch := Channel{
		Type:     ProviderGotify,
		Settings: json.RawMessage(`{not valid json}`),
	}
	masked := MaskSecrets(ch)
	// Should return the original settings unchanged when JSON is invalid.
	if string(masked.Settings) != string(ch.Settings) {
		t.Errorf("invalid JSON should be returned unchanged, got %q", string(masked.Settings))
	}
}

// --- BuildNotifier tests ---

func TestBuildNotifier(t *testing.T) {
	tests := []struct {
		name     string
		channel  Channel
		wantName string
		wantErr  bool
	}{
		{
			name: "gotify",
			channel: Channel{
				Type:     ProviderGotify,
				Settings: mustJSON(GotifySettings{URL: "http://gotify.example.com", Token: "tok"}),
			},
			wantName: "gotify",
		},
		{
			name: "webhook",
			channel: Channel{
				Type:     ProviderWebhook,
				Settings: mustJSON(WebhookSettings{URL: "http://example.com/hook", Headers: nil}),
			},
			wantName: "webhook",
		},
		{
			name: "slack",
			channel: Channel{
				Type:     ProviderSlack,
				Settings: mustJSON(SlackSettings{WebhookURL: "https://hooks.slack.com/services/T/B/x"}),
			},
			wantName: "slack",
		},
		{
			name: "discord",
			channel: Channel{
				Type:     ProviderDiscord,
				Settings: mustJSON(DiscordSettings{WebhookURL: "https://discord.com/api/webhooks/1/tok"}),
			},
			wantName: "discord",
		},
		{
			name: "ntfy",
			channel: Channel{
				Type: ProviderNtfy,
				Settings: mustJSON(NtfySettings{
					Server: "https://ntfy.sh", Topic: "alerts", Priority: 3,
				}),
			},
			wantName: "ntfy",
		},
		{
			name: "telegram",
			channel: Channel{
				Type:     ProviderTelegram,
				Settings: mustJSON(TelegramSettings{BotToken: "123:ABC", ChatID: "999"}),
			},
			wantName: "telegram",
		},
		{
			name: "pushover",
			channel: Channel{
				Type:     ProviderPushover,
				Settings: mustJSON(PushoverSettings{AppToken: "apptoken", UserKey: "userkey"}),
			},
			wantName: "pushover",
		},
		{
			name: "smtp",
			channel: Channel{
				Type: ProviderSMTP,
				Settings: mustJSON(SMTPSettings{
					Host: "smtp.example.com", Port: 587,
					From: "a@b.com", To: "c@d.com",
				}),
			},
			wantName: "smtp",
		},
		{
			name: "apprise",
			channel: Channel{
				Type: ProviderApprise,
				Settings: mustJSON(AppriseSettings{
					URL: "http://apprise.local", Urls: "slack://tok/chan",
				}),
			},
			wantName: "apprise",
		},
		{
			name: "mqtt",
			channel: Channel{
				Type: ProviderMQTT,
				Settings: mustJSON(MQTTSettings{
					Broker: "tcp://mqtt.local:1883", Topic: "sentinel/events",
				}),
			},
			wantName: "mqtt",
		},
		{
			name: "unknown provider",
			channel: Channel{
				Type:     "unknown",
				Settings: json.RawMessage(`{}`),
			},
			wantErr: true,
		},
		{
			name: "gotify invalid JSON",
			channel: Channel{
				Type:     ProviderGotify,
				Settings: json.RawMessage(`{bad json}`),
			},
			wantErr: true,
		},
		{
			name: "webhook invalid URL scheme",
			channel: Channel{
				Type:     ProviderWebhook,
				Settings: mustJSON(WebhookSettings{URL: "ftp://example.com/hook"}),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := BuildNotifier(tt.channel)
			if tt.wantErr {
				if err == nil {
					t.Fatal("BuildNotifier() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildNotifier() error = %v", err)
			}
			if n == nil {
				t.Fatal("BuildNotifier() returned nil notifier")
			}
			if n.Name() != tt.wantName {
				t.Errorf("Name() = %q, want %q", n.Name(), tt.wantName)
			}
		})
	}
}

// mustJSON marshals v to json.RawMessage, panicking on error.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
