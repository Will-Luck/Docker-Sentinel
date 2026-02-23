package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"
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

	createResult  map[string]string // name → id
	createErr     map[string]error
	createCalls   []string
	createConfigs map[string]*container.Config

	startCalls []string
	startErr   map[string]error

	restartCalls []string
	restartErr   map[string]error

	pullCalls []string
	pullErr   map[string]error

	imageDigests   map[string]string
	imageDigestErr map[string]error

	distDigests map[string]string
	distErr     map[string]error

	removeImageCalls []string
	removeImageErr   map[string]error

	tagImageCalls []string
	tagImageErr   map[string]error

	execCalls   []string
	execResults map[string]struct {
		exitCode int
		output   string
	}
	execErr map[string]error

	// Swarm mock fields
	swarmManager     bool
	services         []swarm.Service
	servicesErr      error
	inspectService   map[string]swarm.Service
	inspectSvcErr    map[string]error
	updateSvcCalls   []string
	updateSvcErr     map[string]error
	rollbackSvcCalls []string
	rollbackSvcErr   map[string]error
	serviceTasks     map[string][]swarm.Task
	serviceTasksErr  map[string]error
	nodes            []swarm.Node
	nodesErr         error
}

func newMockDocker() *mockDocker {
	return &mockDocker{
		inspectResults: make(map[string]container.InspectResponse),
		inspectErr:     make(map[string]error),
		stopErr:        make(map[string]error),
		removeErr:      make(map[string]error),
		createResult:   make(map[string]string),
		createErr:      make(map[string]error),
		createConfigs:  make(map[string]*container.Config),
		startErr:       make(map[string]error),
		restartErr:     make(map[string]error),
		pullErr:        make(map[string]error),
		imageDigests:   make(map[string]string),
		imageDigestErr: make(map[string]error),
		distDigests:    make(map[string]string),
		distErr:        make(map[string]error),
		removeImageErr: make(map[string]error),
		tagImageErr:    make(map[string]error),
		execResults: make(map[string]struct {
			exitCode int
			output   string
		}),
		execErr:         make(map[string]error),
		inspectService:  make(map[string]swarm.Service),
		inspectSvcErr:   make(map[string]error),
		updateSvcErr:    make(map[string]error),
		rollbackSvcErr:  make(map[string]error),
		serviceTasks:    make(map[string][]swarm.Task),
		serviceTasksErr: make(map[string]error),
	}
}

func (m *mockDocker) ListContainers(_ context.Context) ([]container.Summary, error) {
	return m.containers, m.containersErr
}

func (m *mockDocker) ListAllContainers(_ context.Context) ([]container.Summary, error) {
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

func (m *mockDocker) CreateContainer(_ context.Context, name string, cfg *container.Config, _ *container.HostConfig, _ *network.NetworkingConfig) (string, error) {
	m.mu.Lock()
	m.createCalls = append(m.createCalls, name)
	if cfg != nil {
		m.createConfigs[name] = cfg
	}
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

func (m *mockDocker) RestartContainer(_ context.Context, id string) error {
	m.mu.Lock()
	m.restartCalls = append(m.restartCalls, id)
	m.mu.Unlock()
	if err, ok := m.restartErr[id]; ok {
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

func (m *mockDocker) RemoveImage(_ context.Context, id string) error {
	m.mu.Lock()
	m.removeImageCalls = append(m.removeImageCalls, id)
	m.mu.Unlock()
	if err, ok := m.removeImageErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) TagImage(_ context.Context, src, target string) error {
	m.mu.Lock()
	m.tagImageCalls = append(m.tagImageCalls, src+"->"+target)
	m.mu.Unlock()
	if err, ok := m.tagImageErr[src]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) ExecContainer(_ context.Context, id string, cmd []string, _ int) (int, string, error) {
	m.mu.Lock()
	m.execCalls = append(m.execCalls, id)
	m.mu.Unlock()
	if err, ok := m.execErr[id]; ok {
		return -1, "", err
	}
	if r, ok := m.execResults[id]; ok {
		return r.exitCode, r.output, nil
	}
	return 0, "", nil
}

func (m *mockDocker) IsSwarmManager(_ context.Context) bool {
	return m.swarmManager
}

func (m *mockDocker) ListServices(_ context.Context) ([]swarm.Service, error) {
	return m.services, m.servicesErr
}

func (m *mockDocker) InspectService(_ context.Context, id string) (swarm.Service, error) {
	if err, ok := m.inspectSvcErr[id]; ok && err != nil {
		return swarm.Service{}, err
	}
	if svc, ok := m.inspectService[id]; ok {
		return svc, nil
	}
	return swarm.Service{}, fmt.Errorf("service %s not found", id)
}

func (m *mockDocker) UpdateService(_ context.Context, id string, _ swarm.Version, _ swarm.ServiceSpec, _ string) error {
	m.mu.Lock()
	m.updateSvcCalls = append(m.updateSvcCalls, id)
	m.mu.Unlock()
	if err, ok := m.updateSvcErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) RollbackService(_ context.Context, id string, _ swarm.Version, _ swarm.ServiceSpec) error {
	m.mu.Lock()
	m.rollbackSvcCalls = append(m.rollbackSvcCalls, id)
	m.mu.Unlock()
	if err, ok := m.rollbackSvcErr[id]; ok {
		return err
	}
	return nil
}

func (m *mockDocker) ListServiceTasks(_ context.Context, serviceID string) ([]swarm.Task, error) {
	if err, ok := m.serviceTasksErr[serviceID]; ok && err != nil {
		return nil, err
	}
	return m.serviceTasks[serviceID], nil
}

func (m *mockDocker) ListNodes(_ context.Context) ([]swarm.Node, error) {
	return m.nodes, m.nodesErr
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
