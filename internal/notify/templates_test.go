package notify

import (
	"strings"
	"testing"
	"time"
)

// --- TemplateEngine.Render tests ---

func TestTemplateEngineRender(t *testing.T) {
	data := TemplateData{
		ContainerName: "nginx",
		OldImage:      "nginx:1.24",
		NewImage:      "nginx:1.25",
		Type:          "update_available",
		Title:         "Update Available",
		Emoji:         "",
		Timestamp:     time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name       string
		customs    map[string]string
		want       string
		useDefault bool // if true, check output matches defaultFormat
	}{
		{
			name:       "nil customs uses default",
			customs:    nil,
			useDefault: true,
		},
		{
			name:       "empty customs uses default",
			customs:    map[string]string{},
			useDefault: true,
		},
		{
			name: "valid custom template",
			customs: map[string]string{
				"update_available": "Container: {{.ContainerName}}",
			},
			want: "Container: nginx",
		},
		{
			name: "invalid template falls back to default",
			customs: map[string]string{
				"update_available": "{{.Invalid",
			},
			useDefault: true,
		},
		{
			name: "template for wrong event type falls back to default",
			customs: map[string]string{
				"update_failed": "Container: {{.ContainerName}}",
			},
			useDefault: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewTemplateEngine(tt.customs)
			got := engine.Render(data)

			if tt.useDefault {
				want := defaultFormat(data)
				if got != want {
					t.Errorf("Render() = %q, want defaultFormat %q", got, want)
				}
			} else {
				if got != tt.want {
					t.Errorf("Render() = %q, want %q", got, tt.want)
				}
			}
		})
	}
}

// --- RenderPreview tests ---

func TestRenderPreview(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		eventType string
		wantErr   bool
		wantSub   string // substring expected in output
	}{
		{
			name:      "valid template",
			template:  "Container: {{.ContainerName}} ({{.Type}})",
			eventType: "update_available",
			wantSub:   "Container: nginx (update_available)",
		},
		{
			name:      "invalid template",
			template:  "{{.Invalid",
			eventType: "update_available",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RenderPreview(tt.template, tt.eventType)
			if tt.wantErr {
				if err == nil {
					t.Fatal("RenderPreview() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderPreview() error = %v", err)
			}
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("RenderPreview() = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}

// --- defaultFormat tests ---

func TestDefaultFormat(t *testing.T) {
	tests := []struct {
		name         string
		data         TemplateData
		wantContains []string
		wantMissing  []string
	}{
		{
			name: "with emoji and title",
			data: TemplateData{
				Emoji:         "\U0001f504",
				Title:         "Update Available",
				ContainerName: "nginx",
				OldImage:      "nginx:1.24",
				NewImage:      "nginx:1.25",
				Type:          "update_available",
			},
			wantContains: []string{
				"\U0001f504 Update Available\n",
				"Container: nginx",
				"Image: nginx:1.24",
				"New: nginx:1.25",
			},
		},
		{
			name: "without emoji with title",
			data: TemplateData{
				Title:         "Update Failed",
				ContainerName: "redis",
				Type:          "update_failed",
				Error:         "pull timeout",
			},
			wantContains: []string{
				"Update Failed\n",
				"Container: redis",
				"Error: pull timeout",
			},
			wantMissing: []string{"\U0001f504"},
		},
		{
			name: "without emoji or title uses Type",
			data: TemplateData{
				Type:          "update_started",
				ContainerName: "postgres",
			},
			wantContains: []string{
				"update_started\n",
				"Container: postgres",
			},
		},
		{
			name: "all fields populated",
			data: TemplateData{
				Emoji:         "\U0001f504",
				Title:         "Update Succeeded",
				ContainerName: "nginx",
				OldImage:      "nginx:1.24",
				NewImage:      "nginx:1.25",
				Error:         "warning: slow pull",
				Type:          "update_succeeded",
			},
			wantContains: []string{
				"\U0001f504 Update Succeeded\n",
				"Container: nginx",
				"Image: nginx:1.24",
				"New: nginx:1.25",
				"Error: warning: slow pull",
			},
		},
		{
			name: "matching OldImage and NewImage skips New line",
			data: TemplateData{
				Title:         "Digest",
				ContainerName: "app",
				OldImage:      "app:latest",
				NewImage:      "app:latest",
				Type:          "digest",
			},
			wantContains: []string{
				"Image: app:latest",
			},
			wantMissing: []string{
				"New: app:latest",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := defaultFormat(tt.data)
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("defaultFormat() missing %q in:\n%s", want, got)
				}
			}
			for _, absent := range tt.wantMissing {
				if strings.Contains(got, absent) {
					t.Errorf("defaultFormat() should not contain %q in:\n%s", absent, got)
				}
			}
		})
	}
}
