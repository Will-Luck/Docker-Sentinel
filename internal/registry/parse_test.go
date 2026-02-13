package registry

import "testing"

func TestRegistryHost(t *testing.T) {
	tests := []struct {
		imageRef string
		want     string
	}{
		{"nginx", "docker.io"},
		{"nginx:1.25", "docker.io"},
		{"library/nginx", "docker.io"},
		{"gitea/gitea:1.21", "docker.io"},
		{"ghcr.io/user/repo:v1.0", "ghcr.io"},
		{"hotio.dev/hotio/sonarr:latest", "hotio.dev"},
		{"registry-1.docker.io/library/nginx:latest", "docker.io"},
		{"lscr.io/linuxserver/radarr:latest", "lscr.io"},
		{"docker.io/library/nginx", "docker.io"},
		{"", "docker.io"},
	}
	for _, tt := range tests {
		t.Run(tt.imageRef, func(t *testing.T) {
			got := RegistryHost(tt.imageRef)
			if got != tt.want {
				t.Errorf("RegistryHost(%q) = %q, want %q", tt.imageRef, got, tt.want)
			}
		})
	}
}
