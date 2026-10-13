package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// HostSwaps gives a Docker host that the hub does not manage itself (a
// cluster agent's) the hub's journalled container swap and the recovery that
// goes with it, with the journal in that host's own store. It is the same
// code the hub runs for its own containers, so a container an agent was
// replacing when it was killed is finished or put back, at the agent's next
// start, exactly as the hub's would be.
type HostSwaps struct {
	u *Updater
}

// NewHostSwaps returns the swaps of the host d, journalled in s. cfg gives
// the settings they follow: image cleanup, image backup and dependency
// awareness.
func NewHostSwaps(d docker.API, s *store.Store, cfg *config.Config, log *logging.Logger, clk clock.Clock) *HostSwaps {
	return &HostSwaps{u: NewUpdater(d, nil, s, nil, cfg, log, clk, notify.NewMulti(log), nil)}
}

// SetShutdownContext ends the background watch of restored containers when
// ctx ends. Call it before Recover.
func (h *HostSwaps) SetShutdownContext(ctx context.Context) {
	h.u.SetShutdownContext(ctx)
}

// Lock claims name for a change (an update, a recovery, a namespace
// repair), reporting false while something else holds it. Unlock releases
// it.
func (h *HostSwaps) Lock(name string) bool { return h.u.tryLock(name) }

// Unlock releases a name claimed with Lock.
func (h *HostSwaps) Unlock(name string) { h.u.unlock(name) }

// WaitIdle waits until no name is claimed, or ctx ends, and reports whether
// everything finished.
func (h *HostSwaps) WaitIdle(ctx context.Context) bool { return h.u.WaitIdle(ctx) }

// Recover finishes or undoes every journalled swap whose container is not
// claimed, as the hub does at startup and at every scan. Call it at startup,
// before any update can run, and then regularly: a swap whose recovery fails
// stays in the journal and is retried at the next call.
func (h *HostSwaps) Recover(ctx context.Context) { h.u.resolvePendingSwaps(ctx) }

// SetAside reports whether name is a container that a journalled swap has
// renamed out of the way (<name>-sentinel-old). It is part of that swap, not
// a container of its own.
func (h *HostSwaps) SetAside(name string) bool { return h.u.setAside(name) }

// Pending returns an error saying that an earlier swap of name did not
// finish, or nil. Nothing may change name until that swap is resolved: its
// journal record is the only way back to the old container.
func (h *HostSwaps) Pending(name string) error {
	rec, found, _ := h.u.store.GetSwap(name)
	if !found {
		return nil
	}
	if !rec.WatchUntil.IsZero() {
		return fmt.Errorf("%w: its old container was put back, and Sentinel watches it until %s in case Docker stops it; try again after that",
			ErrSwapPending, rec.WatchUntil.Format(time.TimeOnly))
	}
	return fmt.Errorf("%w: the agent retries it every minute; if it cannot, check %s and %s%s on this host",
		ErrSwapPending, name, name, replacedSuffix)
}

// BackupImage tags image as a backup before it is replaced, when image
// backup is on, as the hub does before an update.
func (h *HostSwaps) BackupImage(ctx context.Context, name, image string) {
	u := h.u
	if !u.isImageBackup() || image == "" {
		return
	}
	tag := u.backupTag(image)
	if err := u.docker.TagImage(ctx, image, tag); err != nil {
		u.log.Warn("image backup tag failed", "name", name, "tag", tag, "error", err)
		return
	}
	u.log.Info("image backup tag created", "name", name, "tag", tag)
}

// HostUpdate describes the replacement of a container.
type HostUpdate struct {
	Name  string
	OldID string
	// Old is the old container, as inspected: its running state decides
	// whether the replacement is started, and it is journalled so the old
	// container can be rebuilt if it disappears (a --rm container).
	Old      container.InspectResponse
	Recreate docker.Recreate
	// RemoveVolumes removes the old container's anonymous volumes with it
	// (the replacement was given fresh ones).
	RemoveVolumes bool
}

// Update replaces a container with the one described by up.Recreate, through
// the journalled swap: the old container is renamed aside, the replacement
// created, the old one stopped and the replacement started (only if the old
// one was running), then the old one removed. On failure the old container
// is put back, or rebuilt from its inspect if the daemon deleted it, and the
// containers depending on it are repaired; the swap's error is returned.
// On success the containers sharing its network namespace and (with
// dependency awareness on) its dependents follow it to the replacement. It
// returns the replacement's ID, and a warning when the update worked but
// the old container could not be removed yet (recovery removes it later).
func (h *HostSwaps) Update(ctx context.Context, up HostUpdate) (newID, warning string, err error) {
	u := h.u
	snapshot, err := json.Marshal(up.Old)
	if err != nil {
		return "", "", fmt.Errorf("snapshot %s: %w", up.Name, err)
	}
	sw := newSwap(u.docker, u.log, u.store, swapSpec{
		kind: swapUpdate, name: up.Name, oldID: up.OldID, old: up.Old, stopTimeout: 30,
		snapshot: snapshot, removeVolumes: up.RemoveVolumes,
	})
	if err := sw.replace(ctx, up.Recreate, sw.oldRunning, nil); err != nil {
		if !sw.changed() {
			return "", "", err
		}
		u.log.Error("replacement failed, restoring the old container", "name", up.Name, "error", err)
		if restoreErr := u.restoreOld(ctx, sw, snapshot); restoreErr != nil {
			return "", "", fmt.Errorf("%w; restoring the old container failed too: %v", err, restoreErr)
		}
		return "", "", err
	}
	sw.commit(ctx)
	if u.store.HasSwap(up.Name) {
		// commit keeps the record when the old container could not be
		// removed; recovery removes it.
		warning = fmt.Sprintf("updated, but the old container %s%s could not be removed yet; the agent retries", up.Name, replacedSuffix)
	}
	u.afterProviderChange(ctx, up.Name, sw.newID, up.OldID)
	return sw.newID, warning, nil
}

// CleanupOldImage removes the image an updated container ran before, when
// image cleanup is on and no container uses it any more, as the hub does
// after an update.
func (h *HostSwaps) CleanupOldImage(ctx context.Context, oldImageID, name string) {
	h.u.cleanupOldImage(ctx, oldImageID, name)
}
