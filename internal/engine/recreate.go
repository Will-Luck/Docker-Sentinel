package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/guardian"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/moby/moby/api/types/container"
)

// replacedSuffix is added to a container's name while Sentinel replaces it.
// A container still carrying it at startup was left by an update, rollback
// or finalise that Sentinel did not finish; see RecoverInterruptedUpdates.
const replacedSuffix = "-sentinel-old"

// swapTimeout bounds each group of Docker calls in a swap. The calls run
// without the caller's cancellation, so a shutdown cannot stop a swap
// halfway and leave the container missing.
const swapTimeout = 3 * time.Minute

// swap replaces a container without ever leaving its service missing.
//
// replace renames the old container aside (name + replacedSuffix), creates
// the replacement under the real name, stops the old one and starts the
// replacement. Nothing is deleted on the way, so any failure can be undone:
// undo removes the half-made replacement, gives the old container its name
// back and starts it again if it was running. Once the replacement is
// known to be good, commit removes the old container.
type swap struct {
	d    docker.API
	log  *logging.Logger
	name string

	oldID         string
	oldRunning    bool
	oldAutoRemove bool
	stopTimeout   int

	renamed    bool // the old container carries the replaced name
	stopped    bool // the old container was stopped by this swap
	newID      string
	newStarted bool
}

// newSwap prepares the replacement of container oldID, inspected as old,
// that is known as name.
func newSwap(d docker.API, log *logging.Logger, name, oldID string, old container.InspectResponse, stopTimeout int) *swap {
	return &swap{
		d:     d,
		log:   log,
		name:  name,
		oldID: oldID,
		// A missing state is treated as running, so a restore never
		// leaves the service stopped.
		oldRunning:    old.State == nil || old.State.Running,
		oldAutoRemove: old.HostConfig != nil && old.HostConfig.AutoRemove,
		stopTimeout:   stopTimeout,
	}
}

// swapError says at which step a swap failed.
type swapError struct {
	stage string // "rename", "create", "verify" or "start"
	err   error
}

func (e *swapError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *swapError) Unwrap() error { return e.err }

// replace swaps in the replacement described by rc. When start is false
// the replacement is created but not started (for a container that was not
// running). verify, if set, checks the created container before the old
// one is stopped. On error, call undo.
func (s *swap) replace(ctx context.Context, rc docker.Recreate, start bool, verify func(ctx context.Context, id string) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	if err := s.d.RenameContainer(ctx, s.oldID, s.name+replacedSuffix); err != nil {
		return &swapError{stage: "rename", err: err}
	}
	s.renamed = true

	newID, err := s.d.CreateContainer(ctx, s.name, rc.Config, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		return &swapError{stage: "create", err: err}
	}
	s.newID = newID
	if verify != nil {
		if err := verify(ctx, newID); err != nil {
			return &swapError{stage: "verify", err: err}
		}
	}

	if s.oldRunning {
		if err := s.d.StopContainer(ctx, s.oldID, s.stopTimeout); err != nil {
			// The replacement may still start (and validation decides);
			// if it cannot, undo brings the old container back.
			s.log.Warn("stopping the old container failed, carrying on", "name", s.name, "error", err)
		}
		s.stopped = true
	}
	if !start {
		return nil
	}
	if err := s.d.StartContainer(ctx, newID); err != nil {
		return &swapError{stage: "start", err: err}
	}
	s.newStarted = true
	return nil
}

// changed reports whether replace got far enough to change anything. When
// it did not (the rename failed), there is nothing to undo.
func (s *swap) changed() bool {
	return s.renamed || s.newID != ""
}

// undo reverts whatever replace did. It returns an error when the old
// container could not be brought back, for example because the daemon
// already deleted a --rm container on stop.
func (s *swap) undo(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	if s.newID != "" {
		if s.newStarted {
			// Stop it cleanly first: it may share volumes with the old
			// container, which starts on them next.
			if err := s.d.StopContainer(ctx, s.newID, s.stopTimeout); err != nil {
				s.log.Warn("stopping the replacement failed", "name", s.name, "error", err)
			}
		}
		if err := s.d.RemoveContainer(ctx, s.newID); err != nil {
			s.log.Warn("could not remove the replacement", "name", s.name, "id", truncateID(s.newID), "error", err)
		}
		s.newID = ""
		s.newStarted = false
	}
	if s.renamed {
		if err := s.d.RenameContainer(ctx, s.oldID, s.name); err != nil {
			return fmt.Errorf("give the old container its name back: %w", err)
		}
		s.renamed = false
	}
	if s.stopped {
		if err := s.d.StartContainer(ctx, s.oldID); err != nil {
			return fmt.Errorf("start the old container again: %w", err)
		}
		s.stopped = false
	}
	return nil
}

// commit removes the old container once the replacement is good. A
// failure only leaves the stopped old container behind under its replaced
// name, which RecoverInterruptedUpdates removes at the next start.
func (s *swap) commit(ctx context.Context, removeVolumes bool) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	var err error
	if removeVolumes {
		err = s.d.RemoveContainerWithVolumes(ctx, s.oldID)
	} else {
		err = s.d.RemoveContainer(ctx, s.oldID)
	}
	switch {
	case err == nil:
	case s.oldAutoRemove:
		// A --rm container is deleted by the daemon when it stops.
		s.log.Debug("old container already removed by the daemon", "name", s.name)
	default:
		s.log.Warn("could not remove the replaced container", "name", s.name+replacedSuffix, "error", err)
	}
	s.renamed = false
	s.stopped = false
}

// RecoverInterruptedUpdates resolves the containers an update, rollback or
// finalise left half-replaced because Sentinel stopped part way (a
// shutdown, a crash, a host reboot). Call it once at startup, before any
// scan or update can run. For each container named <name>-sentinel-old:
//
//   - no container holds <name>: the old container gets its name back and
//     is started if it is not running;
//   - <name> is running without the maintenance label: the swap had
//     finished, so the old container is removed;
//   - otherwise (<name> is not running, or carries the maintenance label of
//     an update that was never validated): <name> is removed and the old
//     container is restored.
//
// It then finalises any running container still carrying the maintenance
// label, so Docker-Guardian sees it again, and clears maintenance flags left
// set in the store.
func (u *Updater) RecoverInterruptedUpdates(ctx context.Context) {
	all, err := u.docker.ListAllContainers(ctx)
	if err != nil {
		u.log.Warn("startup recovery: could not list containers", "error", err)
		return
	}
	byName := make(map[string]container.Summary, len(all))
	for _, c := range all {
		byName[containerName(c)] = c
	}

	handled := make(map[string]bool)
	for _, oldName := range slices.Sorted(maps.Keys(byName)) {
		name, ok := strings.CutSuffix(oldName, replacedSuffix)
		if !ok || name == "" {
			continue
		}
		old := byName[oldName]
		cur, exists := byName[name]
		u.recoverSwap(ctx, name, old, cur, exists)
		handled[old.ID] = true
		if exists {
			handled[cur.ID] = true
		}
	}

	if len(handled) > 0 {
		if all, err = u.docker.ListAllContainers(ctx); err != nil {
			u.log.Warn("startup recovery: could not list containers", "error", err)
			return
		}
	}
	for _, c := range all {
		name := containerName(c)
		if handled[c.ID] || strings.HasSuffix(name, replacedSuffix) {
			continue
		}
		if on, err := u.store.GetMaintenance(name); err == nil && on {
			if err := u.store.SetMaintenance(name, false); err != nil {
				u.log.Warn("startup recovery: could not clear the maintenance flag", "name", name, "error", err)
			}
		}
		if c.State == container.StateRunning && guardian.HasMaintenanceLabel(c.Labels) {
			u.log.Warn("startup recovery: finishing an interrupted update (removing the maintenance label)", "name", name)
			if _, err := u.finaliseContainer(ctx, c.ID, name); err != nil {
				u.log.Error("startup recovery: could not finalise the container", "name", name, "error", err)
			}
		}
	}
}

// recoverSwap resolves one container left under its replaced name; see
// RecoverInterruptedUpdates.
func (u *Updater) recoverSwap(ctx context.Context, name string, old, cur container.Summary, curExists bool) {
	oldName := name + replacedSuffix
	if curExists {
		if cur.State == container.StateRunning && !guardian.HasMaintenanceLabel(cur.Labels) {
			u.log.Warn("startup recovery: an interrupted update had finished, removing the replaced container",
				"name", name, "removed", oldName)
			if err := u.docker.RemoveContainer(ctx, old.ID); err != nil {
				u.log.Error("startup recovery: could not remove the replaced container", "name", oldName, "error", err)
			}
			return
		}
		u.log.Warn("startup recovery: an interrupted update had not finished, restoring the old container",
			"name", name, "removed", truncateID(cur.ID))
		if err := u.docker.RemoveContainer(ctx, cur.ID); err != nil {
			u.log.Error("startup recovery: could not remove the unfinished replacement", "name", name, "error", err)
			return
		}
	} else {
		u.log.Warn("startup recovery: restoring a container an interrupted update left renamed", "name", name, "from", oldName)
	}
	if err := u.docker.RenameContainer(ctx, old.ID, name); err != nil {
		u.log.Error("startup recovery: could not give the old container its name back", "name", name, "error", err)
		return
	}
	if old.State != container.StateRunning {
		if err := u.docker.StartContainer(ctx, old.ID); err != nil {
			u.log.Error("startup recovery: could not start the restored container", "name", name, "error", err)
		}
	}
}
