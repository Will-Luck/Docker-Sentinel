package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// fakeHost is a DockerAPI that keeps real container state, so recreate tests
// can check what is left on the host after each step fails: which names
// exist, which image each runs, and whether it is running. It follows the
// daemon rules that matter here: names are unique, stopping an auto-remove
// (--rm) container deletes it, and an empty image reference cannot be pulled.
type fakeHost struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer // by ID
	images     map[string]string         // locally present image ref -> image ID
	imageIDs   map[string]bool           // every image ID present, tagged or not
	registry   map[string]string         // pullable image ref -> image ID
	nextID     int

	createErr  map[string]error // by image ref of the container being created
	startErr   map[string]error // by image ref of the container being started
	stopErr    map[string]error // by container ID
	renameErr  map[string]error // by requested new name
	pullCalls  []string
	startCalls []string

	// afterInspect, if set, runs after every inspect (outside the lock),
	// so a test can change the host between the agent's steps.
	afterInspect func(id string)
	// pullGate, if set, blocks every pull until it is closed; pullWaiting
	// (if set) gets the reference of each pull as it starts to wait.
	pullGate    chan struct{}
	pullWaiting chan string
	// removedRunning lists containers removed while running: a forced
	// remove that kills a live container.
	removedRunning []string
}

type fakeContainer struct {
	id, name   string
	image      string // reference the container was created from
	imageID    string
	running    bool
	autoRemove bool
	env        []string
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		containers: map[string]*fakeContainer{},
		images:     map[string]string{},
		imageIDs:   map[string]bool{},
		registry:   map[string]string{},
		createErr:  map[string]error{},
		startErr:   map[string]error{},
		stopErr:    map[string]error{},
		renameErr:  map[string]error{},
	}
}

// addContainer puts a container on the host whose image is already local.
func (h *fakeHost) addContainer(name, image string, running bool) *fakeContainer {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.images[image]; !ok {
		h.images[image] = "sha256:" + strings.ReplaceAll(image, ":", "-") + "-v1"
		h.imageIDs[h.images[image]] = true
	}
	h.nextID++
	c := &fakeContainer{
		id:      fmt.Sprintf("c%d", h.nextID),
		name:    name,
		image:   image,
		imageID: h.images[image],
		running: running,
		env:     []string{"APP_MODE=prod"},
	}
	h.containers[c.id] = c
	return c
}

func (h *fakeHost) byName(name string) *fakeContainer {
	for _, c := range h.containers {
		if c.name == name {
			return c
		}
	}
	return nil
}

// names lists every container name on the host, sorted.
func (h *fakeHost) names() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.containers {
		out = append(out, c.name)
	}
	sort.Strings(out)
	return out
}

// requireOnly fails unless the host holds exactly one container, named
// name, created from image, and running exactly when running is true.
func (h *fakeHost) requireOnly(t *testing.T, name, image string, running bool) {
	t.Helper()
	if got := h.names(); len(got) != 1 || got[0] != name {
		t.Fatalf("containers on host = %v, want only %q", got, name)
	}
	h.mu.Lock()
	c := h.byName(name)
	h.mu.Unlock()
	if c.image != image {
		t.Errorf("%s runs image %q, want %q", name, c.image, image)
	}
	if c.running != running {
		t.Errorf("%s running = %v, want %v", name, c.running, running)
	}
}

func (h *fakeHost) summaries(onlyRunning bool) []container.Summary {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []container.Summary
	for _, c := range h.containers {
		if onlyRunning && !c.running {
			continue
		}
		state := container.StateExited
		if c.running {
			state = container.StateRunning
		}
		out = append(out, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Image: c.image, State: state})
	}
	return out
}

func (h *fakeHost) ListContainers(context.Context) ([]container.Summary, error) {
	return h.summaries(true), nil
}

func (h *fakeHost) ListAllContainers(context.Context) ([]container.Summary, error) {
	return h.summaries(false), nil
}

func (h *fakeHost) InspectContainer(_ context.Context, id string) (container.InspectResponse, error) {
	if h.afterInspect != nil {
		defer h.afterInspect(id)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.containers[id]
	if !ok {
		return container.InspectResponse{}, fmt.Errorf("No such container: %s", id)
	}
	return container.InspectResponse{
		ID:         c.id,
		Name:       "/" + c.name,
		Image:      c.imageID,
		State:      &container.State{Running: c.running},
		Config:     &container.Config{Image: c.image, Env: append([]string(nil), c.env...)},
		HostConfig: &container.HostConfig{AutoRemove: c.autoRemove},
	}, nil
}

func (h *fakeHost) StopContainer(_ context.Context, id string, _ int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.containers[id]
	if !ok {
		return fmt.Errorf("No such container: %s", id)
	}
	if err := h.stopErr[id]; err != nil {
		return err
	}
	if !c.running {
		return nil // the daemon answers 304 and changes nothing
	}
	c.running = false
	if c.autoRemove {
		delete(h.containers, id)
	}
	return nil
}

func (h *fakeHost) RemoveContainer(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.containers[id]
	if !ok {
		return fmt.Errorf("No such container: %s", id)
	}
	if c.running {
		h.removedRunning = append(h.removedRunning, c.name)
	}
	delete(h.containers, id)
	return nil
}

func (h *fakeHost) CreateContainer(_ context.Context, name string, cfg *container.Config, hostCfg *container.HostConfig, _ *network.NetworkingConfig) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byName(name) != nil {
		return "", fmt.Errorf("Conflict. The container name %q is already in use", "/"+name)
	}
	if err := h.createErr[cfg.Image]; err != nil {
		return "", err
	}
	imageID, ok := h.images[cfg.Image]
	if !ok && h.imageIDs[cfg.Image] {
		// An image ID stays usable after its tag moves to a newer image.
		imageID, ok = cfg.Image, true
	}
	if !ok {
		return "", fmt.Errorf("No such image: %s", cfg.Image)
	}
	h.nextID++
	c := &fakeContainer{
		id:         fmt.Sprintf("c%d", h.nextID),
		name:       name,
		image:      cfg.Image,
		imageID:    imageID,
		autoRemove: hostCfg != nil && hostCfg.AutoRemove,
		env:        append([]string(nil), cfg.Env...),
	}
	h.containers[c.id] = c
	return c.id, nil
}

func (h *fakeHost) StartContainer(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.startCalls = append(h.startCalls, id)
	c, ok := h.containers[id]
	if !ok {
		return fmt.Errorf("No such container: %s", id)
	}
	if err := h.startErr[c.image]; err != nil {
		return err
	}
	c.running = true
	return nil
}

func (h *fakeHost) RestartContainer(ctx context.Context, id string) error {
	return h.StartContainer(ctx, id)
}

func (h *fakeHost) RenameContainer(_ context.Context, id, newName string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.containers[id]
	if !ok {
		return fmt.Errorf("No such container: %s", id)
	}
	if err := h.renameErr[newName]; err != nil {
		return err
	}
	if other := h.byName(newName); other != nil && other != c {
		return fmt.Errorf("Conflict. The container name %q is already in use", "/"+newName)
	}
	c.name = newName
	return nil
}

func (h *fakeHost) NetworkConnect(context.Context, string, string) error { return nil }

func (h *fakeHost) PullImage(_ context.Context, ref string) error {
	if h.pullGate != nil {
		if h.pullWaiting != nil {
			h.pullWaiting <- ref
		}
		<-h.pullGate
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pullCalls = append(h.pullCalls, ref)
	if ref == "" {
		return fmt.Errorf("invalid reference format")
	}
	id, ok := h.registry[ref]
	if !ok {
		return fmt.Errorf("manifest unknown: %s", ref)
	}
	h.images[ref] = id
	h.imageIDs[id] = true
	return nil
}

func (h *fakeHost) ImageDigest(_ context.Context, ref string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if id, ok := h.images[ref]; ok {
		return id, nil
	}
	return "", fmt.Errorf("No such image: %s", ref)
}

func (h *fakeHost) ExecContainer(context.Context, string, []string, int) (int, string, error) {
	return 0, "", nil
}

func (h *fakeHost) ContainerLogs(context.Context, string, int) (string, error) { return "", nil }

func (h *fakeHost) EngineID(context.Context) (string, error) { return "fake-engine", nil }

// startByID marks a container running, as `docker start <id>` would.
func (h *fakeHost) startByID(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.containers[id]; ok {
		c.running = true
	}
}
