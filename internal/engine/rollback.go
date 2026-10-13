package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

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
		newID, err := createAndStart(ctx, d, name, rc, verify)
		if err != nil {
			undoTag()
			return "", err
		}
		log.Info("rollback complete", "name", name)
		return newID, nil
	}

	sw := newSwap(d, log, j, swapSpec{kind: swapRollback, name: name, oldID: liveID, old: live, stopTimeout: 30})
	if err := sw.replace(ctx, rc, true, verify); err != nil {
		undoTag()
		if !sw.changed() {
			return "", fmt.Errorf("rollback %s, running container kept: %w", name, err)
		}
		if _, undoErr := sw.undoOrRetry(ctx); undoErr != nil {
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

// createAndStart creates and starts a rollback container when no container
// holds the name, and returns its ID. A container that is created but fails
// to start is kept, so there is something to inspect and start by hand.
func createAndStart(ctx context.Context, d docker.API, name string, rc docker.Recreate, verify func(context.Context, string) error) (string, error) {
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

	liveID, _ := findContainerID(ctx, u.docker, name)
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
	u.publishEvent(events.EventContainerUpdate, name, "rollback succeeded")
	return nil
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
