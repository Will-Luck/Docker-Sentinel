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

// Send posts a rich embed notification to a Discord webhook.
func (d *Discord) Send(ctx context.Context, event Event) error {
	embed := discordEmbed{
		Title:     formatTitle(event.Type),
		Color:     discordColor(event.Type),
		Timestamp: event.Timestamp.UTC().Format(time.RFC3339),
	}

	embed.Fields = append(embed.Fields, discordField{
		Name: "Container", Value: event.ContainerName, Inline: true,
	})
	if event.OldImage != "" {
		embed.Fields = append(embed.Fields, discordField{
			Name: "Old Image", Value: event.OldImage, Inline: true,
		})
	}
	if event.NewImage != "" {
		embed.Fields = append(embed.Fields, discordField{
			Name: "New Image", Value: event.NewImage, Inline: true,
		})
	}
	if event.Error != "" {
		embed.Fields = append(embed.Fields, discordField{
			Name: "Error", Value: event.Error, Inline: false,
		})
	}

	body, err := json.Marshal(discordPayload{Embeds: []discordEmbed{embed}})
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

func discordColor(t EventType) int {
	switch t {
	case EventUpdateSucceeded, EventRollbackOK:
		return 0x2ECC71 // green
	case EventUpdateFailed, EventRollbackFailed:
		return 0xE74C3C // red
	case EventUpdateAvailable, EventVersionAvailable:
		return 0xF39C12 // orange
	default:
		return 0x3498DB // blue
	}
}

type discordPayload struct {
	Embeds []discordEmbed `json:"embeds"`
}

type discordEmbed struct {
	Title     string         `json:"title"`
	Color     int            `json:"color"`
	Fields    []discordField `json:"fields,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
}

type discordField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}
