package docker

import "testing"

func TestContainerPolicy(t *testing.T) {
	tests := []struct {
		name          string
		labels        map[string]string
		defaultPolicy string
		want          Policy
	}{
		{"no label, default manual", map[string]string{}, "manual", PolicyManual},
		{"no label, default auto", map[string]string{}, "auto", PolicyAuto},
		{"explicit auto", map[string]string{"sentinel.policy": "auto"}, "manual", PolicyAuto},
		{"explicit manual", map[string]string{"sentinel.policy": "manual"}, "auto", PolicyManual},
		{"explicit pinned", map[string]string{"sentinel.policy": "pinned"}, "auto", PolicyPinned},
		{"case insensitive", map[string]string{"sentinel.policy": "AUTO"}, "manual", PolicyAuto},
		{"invalid label falls back", map[string]string{"sentinel.policy": "yolo"}, "manual", PolicyManual},
		{"other labels ignored", map[string]string{"com.example.foo": "bar"}, "manual", PolicyManual},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerPolicy(tt.labels, tt.defaultPolicy)
			if got != tt.want {
				t.Errorf("ContainerPolicy() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsLocalImage(t *testing.T) {
	tests := []struct {
		imageRef string
		want     bool
	}{
		// Nothing is "local" — we always try the registry check.
		// The registry checker handles failures gracefully.
		{"nginx", false},
		{"nginx:latest", false},
		{"myapp:v1", false},
		{"library/nginx", false},
		{"ghcr.io/owner/image", false},
		{"docker.io/library/nginx", false},
		{"registry.example.com/myapp:latest", false},
		{"registry.local:5000/myapp", false},
		{"localhost/myapp", false},
		{"gitea/gitea:latest", false},
		{"postgres:16-alpine", false},
	}

	for _, tt := range tests {
		t.Run(tt.imageRef, func(t *testing.T) {
			got := IsLocalImage(tt.imageRef)
			if got != tt.want {
				t.Errorf("IsLocalImage(%q) = %v, want %v", tt.imageRef, got, tt.want)
			}
		})
	}
}
