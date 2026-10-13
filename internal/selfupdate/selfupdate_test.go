package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

type nopLog struct{}

func (nopLog) Info(string, ...any)  {}
func (nopLog) Warn(string, ...any)  {}
func (nopLog) Error(string, ...any) {}
func (nopLog) Debug(string, ...any) {}

// fake is a Docker double that records calls in order.
type fake struct {
	mu      sync.Mutex
	events  []string
	created map[string]*container.Config
	hosts   map[string]*container.HostConfig
	nets    map[string]*network.NetworkingConfig
	names   map[string]string // id -> current name
	list    []container.Summary
	state   map[string]*container.State // inspect results by id
	// failures
	stopErr, startErr map[string]error
	createErr         map[string]error
	nextID            int
	onStop            func()
}

func newFake() *fake {
	return &fake{
		created: map[string]*container.Config{}, hosts: map[string]*container.HostConfig{},
		nets: map[string]*network.NetworkingConfig{}, names: map[string]string{},
		state: map[string]*container.State{}, stopErr: map[string]error{}, startErr: map[string]error{},
		createErr: map[string]error{},
	}
}

func (f *fake) note(s string) { f.mu.Lock(); f.events = append(f.events, s); f.mu.Unlock() }

func (f *fake) ListAllContainers(context.Context) ([]container.Summary, error) { return f.list, nil }
func (f *fake) InspectContainer(_ context.Context, id string) (container.InspectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.state[id]
	if !ok || st == nil {
		return container.InspectResponse{}, errors.New("no such container")
	}
	cp := *st
	if st.Health != nil {
		h := *st.Health
		cp.Health = &h
	}
	return container.InspectResponse{ID: id, State: &cp}, nil
}
func (f *fake) StopContainer(_ context.Context, id string, _ int) error {
	f.note("stop:" + id)
	if f.onStop != nil {
		f.onStop()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.stopErr[id]; err != nil {
		return err
	}
	if st := f.state[id]; st != nil {
		st.Running = false
	}
	return nil
}
func (f *fake) RemoveContainer(_ context.Context, id string) error {
	f.note("remove:" + id)
	f.mu.Lock()
	delete(f.state, id)
	f.list = slices.DeleteFunc(slices.Clone(f.list), func(c container.Summary) bool { return c.ID == id })
	f.mu.Unlock()
	return nil
}
func (f *fake) CreateContainer(_ context.Context, name string, cfg *container.Config, h *container.HostConfig, n *network.NetworkingConfig) (string, error) {
	if err := f.createErr[name]; err != nil {
		return "", err
	}
	for _, c := range f.list { // the daemon refuses a name that is taken
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				return "", fmt.Errorf("name %q is already in use", name)
			}
		}
	}
	f.nextID++
	id := fmt.Sprintf("id-%s", name)
	f.created[id], f.hosts[id], f.nets[id], f.names[id] = cfg, h, n, name
	f.note("create:" + name)
	return id, nil
}
func (f *fake) StartContainer(_ context.Context, id string) error {
	f.note("start:" + id)
	if err := f.startErr[id]; err != nil {
		return err
	}
	f.mu.Lock()
	f.state[id] = &container.State{Running: true}
	f.mu.Unlock()
	return nil
}
func (f *fake) RenameContainer(_ context.Context, id, name string) error {
	f.note("rename:" + id + ">" + name)
	f.names[id] = name
	return nil
}

func hubInspect() container.InspectResponse {
	return container.InspectResponse{
		ID: "old1", Image: "sha256:running-image-id",
		Config: &container.Config{
			Image: "ghcr.io/example/sentinel:2.0.0", Env: []string{"SENTINEL_WEB_PORT=8080"},
			Labels: map[string]string{"sentinel.self": "true"},
		},
		HostConfig: &container.HostConfig{
			PortBindings:  map[network.Port][]network.PortBinding{network.MustParsePort("8080/tcp"): {{HostPort: "8080"}}},
			RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		},
		Mounts: []container.MountPoint{
			{Type: mount.TypeBind, Source: "/run/docker.sock", Destination: "/var/run/docker.sock"},
			{Type: mount.TypeVolume, Name: "sentinel-data", Destination: "/data"},
		},
	}
}

func params(f *fake) Params {
	return Params{
		Docker: f, Log: nopLog{}, Inspect: hubInspect(), Name: "sentinel",
		Image: "ghcr.io/example/sentinel:3.0.0", DataDir: "/data",
		Delay: time.Millisecond, Settle: 30 * time.Millisecond, Poll: time.Millisecond,
		Watchdog: 50 * time.Millisecond,
	}
}

func TestBeginSetsUpReplacementAndHelper(t *testing.T) {
	f := newFake()
	h, err := Begin(context.Background(), params(f))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h.OldName(), "sentinel-old-") {
		t.Errorf("old name = %q", h.OldName())
	}

	// The replacement is created, not started, with the ports and the marker.
	newCfg, newHost := f.created["id-sentinel"], f.hosts["id-sentinel"]
	if newCfg == nil || newCfg.Image != "ghcr.io/example/sentinel:3.0.0" {
		t.Fatalf("replacement config = %+v", newCfg)
	}
	if !slices.Contains(newCfg.Env, "SENTINEL_SELF_UPDATE_REPLACEMENT=1") {
		t.Errorf("replacement env = %v, want the self-update marker", newCfg.Env)
	}
	if len(newHost.PortBindings) != 1 {
		t.Errorf("replacement lost its published ports: %+v", newHost.PortBindings)
	}
	if slices.Contains(f.events, "start:id-sentinel") {
		t.Error("the replacement was started by the running instance; only the helper may start it")
	}

	// The helper runs the new image's helper subcommand with the socket and data mounted.
	// The helper runs the image already running here, never the target: a
	// target older than v3 has no helper subcommand.
	hc, hh := f.created["id-sentinel-self-update-helper"], f.hosts["id-sentinel-self-update-helper"]
	if hc == nil {
		t.Fatal("no helper created")
	}
	if hc.Image != "sha256:running-image-id" || !slices.Equal(hc.Cmd, []string{"self-update-helper"}) {
		t.Errorf("helper image/cmd = %q %v", hc.Image, hc.Cmd)
	}
	if hc.Labels["sentinel.self"] != "false" || hc.Labels[HelperLabel] != "true" {
		t.Errorf("helper labels = %v; it must not look like Sentinel", hc.Labels)
	}
	if !hh.AutoRemove || hh.NetworkMode != "none" {
		t.Errorf("helper host config = %+v", hh)
	}
	if !slices.Contains(hh.Binds, "/run/docker.sock:/var/run/docker.sock") || !slices.Contains(hh.Binds, "sentinel-data:/sentinel-data") {
		t.Errorf("helper binds = %v", hh.Binds)
	}
	if !slices.Contains(f.events, "start:id-sentinel-self-update-helper") {
		t.Errorf("helper not started: %v", f.events)
	}
	if len(hc.Env) != 1 || !strings.HasPrefix(hc.Env[0], SpecEnv+"=") || !strings.Contains(hc.Env[0], `"new_id":"id-sentinel"`) {
		t.Errorf("helper env = %v", hc.Env)
	}
}

func TestBeginWithoutSocketMountReportsErrNoSocket(t *testing.T) {
	f := newFake()
	p := params(f)
	p.Inspect.Mounts = nil
	if _, err := Begin(context.Background(), p); !errors.Is(err, ErrNoSocket) {
		t.Fatalf("err = %v, want ErrNoSocket", err)
	}
	if len(f.events) != 0 {
		t.Errorf("nothing may change without a socket: %v", f.events)
	}
}

func TestBeginHelperStartFailureRestoresEverything(t *testing.T) {
	f := newFake()
	f.startErr["id-sentinel-self-update-helper"] = errors.New("no such image")
	if _, err := Begin(context.Background(), params(f)); err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"remove:id-sentinel-self-update-helper", "remove:id-sentinel"} {
		if !slices.Contains(f.events, want) {
			t.Errorf("missing %q in %v", want, f.events)
		}
	}
	if f.names["old1"] != "sentinel" {
		t.Errorf("running container left named %q", f.names["old1"])
	}
}

// If the helper ends while the old instance still runs, the update did not
// happen: Wait says so and puts things back.
func TestWaitReportsHelperThatEndedWithoutReplacingUs(t *testing.T) {
	f := newFake()
	h, err := Begin(context.Background(), params(f))
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	delete(f.state, "id-sentinel-self-update-helper") // the helper has gone
	f.mu.Unlock()
	if err := h.Wait(context.Background()); err == nil {
		t.Fatal("Wait returned nil although this instance is still running")
	}
	if f.names["old1"] != "sentinel" || !slices.Contains(f.events, "remove:id-sentinel") {
		t.Errorf("not restored: name=%q events=%v", f.names["old1"], f.events)
	}
}

func TestWaitWatchdogRemovesAHangingHelper(t *testing.T) {
	f := newFake()
	h, err := Begin(context.Background(), params(f))
	if err != nil {
		t.Fatal(err)
	}
	err = h.Wait(context.Background()) // the helper stays "running"
	if err == nil || !strings.Contains(err.Error(), "did not replace") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Contains(f.events, "remove:id-sentinel-self-update-helper") {
		t.Errorf("hanging helper not removed: %v", f.events)
	}
}

func spec() Spec {
	return Spec{
		OldID: "old1", OldName: "sentinel-old-1", FinalName: "sentinel", NewID: "new1",
		OldImage: "img:2", NewImage: "img:3", Settle: 20 * time.Millisecond, StopTimeout: 5,
	}
}

func newRunner(f *fake) Runner { return Runner{Docker: f, Log: nopLog{}, Poll: time.Millisecond} }

func TestRunStopsOldBeforeStartingNewThenRemovesOld(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	s := spec()
	s.OutcomeDir = t.TempDir()

	out := newRunner(f).Run(context.Background(), s)

	if out.Result != ResultSuccess {
		t.Fatalf("outcome = %+v", out)
	}
	want := []string{"policy:old1:always", "stop:old1", "start:new1", "remove:old1"}
	if !slices.Equal(f.events, want) {
		t.Errorf("order = %v, want %v (stop first frees the ports and the database lock)", f.events, want)
	}
	if _, err := os.Stat(filepath.Join(s.OutcomeDir, OutcomeFile)); err != nil {
		t.Errorf("outcome file not written: %v", err)
	}
}

func TestRunRollsBackWhenNewContainerWillNotStart(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	f.startErr["new1"] = errors.New("port is already allocated")

	out := newRunner(f).Run(context.Background(), spec())

	if out.Result != ResultRolledBack {
		t.Fatalf("outcome = %+v", out)
	}
	if f.names["old1"] != "sentinel" || !slices.Contains(f.events, "remove:new1") || !slices.Contains(f.events, "start:old1") {
		t.Errorf("not restored: name=%q events=%v", f.names["old1"], f.events)
	}
}

func TestRunRollsBackWhenNewContainerDoesNotStayUp(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	f.startErr["new1"] = nil
	r := newRunner(f)
	// The new container dies shortly after it starts.
	go func() {
		for i := 0; i < 1000; i++ {
			f.mu.Lock()
			if st := f.state["new1"]; st != nil {
				st.Running = false
			}
			f.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	s := spec()
	s.Settle = 100 * time.Millisecond

	out := r.Run(context.Background(), s)

	if out.Result != ResultRolledBack || !strings.Contains(out.Error, "stopped") {
		t.Fatalf("outcome = %+v", out)
	}
	if !slices.Contains(f.events, "remove:new1") {
		t.Errorf("new container not removed: %v", f.events)
	}
}

func TestRunRollsBackWhenNewContainerIsUnhealthy(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	f.state["new1"] = nil
	r := newRunner(f)
	f.startErr["new1"] = nil
	go func() {
		for i := 0; i < 1000; i++ {
			f.mu.Lock()
			if st := f.state["new1"]; st != nil {
				st.Health = &container.Health{Status: container.Unhealthy}
			}
			f.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	s := spec()
	s.Settle = 100 * time.Millisecond

	out := r.Run(context.Background(), s)
	if out.Result != ResultRolledBack || !strings.Contains(out.Error, "unhealthy") {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestRunGivesUpWhenOldContainerCannotBeStopped(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	f.stopErr["old1"] = errors.New("daemon busy")

	out := newRunner(f).Run(context.Background(), spec())

	if out.Result != ResultFailed {
		t.Fatalf("outcome = %+v", out)
	}
	if slices.Contains(f.events, "start:new1") {
		t.Errorf("the new container was started while the old one still ran: %v", f.events)
	}
	if f.names["old1"] != "sentinel" || !slices.Contains(f.events, "remove:new1") {
		t.Errorf("not restored: name=%q events=%v", f.names["old1"], f.events)
	}
}

func TestReportOutcomeReadsAndRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOutcome(dir, Outcome{Result: ResultRolledBack, Error: "boom", NewImage: "img:3"}); err != nil {
		t.Fatal(err)
	}
	out := ReportOutcome(dir, nopLog{})
	if out == nil || out.Result != ResultRolledBack || out.Error != "boom" {
		t.Fatalf("outcome = %+v", out)
	}
	if ReportOutcome(dir, nopLog{}) != nil {
		t.Error("the outcome was reported twice")
	}
}

func cleanupFixture() (*fake, func(id, name, state string, labels map[string]string) container.Summary) {
	f := newFake()
	mk := func(id, name, state string, labels map[string]string) container.Summary {
		return container.Summary{ID: id, Names: []string{"/" + name}, State: container.ContainerState(state), Labels: labels}
	}
	return f, mk
}

func removed(f *fake) []string {
	got := []string{}
	for _, e := range f.events {
		got = append(got, strings.TrimPrefix(e, "remove:"))
	}
	slices.Sort(got)
	return got
}

func TestCleanupLeftoversRemovesOnlyExitedLeftovers(t *testing.T) {
	f, mk := cleanupFixture()
	self := map[string]string{"sentinel.self": "true"}
	f.state["cur"] = &container.State{Running: true}
	f.list = []container.Summary{
		mk("cur", "sentinel", "running", self),
		mk("gone1", "sentinel-old-1700000000", "exited", self),
		mk("gone2", "sentinel-old-1700000999", "created", self),
		mk("keep-paused", "sentinel-old-1700002222", "paused", self),
		mk("keep-running", "sentinel-old-1700001111", "running", self),
		mk("keep-orphan", "lost-old-5", "exited", self),
		mk("keep-unlabelled", "grafana-old-111", "exited", nil),
		mk("helper-done", "sentinel-self-update-helper", "exited", map[string]string{HelperLabel: "true"}),
	}
	CleanupLeftovers(context.Background(), f, "", nopLog{})

	if want := []string{"gone1", "gone2", "helper-done"}; !slices.Equal(removed(f), want) {
		t.Errorf("removed %v, want %v", removed(f), want)
	}
}

// The old container is the helper's way back until the helper has judged the
// new one. Cleanup in the new instance must not remove it before then.
func TestCleanupLeftoversKeepsOldContainerWhileAHelperIsActive(t *testing.T) {
	for _, state := range []string{"running", "created", "restarting"} {
		f, mk := cleanupFixture()
		self := map[string]string{"sentinel.self": "true"}
		f.state["cur"] = &container.State{Running: true}
		f.list = []container.Summary{
			mk("cur", "sentinel", "running", self),
			mk("old", "sentinel-old-1700000000", "exited", self),
			mk("helper", "sentinel-self-update-helper", state, map[string]string{HelperLabel: "true"}),
		}
		CleanupLeftovers(context.Background(), f, "", nopLog{})
		if len(f.events) != 0 {
			t.Errorf("helper %s: removed %v while the swap was in progress", state, f.events)
		}
	}
}

func TestCleanupLeftoversKeepsOldContainerWhileTheOutcomeSaysInProgress(t *testing.T) {
	dir := t.TempDir()
	if err := WriteOutcome(dir, Outcome{Result: ResultInProgress, FinishedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f, mk := cleanupFixture()
	self := map[string]string{"sentinel.self": "true"}
	f.state["cur"] = &container.State{Running: true}
	f.list = []container.Summary{mk("cur", "sentinel", "running", self), mk("old", "sentinel-old-1", "exited", self)}

	CleanupLeftovers(context.Background(), f, dir, nopLog{})
	if len(f.events) != 0 {
		t.Errorf("removed %v during an in-progress swap", f.events)
	}
	// ReportOutcome must not swallow the marker either.
	if out := ReportOutcome(dir, nopLog{}); out != nil {
		t.Errorf("ReportOutcome reported an in-progress swap: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, OutcomeFile)); err != nil {
		t.Errorf("the in-progress marker was removed: %v", err)
	}
}

// Only a replacement that is running, and healthy if it has a health check,
// makes the old container expendable.
func TestCleanupLeftoversNeedsAHealthyRunningReplacement(t *testing.T) {
	cases := map[string]*container.State{
		"not running": {Running: false},
		"restarting":  {Running: true, Restarting: true},
		"unhealthy":   {Running: true, Health: &container.Health{Status: container.Unhealthy}},
		"starting":    {Running: true, Health: &container.Health{Status: container.Starting}},
	}
	for name, st := range cases {
		f, mk := cleanupFixture()
		self := map[string]string{"sentinel.self": "true"}
		f.state["cur"] = st
		f.list = []container.Summary{mk("cur", "sentinel", "running", self), mk("old", "sentinel-old-1", "exited", self)}
		CleanupLeftovers(context.Background(), f, "", nopLog{})
		if len(f.events) != 0 {
			t.Errorf("%s: removed %v", name, f.events)
		}
	}
	f, mk := cleanupFixture()
	self := map[string]string{"sentinel.self": "true"}
	f.state["cur"] = &container.State{Running: true, Health: &container.Health{Status: container.Healthy}}
	f.list = []container.Summary{mk("cur", "sentinel", "running", self), mk("old", "sentinel-old-1", "exited", self)}
	CleanupLeftovers(context.Background(), f, "", nopLog{})
	if !slices.Equal(removed(f), []string{"old"}) {
		t.Errorf("healthy replacement: removed %v", removed(f))
	}
}

// The helper marks the swap as in progress before it touches anything.
func TestRunMarksTheSwapInProgressBeforeStoppingTheOldContainer(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	s := spec()
	s.OutcomeDir = t.TempDir()
	var marker string
	f.onStop = func() {
		b, _ := os.ReadFile(filepath.Join(s.OutcomeDir, OutcomeFile))
		marker = string(b)
	}
	newRunner(f).Run(context.Background(), s)
	if !strings.Contains(marker, `"in_progress"`) {
		t.Errorf("outcome file when the old container was stopped: %q", marker)
	}
}

// Only a leftover helper is removed by name. A container that merely has the
// helper's name is somebody's, and is left alone (and the update stops).
func TestBeginDoesNotRemoveAUserContainerNamedLikeTheHelper(t *testing.T) {
	f := newFake()
	f.list = []container.Summary{{ID: "user1", Names: []string{"/sentinel-self-update-helper"}, State: "running"}}
	if _, err := Begin(context.Background(), params(f)); err == nil {
		t.Fatal("expected an error: the helper's name is taken by another container")
	}
	if slices.Contains(f.events, "remove:user1") {
		t.Error("removed a container that is not a Sentinel helper")
	}
	if f.names["old1"] != "sentinel" {
		t.Errorf("running container left named %q", f.names["old1"])
	}
}

func TestBeginRemovesAnExitedLeftoverHelper(t *testing.T) {
	f := newFake()
	f.list = []container.Summary{{ID: "stale", Names: []string{"/sentinel-self-update-helper"}, State: "exited",
		Labels: map[string]string{HelperLabel: "true"}}}
	if _, err := Begin(context.Background(), params(f)); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.events, "remove:stale") {
		t.Errorf("leftover helper not removed: %v", f.events)
	}
}

func (f *fake) UpdateRestartPolicy(_ context.Context, id string, p container.RestartPolicy) error {
	f.note("policy:" + id + ":" + string(p.Name))
	return nil
}

// Before the old container is stopped it is set to restart "always", so a
// daemon restart between "stop old" and "start new" brings it back.
func TestRunMakesTheOldContainerSurviveADaemonRestartWhileSwapping(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	s := spec()
	s.OldRestart = container.RestartPolicy{Name: "unless-stopped"}
	s.OutcomeDir = t.TempDir()

	out := newRunner(f).Run(context.Background(), s)

	if out.Result != ResultSuccess {
		t.Fatalf("outcome = %+v", out)
	}
	if len(f.events) < 2 || f.events[0] != "policy:old1:always" || f.events[1] != "stop:old1" {
		t.Errorf("events = %v, want the policy set to always before the stop", f.events)
	}
}

func TestRunRestoresTheOldRestartPolicyOnRollback(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	f.startErr["new1"] = errors.New("boom")
	s := spec()
	s.OldRestart = container.RestartPolicy{Name: "unless-stopped"}

	out := newRunner(f).Run(context.Background(), s)

	if out.Result != ResultRolledBack {
		t.Fatalf("outcome = %+v", out)
	}
	last := f.events[len(f.events)-1]
	if last != "policy:old1:unless-stopped" {
		t.Errorf("last event = %q, want the original policy restored (events %v)", last, f.events)
	}
}

// The in-progress marker records what is needed to undo an interrupted swap.
func TestRunJournalsTheSwapInTheInProgressMarker(t *testing.T) {
	f := newFake()
	f.state["old1"] = &container.State{Running: true}
	s := spec()
	s.OldRestart = container.RestartPolicy{Name: "on-failure", MaximumRetryCount: 3}
	s.OutcomeDir = t.TempDir()
	var marker Outcome
	f.onStop = func() {
		b, _ := os.ReadFile(filepath.Join(s.OutcomeDir, OutcomeFile))
		_ = json.Unmarshal(b, &marker)
	}
	newRunner(f).Run(context.Background(), s)

	if marker.Result != ResultInProgress || marker.OldID != "old1" || marker.NewID != "new1" || marker.FinalName != "sentinel" ||
		marker.OldRestart == nil || marker.OldRestart.Name != "on-failure" || marker.OldRestart.MaximumRetryCount != 3 {
		t.Errorf("marker = %+v", marker)
	}
}

const selfHostname = "abcdef123456"

func interruptedSwap() (*fake, string) {
	f := newFake()
	self := map[string]string{"sentinel.self": "true"}
	f.list = []container.Summary{
		{ID: selfHostname + "7890", Names: []string{"/sentinel-old-1700000000"}, State: "running", Labels: self},
		{ID: "new1", Names: []string{"/sentinel"}, State: "created", Labels: self},
	}
	dir := ""
	return f, dir
}

// A host that restarted between "stop old" and "start new" brings the old
// instance back under its parked name, with the replacement never started. It
// puts itself back: removes the replacement, takes its name and its policy.
func TestRecoverPutsAParkedInstanceBackWhenTheReplacementNeverStarted(t *testing.T) {
	for _, state := range []string{"created", "exited"} {
		f, _ := interruptedSwap()
		f.list[1].State = container.ContainerState(state)
		dir := t.TempDir()
		pol := container.RestartPolicy{Name: "unless-stopped"}
		_ = WriteOutcome(dir, Outcome{Result: ResultInProgress, OldID: selfHostname + "7890", NewID: "new1", FinalName: "sentinel", OldRestart: &pol, FinishedAt: time.Now()})

		out := Recover(context.Background(), f, selfHostname, dir, nopLog{})

		if out == nil || out.Result != ResultRolledBack {
			t.Fatalf("%s: outcome = %+v", state, out)
		}
		want := []string{"remove:new1", "rename:" + selfHostname + "7890>sentinel", "policy:" + selfHostname + "7890:unless-stopped"}
		if !slices.Equal(f.events, want) {
			t.Errorf("%s: events = %v, want %v", state, f.events, want)
		}
		if _, err := os.Stat(filepath.Join(dir, OutcomeFile)); err == nil {
			t.Errorf("%s: the in-progress marker was left behind", state)
		}
	}
}

func TestRecoverLeavesAnUnfinishedSwapAloneWhenItIsStillRunning(t *testing.T) {
	t.Run("helper active", func(t *testing.T) {
		f, _ := interruptedSwap()
		f.list = append(f.list, container.Summary{ID: "h", Names: []string{"/sentinel-self-update-helper"}, State: "running",
			Labels: map[string]string{HelperLabel: "true"}, Created: time.Now().Unix()})
		if out := Recover(context.Background(), f, selfHostname, "", nopLog{}); out != nil || len(f.events) != 0 {
			t.Errorf("acted during a live swap: %+v %v", out, f.events)
		}
	})
	t.Run("replacement running", func(t *testing.T) {
		f, _ := interruptedSwap()
		f.list[1].State = "running"
		if out := Recover(context.Background(), f, selfHostname, "", nopLog{}); out != nil || len(f.events) != 0 {
			t.Errorf("acted although the replacement runs: %+v %v", out, f.events)
		}
	})
	t.Run("not parked", func(t *testing.T) {
		f, _ := interruptedSwap()
		f.list[0].Names = []string{"/sentinel"}
		f.list = f.list[:1]
		if out := Recover(context.Background(), f, selfHostname, "", nopLog{}); out != nil || len(f.events) != 0 {
			t.Errorf("acted on an instance with its own name: %+v %v", out, f.events)
		}
	})
}
