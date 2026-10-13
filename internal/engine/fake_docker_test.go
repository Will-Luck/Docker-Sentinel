package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// fakeContainer is one container known to fakeDocker.
type fakeContainer struct {
	id      string
	name    string
	imageID string
	cfg     *container.Config
	host    *container.HostConfig
	mounts  []container.MountPoint
	running bool
}

// fakeDocker is a stateful stand-in for the daemon, unlike mockDocker,
// which answers from fixed tables. Containers are created, renamed, stopped
// and removed for real: names are unique, a --rm container is deleted when
// it stops, inspect works by ID or name and returns not-found for a missing
// container. Image lookups, pulls, tags and the calls it does not model come
// from the embedded mockDocker.
type fakeDocker struct {
	*mockDocker

	fmu        sync.Mutex
	containers map[string]*fakeContainer
	seq        int
	creates    int
	log        []string

	failCreate        map[int]error    // nth create (1-based)
	failStart         map[string]error // by container ID
	failStop          map[string]error // by container ID; the container keeps running
	stopErrStopped    map[string]error // by container ID; the container stops anyway
	failRename        map[string]error // by target name
	failRenameOnce    map[string]error // by target name, only the first time
	restartUpdateErr  error
	removeVolumeCalls []string
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		mockDocker: newMockDocker(),
		containers: make(map[string]*fakeContainer),
		failCreate: make(map[int]error),
		failStart:  make(map[string]error),
		failStop:   make(map[string]error),
		failRename: make(map[string]error),

		stopErrStopped: make(map[string]error),
		failRenameOnce: make(map[string]error),
	}
}

func notFound(what string) error {
	return cerrdefs.ErrNotFound.WithMessage("No such container: " + what)
}

// add puts a container in the fake and returns it.
func (f *fakeDocker) add(id, name, imageRef string, running bool, cfg *container.Config, host *container.HostConfig) *fakeContainer {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	if cfg == nil {
		cfg = &container.Config{}
	}
	if cfg.Image == "" {
		cfg.Image = imageRef
	}
	if host == nil {
		host = &container.HostConfig{}
	}
	c := &fakeContainer{id: id, name: name, imageID: f.imageIDs[imageRef], cfg: cfg, host: host, running: running}
	if c.imageID == "" {
		c.imageID = imageRef
	}
	f.containers[id] = c
	return c
}

func (f *fakeDocker) record(op string) {
	f.log = append(f.log, op)
}

func (f *fakeDocker) find(idOrName string) *fakeContainer {
	if c, ok := f.containers[idOrName]; ok {
		return c
	}
	for _, c := range f.containers {
		if c.name == idOrName {
			return c
		}
	}
	return nil
}

func (f *fakeDocker) byName(name string) *fakeContainer {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	for _, c := range f.containers {
		if c.name == name {
			return c
		}
	}
	return nil
}

// names lists "name(state)" for every container, sorted, for messages.
func (f *fakeDocker) names() []string {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	var out []string
	for _, c := range f.containers {
		state := "exited"
		if c.running {
			state = "running"
		}
		out = append(out, fmt.Sprintf("%s[%s,%s]", c.name, c.id, state))
	}
	slices.Sort(out)
	return out
}

func (f *fakeDocker) runningWithName(name string) int {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	n := 0
	for _, c := range f.containers {
		if c.running && strings.TrimSuffix(c.name, replacedSuffix) == name {
			n++
		}
	}
	return n
}

func (f *fakeDocker) summary(c *fakeContainer) container.Summary {
	state := container.StateExited
	if c.running {
		state = container.StateRunning
	}
	s := container.Summary{
		ID:      c.id,
		Names:   []string{"/" + c.name},
		Image:   c.cfg.Image,
		ImageID: c.imageID,
		Labels:  maps.Clone(c.cfg.Labels),
		State:   state,
	}
	s.HostConfig.NetworkMode = string(c.host.NetworkMode)
	return s
}

func (f *fakeDocker) ListContainers(_ context.Context) ([]container.Summary, error) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	var out []container.Summary
	for _, id := range slices.Sorted(maps.Keys(f.containers)) {
		if c := f.containers[id]; c.running {
			out = append(out, f.summary(c))
		}
	}
	return out, nil
}

func (f *fakeDocker) ListAllContainers(_ context.Context) ([]container.Summary, error) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	var out []container.Summary
	for _, id := range slices.Sorted(maps.Keys(f.containers)) {
		out = append(out, f.summary(f.containers[id]))
	}
	return out, nil
}

func (f *fakeDocker) InspectContainer(_ context.Context, idOrName string) (container.InspectResponse, error) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	c := f.find(idOrName)
	if c == nil {
		return container.InspectResponse{}, notFound(idOrName)
	}
	host := *c.host
	return container.InspectResponse{
		ID:              c.id,
		Name:            "/" + c.name,
		Image:           c.imageID,
		State:           &container.State{Running: c.running},
		Config:          cloneConfig(c.cfg),
		HostConfig:      &host,
		NetworkSettings: &container.NetworkSettings{},
		Mounts:          slices.Clone(c.mounts),
	}, nil
}

func (f *fakeDocker) CreateContainer(_ context.Context, name string, cfg *container.Config, hostCfg *container.HostConfig, _ *network.NetworkingConfig) (string, error) {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.creates++
	f.record("create " + name)
	if err, ok := f.failCreate[f.creates]; ok {
		return "", err
	}
	for _, c := range f.containers {
		if c.name == name {
			return "", cerrdefs.ErrConflict.WithMessage("name " + name + " is already in use")
		}
	}
	f.seq++
	id := fmt.Sprintf("%s-%d", name, f.seq)
	host := &container.HostConfig{}
	if hostCfg != nil {
		h := *hostCfg
		host = &h
	}
	imageID := f.imageIDs[cfg.Image]
	if imageID == "" {
		imageID = cfg.Image
	}
	f.containers[id] = &fakeContainer{id: id, name: name, imageID: imageID, cfg: cloneConfig(cfg), host: host}
	return id, nil
}

func (f *fakeDocker) StartContainer(_ context.Context, id string) error {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.record("start " + id)
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	if err, ok := f.failStart[id]; ok {
		return err
	}
	c.running = true
	return nil
}

func (f *fakeDocker) StopContainer(_ context.Context, id string, _ int) error {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.record("stop " + id)
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	if err, ok := f.failStop[id]; ok {
		return err
	}
	c.running = false
	if c.host.AutoRemove {
		delete(f.containers, c.id)
	}
	return f.stopErrStopped[id]
}

func (f *fakeDocker) RestartContainer(_ context.Context, id string) error {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.record("restart " + id)
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	c.running = true
	return nil
}

func (f *fakeDocker) remove(id string, withVolumes bool) error {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	op := "remove "
	if withVolumes {
		op = "remove-with-volumes "
		f.removeVolumeCalls = append(f.removeVolumeCalls, id)
	}
	f.record(op + id)
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	delete(f.containers, c.id)
	return nil
}

func (f *fakeDocker) RemoveContainer(_ context.Context, id string) error {
	return f.remove(id, false)
}

func (f *fakeDocker) RemoveContainerWithVolumes(_ context.Context, id string) error {
	return f.remove(id, true)
}

func (f *fakeDocker) RenameContainer(_ context.Context, id, newName string) error {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.record("rename " + id + " " + newName)
	if err, ok := f.failRename[newName]; ok {
		return err
	}
	if err, ok := f.failRenameOnce[newName]; ok {
		delete(f.failRenameOnce, newName)
		return err
	}
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	for _, other := range f.containers {
		if other.name == newName && other != c {
			return cerrdefs.ErrConflict.WithMessage("name " + newName + " is already in use")
		}
	}
	c.name = newName
	return nil
}

func (f *fakeDocker) UpdateRestartPolicy(_ context.Context, id string, p container.RestartPolicy) error {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	f.record("restart-policy " + id + " " + string(p.Name))
	if f.restartUpdateErr != nil {
		return f.restartUpdateErr
	}
	c := f.find(id)
	if c == nil {
		return notFound(id)
	}
	c.host.RestartPolicy = p
	return nil
}

// runRollback rolls name back to the snapshot in data, without a journal.
func runRollback(d docker.API, name string, data []byte) error {
	_, err := rollbackContainer(context.Background(), d, nil, name, data, logging.New(false))
	return err
}

// ops returns the fake's call log.
func (f *fakeDocker) ops() []string {
	f.fmu.Lock()
	defer f.fmu.Unlock()
	return slices.Clone(f.log)
}

// newFakeUpdater returns an Updater over a fakeDocker, with a 1s grace
// period and the manual default policy.
func newFakeUpdater(t *testing.T, f *fakeDocker) *Updater {
	t.Helper()
	s := testStore(t)
	log := logging.New(false)
	cfg := config.NewTestConfig()
	cfg.SetDefaultPolicy("manual")
	cfg.SetGracePeriod(time.Second)
	return NewUpdater(f, registry.NewChecker(f, log), s, NewQueue(s, nil, nil), cfg, log,
		newMockClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), notify.NewMulti(log), nil)
}

// assertFakeOps checks that want appear in the fake's call log in order.
func assertFakeOps(t *testing.T, f *fakeDocker, want ...string) {
	t.Helper()
	ops, last := f.ops(), -1
	for _, op := range want {
		i := slices.Index(ops[last+1:], op)
		if i < 0 {
			t.Fatalf("want %v in this order, got ops=%v", want, ops)
		}
		last += i + 1
	}
}

func assertNoFakeOp(t *testing.T, f *fakeDocker, op string) {
	t.Helper()
	if slices.Contains(f.ops(), op) {
		t.Errorf("unexpected %q: ops=%v", op, f.ops())
	}
}
