package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// rollbackContainer recreates a container from its snapshot and returns the
// ID of the container that then runs under name.
//
// Everything that can fail is checked before the running container is
// touched: the snapshot's image must be on this host (pulled back if its
// tag still resolves to the same image ID) and its tag is pointed at it.
// The container holding the name is then swapped out (journalled in j when
// it is set), and removed only once the rollback container exists, runs the
// snapshot's image and has started. On any failure the running container is
// kept. The rollback container takes over the running container's anonymous
// volumes where the destinations match, so it starts on the current data.
func rollbackContainer(ctx context.Context, d docker.API, j swapJournal, name string, snapshotData []byte, log *logging.Logger) (string, error) {
	var inspect container.InspectResponse
	if err := json.Unmarshal(snapshotData, &inspect); err != nil {
		return "", fmt.Errorf("unmarshal snapshot: %w", err)
	}

	if inspect.Config == nil {
		return "", fmt.Errorf("inspect %s: container config is nil", name)
	}

	log.Info("rolling back container", "name", name, "image", inspect.Config.Image)

	liveID, err := findContainerID(ctx, d, name)
	if err != nil {
		return "", fmt.Errorf("list containers for rollback: %w", err)
	}
	var live container.InspectResponse
	if liveID != "" {
		if live, err = d.InspectContainer(ctx, liveID); err != nil {
			log.Warn("could not inspect the running container before rollback", "name", name, "error", err)
		}
	}

	undoTag, err := ensureImage(ctx, d, inspect.Config.Image, inspect.Image, log, liveID)
	if err != nil {
		return "", fmt.Errorf("rollback image for %s: %w", name, err)
	}
	from := inspect
	from.Mounts = preferLiveVolumes(inspect.Mounts, live.Mounts)
	rc := docker.RecreateConfig(from, docker.RecreateOptions{})
	verify := verifyImage(d, inspect.Image)

	if liveID == "" {
		newID, err := createAndStart(ctx, d, name, rc, verify, true)
		if err != nil {
			undoTag()
			return "", err
		}
		log.Info("rollback complete", "name", name)
		return newID, nil
	}

	// The live container's own snapshot lets recovery, or this function,
	// rebuild it should it be gone (a --rm container deleted on stop).
	liveSnapshot, _ := json.Marshal(live)
	sw := newSwap(d, log, j, swapSpec{kind: swapRollback, name: name, oldID: liveID, old: live, stopTimeout: stopTimeout(live.Config, 30), snapshot: liveSnapshot})
	if err := sw.replace(ctx, rc, true, verify); err != nil {
		undoTag()
		if !sw.changed() {
			return "", fmt.Errorf("rollback %s, running container kept: %w", name, err)
		}
		gone, undoErr := sw.undoOrRetry(ctx)
		switch {
		case gone:
			log.Warn("the running container is gone, rebuilding it", "name", name, "error", undoErr)
			if _, rbErr := rebuildFromSnapshot(ctx, d, name, liveSnapshot, sw.oldRunning, sw.rec.Token, log); rbErr != nil {
				return "", fmt.Errorf("rollback %s: %w (and the running container could not be rebuilt: %v)", name, err, rbErr)
			}
			sw.forget()
		case undoErr != nil:
			return "", fmt.Errorf("rollback %s: %w (and the running container could not be restored: %v)", name, err, undoErr)
		}
		return "", fmt.Errorf("rollback %s, running container kept: %w", name, err)
	}
	sw.commit(ctx)

	log.Info("rollback complete", "name", name, "container_id", truncateID(sw.newID))
	return sw.newID, nil
}

// preferLiveVolumes returns the snapshot's mounts with the volume at each
// destination replaced by the one the running container uses there, so a
// rollback keeps the current data rather than the data of the snapshot time.
func preferLiveVolumes(snapshot, live []container.MountPoint) []container.MountPoint {
	if len(live) == 0 {
		return snapshot
	}
	byDest := make(map[string]container.MountPoint, len(live))
	for _, m := range live {
		if m.Type == mount.TypeVolume && m.Name != "" {
			byDest[path.Clean(m.Destination)] = m
		}
	}
	out := slices.Clone(snapshot)
	for i, m := range out {
		if l, ok := byDest[path.Clean(m.Destination)]; ok && m.Type == mount.TypeVolume {
			out[i].Name = l.Name
		}
	}
	return out
}

// verifyImage returns a check that a created container runs image want, so
// a tag that moved between the decision and the create cannot slip a
// different image in. An empty want (a snapshot from an older version, with
// no image ID recorded) checks nothing.
func verifyImage(d docker.API, want string) func(ctx context.Context, id string) error {
	return func(ctx context.Context, id string) error {
		if want == "" {
			return nil
		}
		created, err := d.InspectContainer(ctx, id)
		if err != nil {
			return fmt.Errorf("inspect the new container: %w", err)
		}
		if created.Image != want {
			return fmt.Errorf("new container runs image %s, want %s", shortImageID(created.Image), shortImageID(want))
		}
		return nil
	}
}

// nameHeldError says a rebuild did not happen because a container holds
// the name. token is that container's swap token label, if any.
type nameHeldError struct{ id, token string }

func (e *nameHeldError) Error() string {
	return "the name is held by container " + truncateID(e.id)
}

// rebuild is a prepared rebuild of a container from its snapshot: the
// image is on the host and the create request built.
type rebuild struct {
	name    string
	rc      docker.Recreate
	verify  func(context.Context, string) error
	undoTag func()
}

// prepareRebuild gets everything that can fail out of the way before a
// container is rebuilt from its snapshot: the snapshot is read, and its
// image must be on the host (or pull back to the same image ID). Nothing is
// changed if it fails. A non-empty token is set as the sentinel.swap label,
// so a recovery interrupted during the rebuild recognises the container.
// replacing lists containers about to be removed, which do not count as
// other users of the image tag.
func prepareRebuild(ctx context.Context, d docker.API, name string, snapshot []byte, token string, log *logging.Logger, replacing ...string) (*rebuild, error) {
	var inspect container.InspectResponse
	if err := json.Unmarshal(snapshot, &inspect); err != nil {
		return nil, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	if inspect.Config == nil {
		return nil, fmt.Errorf("snapshot of %s has no config", name)
	}
	undoTag, err := ensureImage(ctx, d, inspect.Config.Image, inspect.Image, log, replacing...)
	if err != nil {
		return nil, fmt.Errorf("image for %s: %w", name, err)
	}
	rc := docker.RecreateConfig(inspect, docker.RecreateOptions{})
	if token != "" {
		rc.Config.Labels = maps.Clone(rc.Config.Labels)
		if rc.Config.Labels == nil {
			rc.Config.Labels = make(map[string]string)
		}
		rc.Config.Labels[swapLabel] = token
	}
	return &rebuild{name: name, rc: rc, verify: verifyImage(d, inspect.Image), undoTag: undoTag}, nil
}

// run creates the prepared container when no container holds its name,
// starting it only when start is set (the original was running), and
// returns its ID.
func (r *rebuild) run(ctx context.Context, d docker.API, start bool) (string, error) {
	holder, err := d.InspectContainer(ctx, r.name)
	switch {
	case err == nil:
		r.undoTag()
		held := &nameHeldError{id: holder.ID}
		if holder.Config != nil {
			held.token = holder.Config.Labels[swapLabel]
		}
		return "", held
	case !docker.IsNotFound(err):
		return "", fmt.Errorf("check the name %s: %w", r.name, err)
	}
	id, err := createAndStart(ctx, d, r.name, r.rc, r.verify, start)
	if err != nil {
		r.undoTag()
	}
	return id, err
}

// rebuildFromSnapshot prepares and runs a rebuild in one go.
func rebuildFromSnapshot(ctx context.Context, d docker.API, name string, snapshot []byte, start bool, token string, log *logging.Logger) (string, error) {
	r, err := prepareRebuild(ctx, d, name, snapshot, token, log)
	if err != nil {
		return "", err
	}
	return r.run(ctx, d, start)
}

// createAndStart creates (and, when start is set, starts) a container when
// no container holds the name, and returns its ID. A container that is
// created but fails to start is kept, so there is something to inspect and
// start by hand.
func createAndStart(ctx context.Context, d docker.API, name string, rc docker.Recreate, verify func(context.Context, string) error, start bool) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	newID, err := d.CreateContainer(ctx, name, rc.Config, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		return "", fmt.Errorf("create rollback container: %w", err)
	}
	if err := verify(ctx, newID); err != nil {
		_ = d.RemoveContainer(ctx, newID)
		return "", err
	}
	if !start {
		return newID, nil
	}
	if err := d.StartContainer(ctx, newID); err != nil {
		return newID, fmt.Errorf("start rollback container: %w", err)
	}
	return newID, nil
}

// findContainerID returns the ID of the container named name, or "" when
// there is none.
func findContainerID(ctx context.Context, d docker.API, name string) (string, error) {
	containers, err := d.ListAllContainers(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range containers {
		if containerName(c) == name {
			return c.ID, nil
		}
	}
	return "", nil
}

// ensureImage makes ref resolve to imageID on this host, so a container
// created from ref runs exactly that image. When the image is gone it pulls
// ref and accepts the result only if it is the same image: a mutable tag
// that has moved on cannot bring the old version back. The returned func
// points ref back where it was, for when the rollback is abandoned.
func ensureImage(ctx context.Context, d docker.API, ref, imageID string, log *logging.Logger, replacing ...string) (func(), error) {
	noop := func() {}
	if imageID == "" || ref == "" {
		return noop, nil // snapshot from an older version: no image ID recorded
	}
	if current, err := d.ImageID(ctx, ref); err == nil && current == imageID {
		return noop, nil
	}
	if have, err := d.ImageID(ctx, imageID); err != nil || have == "" {
		log.Info("rollback image is no longer on this host, pulling it", "image", ref, "id", shortImageID(imageID))
		if err := d.PullImage(ctx, ref); err != nil {
			return noop, fmt.Errorf("image %s is no longer on this host and pulling %s failed: %w", shortImageID(imageID), ref, err)
		}
		if got, err := d.ImageID(ctx, ref); err == nil && got == imageID {
			return noop, nil
		}
		return noop, fmt.Errorf("image %s is no longer on this host and %s now points at a different image", shortImageID(imageID), ref)
	}
	undo, err := retag(ctx, d, ref, imageID, log, replacing...)
	var shared *sharedTagError
	if errors.As(err, &shared) {
		return noop, fmt.Errorf("rolling back would point %s at the old image, but %s still runs the image it names now; roll %s back by hand or stop %s first",
			ref, shared.user, shared.user, shared.user)
	}
	return undo, err
}

// sharedTagError says a tag was left alone because another container runs
// the image it names.
type sharedTagError struct {
	ref, user string
}

func (e *sharedTagError) Error() string {
	return fmt.Sprintf("%s is left alone: %s runs the image it names", e.ref, e.user)
}

// retag points the tag ref at imageID, so that a container running that
// image shows it by name and Sentinel's scans can resolve it. It does not
// when another running container (other than the ones in replacing, which
// are about to go) runs the image the tag names now: that container would
// then look out of date. The returned func points ref back at the image it
// named before.
func retag(ctx context.Context, d docker.API, ref, imageID string, log *logging.Logger, replacing ...string) (func(), error) {
	noop := func() {}
	previous, prevErr := d.ImageID(ctx, ref)
	if prevErr == nil && previous == imageID {
		return noop, nil
	}
	if strings.Contains(ref, "@") {
		return noop, fmt.Errorf("%s does not resolve to image %s", ref, shortImageID(imageID))
	}
	if prevErr == nil && previous != "" {
		if user := runningOn(ctx, d, previous, replacing); user != "" {
			return noop, &sharedTagError{ref: ref, user: user}
		}
	}
	if err := d.TagImage(ctx, imageID, ref); err != nil {
		return noop, fmt.Errorf("point %s back at image %s: %w", ref, shortImageID(imageID), err)
	}
	log.Info("pointed tag back at the old image", "image", ref, "id", shortImageID(imageID))
	if prevErr != nil || previous == "" {
		return noop, nil
	}
	return func() {
		if err := d.TagImage(context.WithoutCancel(ctx), previous, ref); err != nil {
			log.Warn("could not point the tag back at its previous image", "image", ref, "error", err)
		}
	}, nil
}

// runningOn returns the name of a running container, other than those in
// except, that runs image imageID, or "".
func runningOn(ctx context.Context, d docker.API, imageID string, except []string) string {
	running, err := d.ListContainers(ctx)
	if err != nil {
		return ""
	}
	for _, c := range running {
		if c.ImageID == imageID && !slices.Contains(except, c.ID) {
			return containerName(c)
		}
	}
	return ""
}

// shortImageID shortens "sha256:<hex>" to 12 hex digits for messages.
func shortImageID(id string) string {
	return truncateID(strings.TrimPrefix(id, "sha256:"))
}

// RollbackFromStore fetches the latest snapshot from BoltDB and performs
// a rollback.
func RollbackFromStore(ctx context.Context, d docker.API, s *store.Store, name string, log *logging.Logger) error {
	data, err := s.GetLatestSnapshot(name)
	if err != nil {
		return fmt.Errorf("get snapshot: %w", err)
	}
	if data == nil {
		return fmt.Errorf("no snapshot found for %s", name)
	}
	_, err = rollbackContainer(ctx, d, s, name, data, log)
	return err
}

// RollbackContainer rolls a container back to its latest snapshot, as the
// dashboard's Rollback button does. It holds the container's update lock,
// so it cannot interleave with an update of the same container, and it
// reports the result on the event bus; a failure is also notified.
func (u *Updater) RollbackContainer(ctx context.Context, name string) error {
	if !u.tryLock(name) {
		return ErrUpdateInProgress
	}
	defer u.unlock(name)
	if u.store.HasSwap(name) {
		return u.pendingSwapError(name)
	}
	ctx, stop := u.withShutdown(ctx)
	defer stop()

	start := u.clock.Now()
	liveID, _ := findContainerID(ctx, u.docker, name)
	var live container.InspectResponse
	if liveID != "" {
		live, _ = u.docker.InspectContainer(ctx, liveID)
	}
	newID, err := u.rollbackFromStore(ctx, name)
	if err != nil {
		u.publishEvent(events.EventContainerUpdate, name, "rollback failed: "+err.Error())
		u.notifier.Notify(context.WithoutCancel(ctx), notify.Event{
			Type:          notify.EventRollbackFailed,
			ContainerName: name,
			Error:         err.Error(),
			Timestamp:     u.clock.Now(),
		})
		return err
	}
	u.afterProviderChange(ctx, name, newID, liveID)
	u.recordManualRollback(ctx, name, live, newID, start)
	u.publishEvent(events.EventContainerUpdate, name, "rollback succeeded")
	return nil
}

// recordManualRollback records and notifies a rollback asked for from the
// dashboard or the API: from the image live ran to the one the rollback
// container runs.
func (u *Updater) recordManualRollback(ctx context.Context, name string, live container.InspectResponse, newID string, start time.Time) {
	ctx = context.WithoutCancel(ctx)
	entry := store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		Outcome:       "rollback",
		Duration:      u.clock.Since(start),
		Error:         "rolled back on request",
	}
	if live.Config != nil {
		entry.OldImage, entry.OldDigest = live.Config.Image, live.Image
	}
	if rolled, err := u.docker.InspectContainer(ctx, newID); err == nil && rolled.Config != nil {
		entry.NewImage, entry.NewDigest = rolled.Config.Image, rolled.Image
	}
	if err := u.store.RecordUpdate(entry); err != nil {
		u.log.Warn("failed to persist rollback record", "name", name, "error", err)
	}
	u.notifier.Notify(ctx, notify.Event{
		Type:          notify.EventRollbackOK,
		ContainerName: name,
		OldImage:      entry.OldImage,
		NewImage:      entry.NewImage,
		Timestamp:     u.clock.Now(),
	})
}

// rollbackFromStore rolls name back to its latest snapshot and returns the
// ID of the rollback container.
func (u *Updater) rollbackFromStore(ctx context.Context, name string) (string, error) {
	data, err := u.store.GetLatestSnapshot(name)
	if err != nil {
		return "", fmt.Errorf("get snapshot: %w", err)
	}
	if data == nil {
		return "", fmt.Errorf("no snapshot found for %s", name)
	}
	return rollbackContainer(ctx, u.docker, u.store, name, data, u.log)
}
