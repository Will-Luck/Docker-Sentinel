package agent

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

// fakeHost is a DockerAPI that keeps real container state, so recreate tests
// can check what is left on the host after each step fails: which names
// exist, which image each runs, and whether it is running. It follows the
// daemon rules that matter here: names are unique, stopping an auto-remove
// (--rm) container deletes it, and an empty image reference cannot be pulled.
//
// Create behaves like the daemon's: the image's defaults (ENV, CMD, LABEL,
// VOLUME, EXPOSE) fill what the request leaves empty, a container with no
// hostname gets its short ID, each anonymous volume gets a new volume, a
// user network gets the short-ID alias, and network_mode container:X
// refuses a hostname, ports or endpoints.
type fakeHost struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer // by ID
	images     map[string]string         // locally present image ref -> image ID
	imageIDs   map[string]bool           // every image ID present, tagged or not
	registry   map[string]string         // pullable image ref -> image ID
	nextID     int

	createErr map[string]error // by image ref of the container being created
	startErr  map[string]error // by image ref of the container being started
	stopErr   map[string]error // by container ID
	renameErr map[string]error // by requested new name
	removeErr map[string]error // by container ID

	// lazyAutoRemove makes a stopped --rm container linger (as the daemon
	// can take a moment to delete it): it is gone by the next start.
	lazyAutoRemove bool
	pullCalls      []string
	startCalls     []string
	restartCalls   []string

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
	// removedWithVolumes lists containers removed together with their
	// anonymous volumes.
	removedWithVolumes []string

	// imageConfigs holds each image's defaults, by image ID.
	imageConfigs map[string]*container.Config
	volumeSeq    int
}

type fakeContainer struct {
	id, name   string
	image      string // reference the container was created from
	imageID    string
	running    bool
	autoRemove bool
	env        []string
	labels     map[string]string
	removing   bool // stopped --rm container the daemon is deleting
	started    bool // has been started at some point

	// Set for containers made by the daemon-like create path; nil for the
	// plain ones addContainer makes.
	cfg      *container.Config
	hostCfg  *container.HostConfig
	mounts   []container.MountPoint
	networks map[string]*network.EndpointSettings
	// request is what the agent sent to create this container.
	request *createRequest
	// joined is the ID of the container whose network namespace this one
	// (network_mode container:X) joined when it last started.
	joined string
}

// createRequest is a create call as the agent made it.
type createRequest struct {
	cfg     container.Config
	hostCfg container.HostConfig
	netCfg  *network.NetworkingConfig
}

// fakeID makes the n-th container ID, full length so short-ID rules apply.
func fakeID(n int) string {
	return fmt.Sprintf("%012x%052d", n, 0)
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
		removeErr:  map[string]error{},

		imageConfigs: map[string]*container.Config{},
	}
}

// addImage makes image present locally with the given defaults.
func (h *fakeHost) addImage(ref, id string, defaults *container.Config) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.images[ref] = id
	h.imageIDs[id] = true
	h.imageConfigs[id] = defaults
}

// addFromImage creates a container from a local image through the
// daemon-like create path, as `docker create` with the user's settings
// would, and starts it if running.
func (h *fakeHost) addFromImage(t *testing.T, name, image string, user *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig, running bool) *fakeContainer {
	t.Helper()
	if user == nil {
		user = &container.Config{}
	}
	cfg := *user
	cfg.Image = image
	if hostCfg == nil {
		hostCfg = &container.HostConfig{}
	}
	id, err := h.CreateContainer(context.Background(), name, &cfg, hostCfg, netCfg)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.containers[id]
	c.running, c.started = running, running
	return c
}

// volumeAt returns the name of the volume mounted at dest, or "".
func (c *fakeContainer) volumeAt(dest string) string {
	for _, m := range c.mounts {
		if m.Type == mount.TypeVolume && path.Clean(m.Destination) == path.Clean(dest) {
			return m.Name
		}
	}
	return ""
}

// mergeImageDefaults fills what cfg leaves empty from the image, as the
// daemon does on create.
func mergeImageDefaults(cfg *container.Config, img *container.Config) {
	if img == nil {
		return
	}
	for _, e := range img.Env {
		key, _, _ := strings.Cut(e, "=")
		if !slices.ContainsFunc(cfg.Env, func(c string) bool { return strings.HasPrefix(c, key+"=") }) {
			cfg.Env = append(cfg.Env, e)
		}
	}
	for k, v := range img.Labels {
		if cfg.Labels == nil {
			cfg.Labels = map[string]string{}
		}
		if _, ok := cfg.Labels[k]; !ok {
			cfg.Labels[k] = v
		}
	}
	if len(cfg.Entrypoint) == 0 {
		cfg.Entrypoint = slices.Clone(img.Entrypoint)
		if len(cfg.Cmd) == 0 {
			cfg.Cmd = slices.Clone(img.Cmd)
		}
	}
	for v := range img.Volumes {
		if cfg.Volumes == nil {
			cfg.Volumes = map[string]struct{}{}
		}
		cfg.Volumes[v] = struct{}{}
	}
	for p := range img.ExposedPorts {
		if cfg.ExposedPorts == nil {
			cfg.ExposedPorts = network.PortSet{}
		}
		cfg.ExposedPorts[p] = struct{}{}
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
		id:      fakeID(h.nextID),
		name:    name,
		image:   image,
		imageID: h.images[image],
		running: running,
		started: running,
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
		out = append(out, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Image: c.image, State: state, Labels: c.labels})
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
	cfg := &container.Config{Image: c.image, Env: slices.Clone(c.env), Labels: maps.Clone(c.labels)}
	if c.cfg != nil {
		cp := *c.cfg
		cp.Image = c.image
		cfg = &cp
	}
	hostCfg := &container.HostConfig{}
	if c.hostCfg != nil {
		cp := *c.hostCfg
		hostCfg = &cp
	}
	hostCfg.AutoRemove = c.autoRemove
	return container.InspectResponse{
		ID:              c.id,
		Name:            "/" + c.name,
		Image:           c.imageID,
		State:           &container.State{Running: c.running, StartedAt: startedAt(c.started)},
		Config:          cfg,
		HostConfig:      hostCfg,
		Mounts:          slices.Clone(c.mounts),
		NetworkSettings: &container.NetworkSettings{Networks: c.networks},
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
		if h.lazyAutoRemove {
			c.removing = true
		} else {
			delete(h.containers, id)
		}
	}
	return nil
}

func (h *fakeHost) RemoveContainerWithVolumes(ctx context.Context, id string) error {
	h.mu.Lock()
	c, ok := h.containers[id]
	name := ""
	if ok {
		name = c.name
	}
	h.mu.Unlock()
	if err := h.RemoveContainer(ctx, id); err != nil {
		return err
	}
	h.mu.Lock()
	h.removedWithVolumes = append(h.removedWithVolumes, name)
	h.mu.Unlock()
	return nil
}

// ImageConfig returns an image's defaults, by reference or ID.
func (h *fakeHost) ImageConfig(_ context.Context, ref string) (*container.Config, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := ref
	if byRef, ok := h.images[ref]; ok {
		id = byRef
	}
	cfg, ok := h.imageConfigs[id]
	if !ok {
		return nil, fmt.Errorf("No such image: %s", ref)
	}
	cp := *cfg
	return &cp, nil
}

func (h *fakeHost) RemoveContainer(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.containers[id]
	if !ok {
		return fmt.Errorf("No such container: %s", id)
	}
	if err := h.removeErr[id]; err != nil {
		return err
	}
	if c.running {
		h.removedRunning = append(h.removedRunning, c.name)
	}
	delete(h.containers, id)
	return nil
}

func (h *fakeHost) CreateContainer(_ context.Context, name string, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.byName(name) != nil {
		return "", fmt.Errorf("Conflict. The container name %q is already in use", "/"+name)
	}
	if err := h.createErr[cfg.Image]; err != nil {
		return "", err
	}
	if hostCfg != nil && hostCfg.NetworkMode.IsContainer() &&
		(cfg.Hostname != "" || len(cfg.ExposedPorts) > 0 || (netCfg != nil && len(netCfg.EndpointsConfig) > 0)) {
		return "", fmt.Errorf("conflicting options: hostname, ports and endpoints cannot be used with network mode %s", hostCfg.NetworkMode)
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
		id:         fakeID(h.nextID),
		name:       name,
		image:      cfg.Image,
		imageID:    imageID,
		autoRemove: hostCfg != nil && hostCfg.AutoRemove,
		env:        append([]string(nil), cfg.Env...),
	}
	if img, known := h.imageConfigs[imageID]; known {
		h.materialise(c, cfg, hostCfg, netCfg, img)
	}
	h.containers[c.id] = c
	return c.id, nil
}

// materialise fills in a created container as the daemon would.
func (h *fakeHost) materialise(c *fakeContainer, req *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig, img *container.Config) {
	rec := &createRequest{cfg: *req, netCfg: netCfg}
	if hostCfg != nil {
		rec.hostCfg = *hostCfg
	}
	c.request = rec

	cfg := *req
	cfg.Env = slices.Clone(req.Env)
	cfg.Labels = maps.Clone(req.Labels)
	cfg.Volumes = maps.Clone(req.Volumes)
	cfg.ExposedPorts = maps.Clone(req.ExposedPorts)
	mergeImageDefaults(&cfg, img)
	containerMode := hostCfg != nil && hostCfg.NetworkMode.IsContainer()
	if cfg.Hostname == "" && !containerMode {
		cfg.Hostname = c.id[:12]
	}
	c.cfg = &cfg
	c.env = cfg.Env
	c.labels = cfg.Labels
	hc := container.HostConfig{}
	if hostCfg != nil {
		hc = *hostCfg
	}
	c.hostCfg = &hc

	covered := map[string]bool{}
	for _, m := range hc.Mounts {
		dest := path.Clean(m.Target)
		covered[dest] = true
		if m.Type == mount.TypeVolume {
			name := m.Source
			if name == "" {
				name = h.newVolume()
			}
			c.mounts = append(c.mounts, container.MountPoint{Type: mount.TypeVolume, Name: name, Destination: m.Target, RW: !m.ReadOnly})
		}
	}
	anonymous := map[string]bool{}
	for _, b := range hc.Binds {
		parts := strings.Split(b, ":")
		if len(parts) == 1 {
			anonymous[path.Clean(parts[0])] = true
			continue
		}
		covered[path.Clean(parts[1])] = true
	}
	for v := range cfg.Volumes {
		anonymous[path.Clean(v)] = true
	}
	for _, dest := range slices.Sorted(maps.Keys(anonymous)) {
		if !covered[dest] {
			c.mounts = append(c.mounts, container.MountPoint{Type: mount.TypeVolume, Name: h.newVolume(), Destination: dest, RW: true})
		}
	}

	if containerMode {
		return
	}
	c.networks = map[string]*network.EndpointSettings{}
	if netCfg != nil {
		for name, ep := range netCfg.EndpointsConfig {
			cp := network.EndpointSettings{}
			if ep != nil {
				cp = *ep
				cp.Aliases = slices.Clone(ep.Aliases)
			}
			c.networks[name] = &cp
		}
	}
	mode := string(hc.NetworkMode)
	if mode == "" || mode == "default" || mode == "bridge" {
		if _, ok := c.networks["bridge"]; !ok {
			c.networks["bridge"] = &network.EndpointSettings{}
		}
	}
	for name, ep := range c.networks {
		if name != "bridge" {
			ep.Aliases = append(ep.Aliases, c.id[:12])
		}
	}
}

func (h *fakeHost) newVolume() string {
	h.volumeSeq++
	return fmt.Sprintf("anon-%d", h.volumeSeq)
}

func (h *fakeHost) StartContainer(_ context.Context, id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.startCalls = append(h.startCalls, id)
	c, ok := h.containers[id]
	if !ok {
		return fmt.Errorf("No such container: %s", id)
	}
	if c.removing {
		// The daemon finished deleting the stopped --rm container.
		delete(h.containers, id)
		return fmt.Errorf("No such container: %s", id)
	}
	if err := h.startErr[c.image]; err != nil {
		return err
	}
	if c.hostCfg != nil && c.hostCfg.NetworkMode.IsContainer() {
		// The daemon resolves the provider by name or ID prefix on every
		// start and joins its namespace; a removed one cannot be joined.
		ref := c.hostCfg.NetworkMode.ConnectedContainer()
		p := h.resolve(ref)
		if p == nil {
			return fmt.Errorf("No such container: %s", ref)
		}
		if !p.running {
			return fmt.Errorf("cannot join network namespace of a non running container %s", ref)
		}
		c.joined = p.id
	}
	c.running = true
	c.started = true
	return nil
}

// resolve finds a container by name or ID prefix (12 or more digits).
func (h *fakeHost) resolve(ref string) *fakeContainer {
	if c := h.byName(ref); c != nil {
		return c
	}
	for _, c := range h.containers {
		if c.id == ref || (len(ref) >= 12 && strings.HasPrefix(c.id, ref)) {
			return c
		}
	}
	return nil
}

// startedAt is the inspect StartedAt of a container that has (or has not)
// been started.
func startedAt(started bool) string {
	if started {
		return "2026-10-01T12:00:00Z"
	}
	return "0001-01-01T00:00:00Z"
}

func (h *fakeHost) RestartContainer(ctx context.Context, id string) error {
	h.mu.Lock()
	if c, ok := h.containers[id]; ok {
		c.running = false
	}
	h.restartCalls = append(h.restartCalls, id)
	h.mu.Unlock()
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
