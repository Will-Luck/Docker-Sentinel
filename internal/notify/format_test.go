package notify

import (
	"strings"
	"testing"
	"time"
)

// --- formatTitle tests ---

func TestFormatTitle(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      string
	}{
		{EventUpdateAvailable, "Sentinel: Update Available"},
		{EventUpdateStarted, "Sentinel: Update Started"},
		{EventUpdateSucceeded, "Sentinel: Update Succeeded"},
		{EventUpdateFailed, "Sentinel: Update Failed"},
		{EventRollbackOK, "Sentinel: Rollback Succeeded"},
		{EventRollbackFailed, "Sentinel: Rollback Failed"},
		{EventVersionAvailable, "Sentinel: Version Available"},
		{EventContainerState, "Sentinel: Container State"},
		{EventDigest, "Sentinel: Digest"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			got := formatTitle(tt.eventType)
			if got != tt.want {
				t.Errorf("formatTitle(%q) = %q, want %q", tt.eventType, got, tt.want)
			}
		})
	}
}

// --- formatMessage tests ---

func TestFormatMessage(t *testing.T) {
	tests := []struct {
		name         string
		event        Event
		wantContains []string
		wantMissing  []string
	}{
		{
			name: "all fields populated",
			event: Event{
				ContainerName: "nginx",
				OldImage:      "nginx:1.25",
				NewImage:      "nginx:1.26",
				Error:         "pull timeout",
				Timestamp:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
			},
			wantContains: []string{
				"Container: nginx",
				"Old image: nginx:1.25",
				"New image: nginx:1.26",
				"Error: pull timeout",
			},
		},
		{
			name: "only container name",
			event: Event{
				ContainerName: "redis",
			},
			wantContains: []string{"Container: redis"},
			wantMissing:  []string{"Old image:", "New image:", "Error:"},
		},
		{
			name: "container name and error",
			event: Event{
				ContainerName: "postgres",
				Error:         "connection refused",
			},
			wantContains: []string{
				"Container: postgres",
				"Error: connection refused",
			},
			wantMissing: []string{"Old image:", "New image:"},
		},
		{
			name: "empty container name",
			event: Event{
				ContainerName: "",
			},
			wantContains: []string{"Container: \n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMessage(tt.event)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("formatMessage() missing %q in:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantMissing {
				if strings.Contains(got, absent) {
					t.Errorf("formatMessage() should not contain %q in:\n%s", absent, got)
				}
			}
		})
	}
}

// --- formatMessageMarkdown tests ---

func TestFormatMessageMarkdown(t *testing.T) {
	tests := []struct {
		name         string
		event        Event
		wantContains []string
	}{
		{
			name: "all fields with markdown",
			event: Event{
				ContainerName: "nginx",
				OldImage:      "nginx:1.25",
				NewImage:      "nginx:1.26",
				Error:         "timeout",
			},
			wantContains: []string{
				"**Container:** `nginx`",
				"**Old image:** `nginx:1.25`",
				"**New image:** `nginx:1.26`",
				"**Error:** timeout",
			},
		},
		{
			name: "minimal event",
			event: Event{
				ContainerName: "redis",
			},
			wantContains: []string{"**Container:** `redis`"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatMessageMarkdown(tt.event)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("formatMessageMarkdown() missing %q in:\n%s", want, got)
				}
			}
		})
	}

	// Verify markdown is absent from plain formatMessage for the same event.
	event := Event{ContainerName: "nginx", OldImage: "nginx:1.25"}
	plain := formatMessage(event)
	if strings.Contains(plain, "**") || strings.Contains(plain, "`") {
		t.Errorf("formatMessage() should not contain markdown: %s", plain)
	}
}

// --- discordColor tests ---

func TestDiscordColor(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      int
		desc      string
	}{
		{EventUpdateSucceeded, 0x2ECC71, "green"},
		{EventRollbackOK, 0x2ECC71, "green"},
		{EventUpdateFailed, 0xE74C3C, "red"},
		{EventRollbackFailed, 0xE74C3C, "red"},
		{EventUpdateAvailable, 0xF39C12, "orange"},
		{EventVersionAvailable, 0xF39C12, "orange"},
		{EventUpdateStarted, 0x3498DB, "blue"},
		{EventContainerState, 0x3498DB, "blue"},
		{EventDigest, 0x3498DB, "blue"},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			got := discordColor(tt.eventType)
			if got != tt.want {
				t.Errorf("discordColor(%q) = 0x%X, want 0x%X (%s)", tt.eventType, got, tt.want, tt.desc)
			}
		})
	}
}

// --- priority tests ---

func TestPriority(t *testing.T) {
	tests := []struct {
		eventType EventType
		want      int
	}{
		{EventUpdateFailed, 8},
		{EventRollbackFailed, 8},
		{EventUpdateAvailable, 5},
		{EventUpdateStarted, 5},
		{EventUpdateSucceeded, 5},
		{EventRollbackOK, 5},
		{EventVersionAvailable, 5},
		{EventContainerState, 5},
		{EventDigest, 5},
	}

	for _, tt := range tests {
		t.Run(string(tt.eventType), func(t *testing.T) {
			got := priority(tt.eventType)
			if got != tt.want {
				t.Errorf("priority(%q) = %d, want %d", tt.eventType, got, tt.want)
			}
		})
	}
}

// --- maskToken tests ---

func TestMaskToken(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "****"},
		{"abc", "****"},
		{"abcd", "****"},
		{"abcde", "abcd****"},
		{"my-long-api-token-here", "my-l****"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := maskToken(tt.input)
			if got != tt.want {
				t.Errorf("maskToken(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// --- maskURL tests ---

func TestMaskURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "slack webhook",
			input: "https://hooks.slack.com/services/T00/B00/xxx",
			want:  "https://hooks.slack.com/****",
		},
		{
			name:  "not a url",
			input: "not-a-url",
			want:  "****",
		},
		{
			name:  "empty string",
			input: "",
			want:  "****",
		},
		{
			name:  "url without path",
			input: "http://example.com",
			want:  "http://example.com/****",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := maskURL(tt.input)
			if got != tt.want {
				t.Errorf("maskURL(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
