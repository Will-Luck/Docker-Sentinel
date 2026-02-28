package docker

import (
	"testing"
	"time"
)

func TestContainerPolicy(t *testing.T) {
	tests := []struct {
		name          string
		labels        map[string]string
		defaultPolicy string
		want          Policy
		wantLabel     bool
	}{
		{"no label, default manual", map[string]string{}, "manual", PolicyManual, false},
		{"no label, default auto", map[string]string{}, "auto", PolicyAuto, false},
		{"explicit auto", map[string]string{"sentinel.policy": "auto"}, "manual", PolicyAuto, true},
		{"explicit manual", map[string]string{"sentinel.policy": "manual"}, "auto", PolicyManual, true},
		{"explicit pinned", map[string]string{"sentinel.policy": "pinned"}, "auto", PolicyPinned, true},
		{"case insensitive", map[string]string{"sentinel.policy": "AUTO"}, "manual", PolicyAuto, true},
		{"invalid label falls back", map[string]string{"sentinel.policy": "yolo"}, "manual", PolicyManual, false},
		{"other labels ignored", map[string]string{"com.example.foo": "bar"}, "manual", PolicyManual, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, fromLabel := ContainerPolicy(tt.labels, tt.defaultPolicy)
			if got != tt.want {
				t.Errorf("ContainerPolicy() policy = %q, want %q", got, tt.want)
			}
			if fromLabel != tt.wantLabel {
				t.Errorf("ContainerPolicy() fromLabel = %v, want %v", fromLabel, tt.wantLabel)
			}
		})
	}
}

func TestContainerSemverScope(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   SemverScope
	}{
		{"no label", map[string]string{}, ScopeDefault},
		{"patch", map[string]string{"sentinel.semver": "patch"}, ScopePatch},
		{"minor", map[string]string{"sentinel.semver": "minor"}, ScopeMinor},
		{"major", map[string]string{"sentinel.semver": "major"}, ScopeMajor},
		{"all alias", map[string]string{"sentinel.semver": "all"}, ScopeMajor},
		{"case insensitive", map[string]string{"sentinel.semver": "MINOR"}, ScopeMinor},
		{"invalid falls back", map[string]string{"sentinel.semver": "yolo"}, ScopeDefault},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerSemverScope(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerSemverScope() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContainerGracePeriod(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   time.Duration
	}{
		{"missing label", map[string]string{}, 0},
		{"empty value", map[string]string{"sentinel.grace-period": ""}, 0},
		{"valid seconds", map[string]string{"sentinel.grace-period": "30s"}, 30 * time.Second},
		{"valid minutes", map[string]string{"sentinel.grace-period": "5m"}, 5 * time.Minute},
		{"valid days (capped)", map[string]string{"sentinel.grace-period": "1d"}, time.Hour},
		{"invalid value", map[string]string{"sentinel.grace-period": "abc"}, 0},
		{"exceeds cap", map[string]string{"sentinel.grace-period": "2h"}, time.Hour},
		{"negative", map[string]string{"sentinel.grace-period": "-5s"}, 0},
		{"exactly 1h", map[string]string{"sentinel.grace-period": "1h"}, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ContainerGracePeriod(tt.labels)
			if got != tt.want {
				t.Errorf("ContainerGracePeriod() = %v, want %v", got, tt.want)
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
