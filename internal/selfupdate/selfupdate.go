// Package selfupdate replaces a running Sentinel container (a hub, a standalone
// instance or an agent) with one on a new image.
//
// A container cannot free its own published ports, or its database lock,
// before the new one starts. So the running instance sets everything up, then
// hands the swap to a short-lived helper container: it is created from the
// image the running instance uses (so it works whatever the target image is,
// including an older one), mounts the Docker socket and runs the hidden "self-update-helper"
// subcommand, which stops the old container, starts the new one, waits for it
// to settle and puts the old one back if it does not. The helper records the
// outcome in the data directory and the new instance reports it on startup.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
)

const (
	// HelperSubcommand is the hidden subcommand the helper container runs.
	HelperSubcommand = "self-update-helper"
	// HelperLabel marks a helper container.
	HelperLabel = "sentinel.self-update-helper"
	// SpecEnv carries the helper's Spec, as JSON, in its environment.
	SpecEnv = "SENTINEL_SELF_UPDATE_SPEC"
	// DefaultSocket is the Docker socket path inside a Sentinel container.
	DefaultSocket = "/var/run/docker.sock"

	helperSocket  = "/var/run/docker.sock"
	helperDataDir = "/sentinel-data"

	defaultDelay    = 3 * time.Second
	defaultSettle   = 20 * time.Second
	defaultWatchdog = 5 * time.Minute
)

// ErrNoSocket means the running container has no Docker socket mounted, so a
// helper cannot control the daemon (for example Sentinel reaches a remote
// daemon over TCP). The caller falls back to replacing in-process.
var ErrNoSocket = errors.New("the Docker socket is not mounted in this container")

// Docker is the part of the Docker API the self-update needs. docker.API and
// the agent's DockerAPI both satisfy it.
type Docker interface {
	ListAllContainers(ctx context.Context) ([]container.Summary, error)
	InspectContainer(ctx context.Context, id string) (container.InspectResponse, error)
	StopContainer(ctx context.Context, id string, timeout int) error
	RemoveContainer(ctx context.Context, id string) error
	CreateContainer(ctx context.Context, name string, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig) (string, error)
	StartContainer(ctx context.Context, id string) error
	RenameContainer(ctx context.Context, id string, newName string) error
}

// Logger is the logging the package needs.
type Logger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	Debug(msg string, args ...any)
}

// Spec is everything the helper needs to finish the swap.
type Spec struct {
	OldID     string `json:"old_id"`
	OldName   string `json:"old_name"`   // the name the old container has been renamed to
	FinalName string `json:"final_name"` // the name the container has in the end
	NewID     string `json:"new_id"`
	OldImage  string `json:"old_image"`
	NewImage  string `json:"new_image"`
	// Delay is how long the helper waits before stopping the old container,
	// so the old instance can still report its result.
	Delay time.Duration `json:"delay"`
	// Settle is how long the new container must stay up (and healthy, if it
	// has a health check) before the swap counts as done.
	Settle time.Duration `json:"settle"`
	// OldRestart is the old container's restart policy, which the helper
	// changes during the swap and puts back if it rolls back.
	OldRestart container.RestartPolicy `json:"old_restart"`
	// NewRestart is the policy the replacement is meant to have. It is created
	// with none, so that after a daemon restart inside the swap only the old
	// container returns, and gets this policy once the swap is committed.
	NewRestart container.RestartPolicy `json:"new_restart"`
	// StopTimeout is the grace period, in seconds, for stopping the old one.
	StopTimeout int `json:"stop_timeout"`
	// OutcomeDir is where the helper writes the outcome file ("" = nowhere).
	OutcomeDir string `json:"outcome_dir"`
}

// Params describes the self-update the running instance wants.
type Params struct {
	Docker  Docker
	Log     Logger
	Inspect container.InspectResponse // the running container
	Name    string                    // its name, without the slash
	// Image is the target image reference.
	Image string
	// OldImageDefaults is the config of the image the container runs, so the
	// replacement does not keep values that came from it (nil keeps all).
	OldImageDefaults *container.Config
	// DataDir is the directory in this container that holds the database. The
	// helper writes its outcome file there. Empty means no outcome file.
	DataDir string
	// Socket is the Docker socket path in this container (default DefaultSocket).
	Socket string
	// Delay, Settle and Watchdog override the defaults (zero = default).
	Delay, Settle, Watchdog time.Duration
	// Poll is how often Wait polls the helper (default 1s).
	Poll time.Duration
}

// Handover is a prepared self-update whose helper has been started.
type Handover struct {
	p        Params
	oldID    string
	oldName  string
	newID    string
	helperID string
}

// OldName is the name the running container was renamed to.
func (h *Handover) OldName() string { return h.oldName }

// NewID is the replacement container.
func (h *Handover) NewID() string { return h.newID }

// BuildReplacement is the create request for the container that replaces
// inspect: the target image, the new image's defaults rather than the old
// one's, the old container's anonymous volumes and every network, and the
// marker that makes the replacement wait for the database lock.
func BuildReplacement(inspect container.InspectResponse, image string, oldDefaults *container.Config) docker.Recreate {
	rc := docker.RecreateConfig(inspect, docker.RecreateOptions{Image: image, OldImage: oldDefaults})
	rc.Config.Env = append(slices.DeleteFunc(slices.Clone(rc.Config.Env), func(e string) bool {
		return strings.HasPrefix(e, store.SelfUpdateReplacementEnv+"=")
	}), store.SelfUpdateReplacementEnv+"=1")
	return rc
}

// ImageDefaults reads the config baked into an image, or nil when the daemon
// client cannot (so the caller keeps the container's config whole).
func ImageDefaults(ctx context.Context, d any, imageID string, log Logger) *container.Config {
	reader, ok := d.(docker.ImageConfigReader)
	if !ok || imageID == "" {
		return nil
	}
	cfg, err := reader.ImageConfig(ctx, imageID)
	if err != nil {
		log.Warn("could not read the old image's defaults, keeping the container config as it is", "image", imageID, "error", err)
		return nil
	}
	return cfg
}

// socketSource returns the host path of the Docker socket mounted at socket.
func socketSource(inspect container.InspectResponse, socket string) (string, bool) {
	for _, m := range inspect.Mounts {
		if m.Destination == socket && m.Type == mount.TypeBind && m.Source != "" {
			return m.Source, true
		}
	}
	return "", false
}

// dataBind finds the mount that holds dir (the one with the longest
// destination that is dir or a parent of it) and returns the bind that gives
// the helper that mount at helperDataDir, plus where dir is inside it.
func dataBind(inspect container.InspectResponse, dir string) (bind, outcomeDir string, ok bool) {
	if dir == "" {
		return "", "", false
	}
	dir = path.Clean(dir)
	best := -1
	for i, m := range inspect.Mounts {
		dest := path.Clean(m.Destination)
		if dest != dir && !strings.HasPrefix(dir, strings.TrimSuffix(dest, "/")+"/") {
			continue
		}
		if m.Type != mount.TypeVolume && m.Type != mount.TypeBind {
			continue
		}
		if best < 0 || len(dest) > len(path.Clean(inspect.Mounts[best].Destination)) {
			best = i
		}
	}
	if best < 0 {
		return "", "", false
	}
	m := inspect.Mounts[best]
	src := m.Source
	if m.Type == mount.TypeVolume {
		src = m.Name
	}
	if src == "" {
		return "", "", false
	}
	rel := strings.TrimPrefix(dir, path.Clean(m.Destination))
	return src + ":" + helperDataDir, path.Join(helperDataDir, rel), true
}

// Begin sets the swap up and starts the helper: it renames the running
// container aside, creates the replacement (not started) and the helper, and
// starts the helper. If anything fails before the helper starts, the running
// container gets its name back and nothing else is left behind. It returns
// ErrNoSocket when no helper can be used.
//
// Once Begin returns, the helper stops this instance after Params.Delay, so
// the caller should report whatever it has to report and then call Wait.
func Begin(ctx context.Context, p Params) (*Handover, error) {
	if p.Socket == "" {
		p.Socket = DefaultSocket
	}
	hostSocket, ok := socketSource(p.Inspect, p.Socket)
	if !ok {
		return nil, ErrNoSocket
	}
	if p.Delay == 0 {
		p.Delay = defaultDelay
	}
	if p.Settle == 0 {
		p.Settle = defaultSettle
	}

	if InProgress(p.DataDir) {
		return nil, errors.New("another self-update is in progress")
	}
	if p.DataDir != "" {
		// A marker left by an earlier swap must not be mistaken for this one's.
		_ = os.Remove(filepath.Join(p.DataDir, OutcomeFile))
	}

	h := &Handover{p: p, oldID: p.Inspect.ID}
	h.oldName = fmt.Sprintf("%s-old-%d", p.Name, time.Now().Unix())

	p.Log.Info("self-update: renaming the running container aside", "from", p.Name, "to", h.oldName)
	if err := p.Docker.RenameContainer(ctx, h.oldID, h.oldName); err != nil {
		return nil, fmt.Errorf("rename self: %w", err)
	}
	undo := func() {
		bg := context.Background()
		if h.helperID != "" {
			_ = p.Docker.RemoveContainer(bg, h.helperID)
		}
		if h.newID != "" {
			_ = p.Docker.RemoveContainer(bg, h.newID)
		}
		_ = p.Docker.RenameContainer(bg, h.oldID, p.Name)
	}

	rc := BuildReplacement(p.Inspect, p.Image, p.OldImageDefaults)
	// No restart policy until the helper commits the swap (see Spec.NewRestart).
	rc.HostConfig.RestartPolicy = container.RestartPolicy{Name: container.RestartPolicyDisabled}
	p.Log.Info("self-update: creating the replacement", "name", p.Name, "image", p.Image)
	newID, err := p.Docker.CreateContainer(ctx, p.Name, rc.Config, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		undo()
		return nil, fmt.Errorf("create replacement: %w", err)
	}
	h.newID = newID

	spec := Spec{
		OldID: h.oldID, OldName: h.oldName, FinalName: p.Name, NewID: newID,
		OldImage: p.Inspect.Config.Image, NewImage: p.Image,
		Delay: p.Delay, Settle: p.Settle, StopTimeout: 30,
	}
	if p.Inspect.HostConfig != nil {
		spec.OldRestart = p.Inspect.HostConfig.RestartPolicy
		spec.NewRestart = p.Inspect.HostConfig.RestartPolicy
	}
	binds := []string{hostSocket + ":" + helperSocket}
	if b, outDir, ok := dataBind(p.Inspect, p.DataDir); ok {
		binds = append(binds, b)
		spec.OutcomeDir = outDir
	}
	specJSON, err := json.Marshal(spec)
	if err != nil {
		undo()
		return nil, fmt.Errorf("encode helper spec: %w", err)
	}

	helperName := p.Name + "-self-update-helper"
	// A helper left by an earlier attempt holds the name. Only an exited helper
	// of ours is removed; a container that merely has the name is somebody
	// else's, and the update stops there (the create below fails on the name).
	if old, err := findContainer(ctx, p.Docker, helperName); err == nil && old != nil &&
		old.Labels[HelperLabel] == "true" && old.State == container.StateExited {
		_ = p.Docker.RemoveContainer(ctx, old.ID)
	}
	// The helper runs the image this instance runs (by its ID), not the target:
	// the target may be older than the helper subcommand (a rollback to v2.x)
	// and would start a full Sentinel instead.
	helperImage := p.Inspect.Image
	if helperImage == "" {
		helperImage = p.Inspect.Config.Image
	}
	helperCfg := &container.Config{
		Image: helperImage,
		Cmd:   []string{HelperSubcommand},
		Env:   []string{SpecEnv + "=" + string(specJSON)},
		// The image carries sentinel.self=true; the helper is not Sentinel.
		Labels: map[string]string{HelperLabel: "true", "sentinel.self": "false", "sentinel.policy": "pinned"},
	}
	helperHost := &container.HostConfig{
		AutoRemove:  true,
		Binds:       binds,
		NetworkMode: "none",
	}
	p.Log.Info("self-update: starting the helper", "image", p.Image)
	helperID, err := p.Docker.CreateContainer(ctx, helperName, helperCfg, helperHost, nil)
	if err != nil {
		undo()
		return nil, fmt.Errorf("create self-update helper: %w", err)
	}
	h.helperID = helperID
	if err := p.Docker.StartContainer(ctx, helperID); err != nil {
		undo()
		return nil, fmt.Errorf("start self-update helper: %w", err)
	}
	return h, nil
}

// Wait waits for the helper to do its work. When the swap succeeds the helper
// stops this process and Wait never returns. It returns an error when the
// helper ends while this instance is still running, or does not stop it within
// the watchdog time.
//
// What is done then follows the helper's outcome marker, never the mere fact
// that the helper is gone: a helper that has begun the swap owns both
// containers, so with an unfinished or unknown outcome nothing is removed or
// renamed (and the error says so); a rolled-back or failed outcome means the
// helper already restored this instance. Only when the helper never began (no
// marker at all) is its set-up undone, and then only what is provably unused.
func (h *Handover) Wait(ctx context.Context) error {
	p := h.p
	poll := p.Poll
	if poll == 0 {
		poll = time.Second
	}
	watchdog := p.Watchdog
	if watchdog == 0 {
		watchdog = defaultWatchdog
	}
	deadline := time.Now().Add(watchdog)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
		info, err := p.Docker.InspectContainer(ctx, h.helperID)
		if err != nil || info.State == nil || !info.State.Running {
			return h.resolve("the self-update helper finished without replacing this instance")
		}
		if time.Now().After(deadline) {
			return h.resolve(fmt.Sprintf("the self-update helper did not replace this instance within %s", watchdog))
		}
	}
}

// resolve decides, from the helper's outcome marker, what to do about a
// self-update that did not replace this instance.
func (h *Handover) resolve(what string) error {
	p := h.p
	marker := ReadOutcomeFile(p.DataDir)
	switch {
	case marker != nil && (marker.Result == ResultRolledBack || marker.Result == ResultFailed):
		// The helper judged the new container and restored this instance.
		return fmt.Errorf("%s: it %s (%s); this instance is unchanged", what, strings.ReplaceAll(marker.Result, "_", " "), marker.Error)
	case marker != nil:
		// In progress or (impossibly) successful: the helper owns the swap.
		p.Log.Error("self-update: the helper ended while the swap was not finished; leaving both containers exactly as they are, check them by hand",
			"old", h.oldName, "old_id", h.oldID, "new_id", h.newID, "marker", marker.Result)
		return fmt.Errorf("%s while the swap was marked %s; nothing was removed or renamed (old container %s, replacement %s), check them by hand",
			what, marker.Result, h.oldName, h.newID)
	case p.DataDir == "":
		p.Log.Error("self-update: the helper ended and no outcome can be read here; leaving both containers exactly as they are, check them by hand",
			"old", h.oldName, "old_id", h.oldID, "new_id", h.newID)
		return fmt.Errorf("%s and its outcome is unknown; nothing was removed or renamed (old container %s, replacement %s), check them by hand",
			what, h.oldName, h.newID)
	}

	// No marker: the helper never began the swap, so the old container was
	// never touched. Remove only what is provably unused: the helper, which
	// this instance created, and the replacement if it was never started.
	bg := context.Background()
	_ = p.Docker.RemoveContainer(bg, h.helperID)
	if info, err := p.Docker.InspectContainer(bg, h.newID); err == nil && info.State != nil &&
		(info.State.Running || info.State.Restarting) {
		p.Log.Error("self-update: the replacement is running although the helper never began; leaving it, check the containers by hand",
			"old", h.oldName, "new_id", h.newID)
		return fmt.Errorf("%s, but the replacement %s is running; nothing was removed (old container %s), check them by hand", what, h.newID, h.oldName)
	}
	_ = p.Docker.RemoveContainer(bg, h.newID)
	_ = p.Docker.RenameContainer(bg, h.oldID, p.Name)
	return fmt.Errorf("%s; the helper never started the swap, so this instance was put back unchanged (check the helper's logs)", what)
}

// FindSelf returns the container with the given ID (or ID prefix), or nil. A
// Sentinel identifies itself by the ID of its own container, never by the
// sentinel.self label, because several Sentinel containers can share a host.
func FindSelf(containers []container.Summary, selfID string) *container.Summary {
	if selfID == "" {
		return nil
	}
	for i := range containers {
		if strings.HasPrefix(containers[i].ID, selfID) {
			return &containers[i]
		}
	}
	return nil
}

// ErrSelfUnknown means this process's own container could not be determined,
// so no self-update is done: guessing could replace another Sentinel.
var ErrSelfUnknown = errors.New("could not determine which container this Sentinel is running in, so it will not self-update (a guess could replace another Sentinel)")

func findContainer(ctx context.Context, d Docker, name string) (*container.Summary, error) {
	all, err := d.ListAllContainers(ctx)
	if err != nil {
		return nil, err
	}
	for i, c := range all {
		for _, n := range c.Names {
			if strings.TrimPrefix(n, "/") == name {
				return &all[i], nil
			}
		}
	}
	return nil, nil
}
