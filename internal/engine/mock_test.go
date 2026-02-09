package engine

import (
	"context"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// mockDocker implements docker.API for engine tests.
type mockDocker struct {
	mu sync.Mutex

	containers    []container.Summary
	containersErr error

	inspectResults map[string]container.InspectResponse
	inspectErr     map[string]error

	stopCalls []string
	stopErr   map[string]error

	removeCalls []string
	removeErr   map[string]error

	createResult map[string]string // name → id
	createErr    map[string]error
	createCalls  []string

	startCalls []string
	startErr   map[string]error

	pullCalls []string
	pullErr   map[string]error

	imageDigests   map[string]string
	imageDigestErr map[string]error

	distDigests map[string]string
	distErr     map[string]error
}

func newMockDocker() *mockDocker {
	return &mockDocker{
		inspectResults: make(map[string]container.InspectResponse),
		inspectErr:     make(map[string]error),
		stopErr:        make(map[string]error),
		removeErr:      make(map[string]error),
		createResult:   make(map[string]string),
		createErr:      make(map[string]error),
		startErr:       make(map[string]error),
		pullErr:        make(map[string]error),
		imageDigests:   make(map[string]string),
		imageDigestErr: make(map[string]error),
		distDigests:    make(map[string]string),
		distErr:        make(map[string]error),
	}
}

func (m *mockDocker) ListContainers(_ context.Context) ([]container.Summary, error) {
	return m.containers, m.containersErr
}

func (m *mockDocker) InspectContainer(_ context.Context, id string) (container.InspectResponse, error) {
	if err, ok := m.inspectErr[id]; ok && err != nil {
		return container.InspectResponse{}, err
	}
	return m.inspectResults[id], nil
}

func (m *mockDocker) StopContainer(_ context.Context, id string, _ int) error {
	m.mu.Lock()
	m.stopCalls = append(m.stopCalls, id)
	m.mu.Unlock()
	if err, ok := m.stopErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) RemoveContainer(_ context.Context, id string) error {
	m.mu.Lock()
	m.removeCalls = append(m.removeCalls, id)
	m.mu.Unlock()
	if err, ok := m.removeErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) CreateContainer(_ context.Context, name string, _ *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig) (string, error) {
	m.mu.Lock()
	m.createCalls = append(m.createCalls, name)
	m.mu.Unlock()
	if err, ok := m.createErr[name]; ok {
		return "", err
	}
	if id, ok := m.createResult[name]; ok {
		return id, nil
	}
	return "new-" + name, nil
}

func (m *mockDocker) StartContainer(_ context.Context, id string) error {
	m.mu.Lock()
	m.startCalls = append(m.startCalls, id)
	m.mu.Unlock()
	if err, ok := m.startErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) PullImage(_ context.Context, ref string) error {
	m.mu.Lock()
	m.pullCalls = append(m.pullCalls, ref)
	m.mu.Unlock()
	if err, ok := m.pullErr[ref]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) ImageDigest(_ context.Context, ref string) (string, error) {
	if err, ok := m.imageDigestErr[ref]; ok {
		return "", err
	}
	return m.imageDigests[ref], nil
}

func (m *mockDocker) DistributionDigest(_ context.Context, ref string) (string, error) {
	if err, ok := m.distErr[ref]; ok {
		return "", err
	}
	return m.distDigests[ref], nil
}

func (m *mockDocker) Close() error { return nil }

// mockClock implements clock.Clock for testing.
type mockClock struct {
	now time.Time
}

func newMockClock(t time.Time) *mockClock {
	return &mockClock{now: t}
}

func (c *mockClock) Now() time.Time { return c.now }
func (c *mockClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.now.Add(d)
	return ch
}
func (c *mockClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }
func (c *mockClock) Advance(d time.Duration)         { c.now = c.now.Add(d) }
