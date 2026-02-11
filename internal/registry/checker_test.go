package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// mockDockerForRegistry implements docker.API for registry checker tests.
type mockDockerForRegistry struct {
	imageDigests        map[string]string
	imageDigestErr      map[string]error
	distributionDigests map[string]string
	distributionErr     map[string]error
}

func newMockRegistry() *mockDockerForRegistry {
	return &mockDockerForRegistry{
		imageDigests:        make(map[string]string),
		imageDigestErr:      make(map[string]error),
		distributionDigests: make(map[string]string),
		distributionErr:     make(map[string]error),
	}
}

func (m *mockDockerForRegistry) ListContainers(_ context.Context) ([]container.Summary, error) {
	return nil, nil
}
func (m *mockDockerForRegistry) ListAllContainers(_ context.Context) ([]container.Summary, error) {
	return nil, nil
}
func (m *mockDockerForRegistry) InspectContainer(_ context.Context, _ string) (container.InspectResponse, error) {
	return container.InspectResponse{}, nil
}
func (m *mockDockerForRegistry) StopContainer(_ context.Context, _ string, _ int) error { return nil }
func (m *mockDockerForRegistry) RemoveContainer(_ context.Context, _ string) error      { return nil }
func (m *mockDockerForRegistry) CreateContainer(_ context.Context, _ string, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig) (string, error) {
	return "", nil
}
func (m *mockDockerForRegistry) StartContainer(_ context.Context, _ string) error { return nil }
func (m *mockDockerForRegistry) PullImage(_ context.Context, _ string) error      { return nil }
func (m *mockDockerForRegistry) Close() error                                     { return nil }

func (m *mockDockerForRegistry) ImageDigest(_ context.Context, ref string) (string, error) {
	if err, ok := m.imageDigestErr[ref]; ok {
		return "", err
	}
	return m.imageDigests[ref], nil
}

func (m *mockDockerForRegistry) DistributionDigest(_ context.Context, ref string) (string, error) {
	if err, ok := m.distributionErr[ref]; ok {
		return "", err
	}
	return m.distributionDigests[ref], nil
}

func TestCheckUpdateAvailable(t *testing.T) {
	mock := newMockRegistry()
	mock.imageDigests["nginx:1.25"] = "docker.io/library/nginx@sha256:aaa111"
	mock.distributionDigests["nginx:1.25"] = "sha256:bbb222"

	checker := NewChecker(mock, logging.New(false))
	result := checker.Check(context.Background(), "nginx:1.25")

	if result.IsLocal {
		t.Error("expected IsLocal=false")
	}
	if !result.UpdateAvailable {
		t.Error("expected UpdateAvailable=true (digests differ)")
	}
	if result.Error != nil {
		t.Errorf("unexpected error: %v", result.Error)
	}
}

func TestCheckNoUpdate(t *testing.T) {
	mock := newMockRegistry()
	mock.imageDigests["nginx:1.25"] = "docker.io/library/nginx@sha256:aaa111"
	mock.distributionDigests["nginx:1.25"] = "sha256:aaa111"

	checker := NewChecker(mock, logging.New(false))
	result := checker.Check(context.Background(), "nginx:1.25")

	if result.UpdateAvailable {
		t.Error("expected UpdateAvailable=false (digests match)")
	}
}

func TestCheckLocalImageFallsBackOnError(t *testing.T) {
	mock := newMockRegistry()
	// Simulate a locally built image that fails registry check.
	mock.imageDigests["myapp:latest"] = "sha256:local123"
	mock.distributionErr["myapp:latest"] = errors.New("401 unauthorized")

	checker := NewChecker(mock, logging.New(false))
	result := checker.Check(context.Background(), "myapp:latest")

	if !result.IsLocal {
		t.Error("expected IsLocal=true when distribution check fails")
	}
	if result.UpdateAvailable {
		t.Error("should not show updates when registry unreachable")
	}
}

func TestCheckPinnedDigest(t *testing.T) {
	mock := newMockRegistry()
	checker := NewChecker(mock, logging.New(false))

	result := checker.Check(context.Background(), "nginx@sha256:abc123")
	if !result.IsLocal {
		t.Error("expected IsLocal=true for pinned-by-digest image")
	}
}

func TestCheckDistributionError(t *testing.T) {
	mock := newMockRegistry()
	mock.imageDigests["ghcr.io/owner/app:latest"] = "sha256:aaa111"
	mock.distributionErr["ghcr.io/owner/app:latest"] = errors.New("401 unauthorized")

	checker := NewChecker(mock, logging.New(false))
	result := checker.Check(context.Background(), "ghcr.io/owner/app:latest")

	if !result.IsLocal {
		t.Error("expected IsLocal=true when registry is unreachable")
	}
	if result.UpdateAvailable {
		t.Error("should not show update when registry fails")
	}
}

func TestCheckImageDigestError(t *testing.T) {
	mock := newMockRegistry()
	mock.imageDigestErr["ghcr.io/owner/app:latest"] = errors.New("image not found locally")

	checker := NewChecker(mock, logging.New(false))
	result := checker.Check(context.Background(), "ghcr.io/owner/app:latest")

	if result.Error == nil {
		t.Error("expected error when local digest fails")
	}
}

func TestDigestsMatch(t *testing.T) {
	tests := []struct {
		name   string
		local  string
		remote string
		match  bool
	}{
		{"same hash", "sha256:abc", "sha256:abc", true},
		{"prefixed local", "docker.io/library/nginx@sha256:abc", "sha256:abc", true},
		{"different", "sha256:abc", "sha256:def", false},
		{"both prefixed", "reg.io/app@sha256:abc", "other.io/app@sha256:abc", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := digestsMatch(tt.local, tt.remote); got != tt.match {
				t.Errorf("digestsMatch(%q, %q) = %v, want %v", tt.local, tt.remote, got, tt.match)
			}
		})
	}
}

// Verify mockDockerForRegistry implements docker.API.
var _ docker.API = (*mockDockerForRegistry)(nil)
