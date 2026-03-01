package server

import "testing"

func TestBaseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v2.0.1 (abc1234)", "v2.0.1"},
		{"v2.0.1", "v2.0.1"},
		{"dev (abc1234)", "dev"},
		{"dev", "dev"},
		{"", ""},
		{"  v2.0.1  ", "v2.0.1"},
		{"v2.0.1 (abc1234) extra", "v2.0.1"},
	}

	for _, tt := range tests {
		got := baseVersion(tt.input)
		if got != tt.want {
			t.Errorf("baseVersion(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestReplaceImageTag(t *testing.T) {
	tests := []struct {
		image  string
		newTag string
		want   string
	}{
		{"ghcr.io/foo/sentinel:v2.0.0", "v2.0.1", "ghcr.io/foo/sentinel:v2.0.1"},
		{"sentinel:latest", "v2.0.1", "sentinel:v2.0.1"},
		{"sentinel", "v2.0.1", "sentinel:v2.0.1"},
		{"registry.example.com:5000/sentinel:old", "v2.0.1", "registry.example.com:5000/sentinel:v2.0.1"},
		{"ghcr.io/will-luck/docker-sentinel:v2.0.0", "v2.1.0", "ghcr.io/will-luck/docker-sentinel:v2.1.0"},
	}

	for _, tt := range tests {
		got := replaceImageTag(tt.image, tt.newTag)
		if got != tt.want {
			t.Errorf("replaceImageTag(%q, %q) = %q, want %q", tt.image, tt.newTag, got, tt.want)
		}
	}
}
