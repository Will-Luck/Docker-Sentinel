package portainer

import "testing"

func TestIsPortainerImage(t *testing.T) {
	tests := []struct {
		image string
		want  bool
	}{
		// Positive: bare image references.
		{"portainer/portainer-ce", true},
		{"portainer/portainer-ce:latest", true},
		{"portainer/portainer-ee:2.19.0", true},
		{"portainer/portainer-ce:2.21.5-alpine", true},

		// Positive: Docker Hub canonical forms.
		{"docker.io/portainer/portainer-ce", true},
		{"docker.io/portainer/portainer-ce:latest", true},
		{"index.docker.io/portainer/portainer-ce:latest", true},
		{"index.docker.io/portainer/portainer-ce:2.19.0", true},

		// Positive: private registry with port.
		{"myregistry:5000/portainer/portainer-ce", true},
		{"myregistry:5000/portainer/portainer-ce:latest", true},

		// Positive: digest reference.
		{"portainer/portainer-ce@sha256:abcdef1234567890", true},
		{"docker.io/portainer/portainer-ce@sha256:abcdef1234567890", true},

		// Negative: unrelated images.
		{"nginx:latest", false},
		{"nginx", false},
		{"ghcr.io/someorg/portainer-ce", false},
		{"ghcr.io/someorg/someimage:latest", false},
		{"library/nginx", false},
		{"index.docker.io/library/nginx:latest", false},
		{"myregistry:5000/someorg/someimage", false},

		// Negative: partial name collisions.
		{"notportainer/portainer-ce", false},
		{"portainer-ce", false},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			got := IsPortainerImage(tt.image)
			if got != tt.want {
				t.Errorf("IsPortainerImage(%q) = %v, want %v", tt.image, got, tt.want)
			}
		})
	}
}
