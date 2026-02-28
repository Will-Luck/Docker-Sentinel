package notify

import (
	"bytes"
	"strings"
	"text/template"
	"time"
)

// TemplateData holds the variables available to notification templates.
type TemplateData struct {
	ContainerName string
	OldImage      string
	NewImage      string
	OldDigest     string
	NewDigest     string
	Error         string
	Type          string // event type name
	Timestamp     time.Time
	Title         string
	Emoji         string
	Severity      string
}

// TemplateEngine renders notification messages using Go text/template.
// When no custom template is set for an event type, the default format is used.
type TemplateEngine struct {
	customs map[string]string // event_type -> template string
}

// NewTemplateEngine creates an engine with the given custom templates.
func NewTemplateEngine(customs map[string]string) *TemplateEngine {
	return &TemplateEngine{customs: customs}
}

// Render produces the notification message body for the given event data.
// If a custom template exists for the event type, it is used. Otherwise
// the default format is returned. On template error, falls back to default.
func (e *TemplateEngine) Render(data TemplateData) string {
	if e != nil && e.customs != nil {
		if tmplStr, ok := e.customs[data.Type]; ok && tmplStr != "" {
			result, err := executeTemplate(tmplStr, data)
			if err == nil {
				return result
			}
			// Fall through to default on error.
		}
	}
	return defaultFormat(data)
}

// RenderPreview renders a template string with sample data for preview purposes.
// Returns the rendered output or an error if the template is invalid.
func RenderPreview(tmplStr string, eventType string) (string, error) {
	data := sampleData(eventType)
	return executeTemplate(tmplStr, data)
}

func executeTemplate(tmplStr string, data TemplateData) (string, error) {
	t, err := template.New("notify").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func defaultFormat(data TemplateData) string {
	var b strings.Builder
	if data.Emoji != "" {
		b.WriteString(data.Emoji)
		b.WriteString(" ")
	}
	if data.Title != "" {
		b.WriteString(data.Title)
	} else {
		b.WriteString(data.Type)
	}
	b.WriteString("\n")
	if data.ContainerName != "" {
		b.WriteString("Container: ")
		b.WriteString(data.ContainerName)
		b.WriteString("\n")
	}
	if data.OldImage != "" {
		b.WriteString("Image: ")
		b.WriteString(data.OldImage)
		b.WriteString("\n")
	}
	if data.NewImage != "" && data.NewImage != data.OldImage {
		b.WriteString("New: ")
		b.WriteString(data.NewImage)
		b.WriteString("\n")
	}
	if data.Error != "" {
		b.WriteString("Error: ")
		b.WriteString(data.Error)
		b.WriteString("\n")
	}
	return b.String()
}

func sampleData(eventType string) TemplateData {
	return TemplateData{
		ContainerName: "nginx",
		OldImage:      "nginx:1.24",
		NewImage:      "nginx:1.25",
		OldDigest:     "sha256:abc123...",
		NewDigest:     "sha256:def456...",
		Type:          eventType,
		Timestamp:     time.Now(),
		Title:         "Update Available",
		Emoji:         "\U0001f504",
		Severity:      "info",
	}
}
