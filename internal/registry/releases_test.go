package registry

import "testing"

func TestImageToGitHubRepo(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		want     string
	}{
		{"ghcr basic", "ghcr.io/will-luck/sentinel", "will-luck/sentinel"},
		{"ghcr strips tag", "ghcr.io/will-luck/sentinel:v2.0", "will-luck/sentinel"},
		{"ghcr strips digest", "ghcr.io/will-luck/sentinel@sha256:abc123", "will-luck/sentinel"},
		{"ghcr tag and digest", "ghcr.io/will-luck/sentinel:v2.0@sha256:abc123", "will-luck/sentinel"},
		{"lscr linuxserver", "lscr.io/linuxserver/sonarr", "linuxserver/docker-sonarr"},
		{"linuxserver shorthand", "linuxserver/radarr", "linuxserver/docker-radarr"},
		{"linuxserver strips tag", "linuxserver/radarr:latest", "linuxserver/docker-radarr"},
		{"bare image unmapped", "nginx", ""},
		{"bare image with tag unmapped", "nginx:1.25", ""},
		{"docker.io library unmapped", "docker.io/library/nginx", ""},
		{"hotio unmapped", "hotio.dev/hotio/sonarr:latest", ""},
		{"empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := imageToGitHubRepo(tt.imageRef)
			if got != tt.want {
				t.Errorf("imageToGitHubRepo(%q) = %q, want %q", tt.imageRef, got, tt.want)
			}
		})
	}
}

func TestMatchImagePattern(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		pattern  string
		want     bool
	}{
		{"exact bare", "nginx", "nginx", true},
		{"exact with path", "ghcr.io/owner/repo", "ghcr.io/owner/repo", true},
		{"wildcard match", "ghcr.io/owner/repo", "ghcr.io/owner/*", true},
		{"wildcard different owner", "ghcr.io/other/repo", "ghcr.io/owner/*", false},
		{"bare name matches last segment with prefix", "library/nginx", "nginx", true},
		{"bare name matches deep path", "docker.io/library/nginx", "nginx", true},
		{"bare name no partial match", "nginx-custom", "nginx", false},
		{"no match different names", "nginx", "redis", false},
		{"wildcard different registry", "ghcr.io/owner/repo", "docker.io/owner/*", false},
		{"bare name exact", "nginx", "nginx", true},
		{"wildcard empty after prefix", "ghcr.io/owner/", "ghcr.io/owner/*", true},
		{"pattern with slash no match", "nginx", "library/redis", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchImagePattern(tt.imageRef, tt.pattern)
			if got != tt.want {
				t.Errorf("matchImagePattern(%q, %q) = %v, want %v", tt.imageRef, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestImageToGitHubRepoWithSources(t *testing.T) {
	tests := []struct {
		name     string
		imageRef string
		sources  []ReleaseSource
		want     string
	}{
		{
			name:     "custom source bare name match",
			imageRef: "nginx:1.25",
			sources:  []ReleaseSource{{ImagePattern: "nginx", GitHubRepo: "nginx/nginx"}},
			want:     "nginx/nginx",
		},
		{
			name:     "fallback to built-in ghcr",
			imageRef: "ghcr.io/owner/repo:v1",
			sources:  nil,
			want:     "owner/repo",
		},
		{
			name:     "no match anywhere",
			imageRef: "unknown/image:v1",
			sources:  nil,
			want:     "",
		},
		{
			name:     "custom source wildcard",
			imageRef: "ghcr.io/org/app:v1",
			sources:  []ReleaseSource{{ImagePattern: "ghcr.io/org/*", GitHubRepo: "org/app"}},
			want:     "org/app",
		},
		{
			name:     "custom source takes priority over built-in",
			imageRef: "ghcr.io/owner/repo:v2",
			sources:  []ReleaseSource{{ImagePattern: "ghcr.io/owner/repo", GitHubRepo: "custom/override"}},
			want:     "custom/override",
		},
		{
			name:     "empty sources uses fallback",
			imageRef: "linuxserver/sonarr:latest",
			sources:  []ReleaseSource{},
			want:     "linuxserver/docker-sonarr",
		},
		{
			name:     "first matching source wins",
			imageRef: "ghcr.io/org/tool:v1",
			sources: []ReleaseSource{
				{ImagePattern: "ghcr.io/org/*", GitHubRepo: "first/match"},
				{ImagePattern: "ghcr.io/org/tool", GitHubRepo: "second/match"},
			},
			want: "first/match",
		},
		{
			name:     "digest stripped before source matching",
			imageRef: "nginx@sha256:deadbeef",
			sources:  []ReleaseSource{{ImagePattern: "nginx", GitHubRepo: "nginx/nginx"}},
			want:     "nginx/nginx",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := imageToGitHubRepoWithSources(tt.imageRef, tt.sources)
			if got != tt.want {
				t.Errorf("imageToGitHubRepoWithSources(%q, ...) = %q, want %q", tt.imageRef, got, tt.want)
			}
		})
	}
}
