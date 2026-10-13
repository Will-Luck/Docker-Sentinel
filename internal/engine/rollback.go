package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// rollback recreates a container from its snapshot.
//
// Everything that can fail is checked before the running container is
// touched: the snapshot's image must be on this host (pulled back if its
// tag still resolves to the same image ID) and its tag is pointed at it.
// The container holding the name is then swapped out, and removed only once
// the rollback container exists, runs the snapshot's image and has started.
// On any failure the running container is kept.
func rollback(ctx context.Context, d docker.API, name string, snapshotData []byte, log *logging.Logger) error {
	var inspect container.InspectResponse
	if err := json.Unmarshal(snapshotData, &inspect); err != nil {
		return fmt.Errorf("unmarshal snapshot: %w", err)
	}

	if inspect.Config == nil {
		return fmt.Errorf("inspect %s: container config is nil", name)
	}

	log.Info("rolling back container", "name", name, "image", inspect.Config.Image)

	undoTag, err := ensureImage(ctx, d, inspect.Config.Image, inspect.Image, log)
	if err != nil {
		return fmt.Errorf("rollback image for %s: %w", name, err)
	}
	rc := docker.RecreateConfig(inspect, docker.RecreateOptions{})
	verify := func(ctx context.Context, id string) error {
		if inspect.Image == "" {
			return nil // snapshot from an older version: no image ID recorded
		}
		created, err := d.InspectContainer(ctx, id)
		if err != nil {
			return fmt.Errorf("inspect rollback container: %w", err)
		}
		if created.Image != inspect.Image {
			return fmt.Errorf("rollback container runs image %s, want %s", shortImageID(created.Image), shortImageID(inspect.Image))
		}
		return nil
	}

	liveID, err := findContainerID(ctx, d, name)
	if err != nil {
		undoTag()
		return fmt.Errorf("list containers for rollback: %w", err)
	}
	if liveID == "" {
		if err := createAndStart(ctx, d, name, rc, verify); err != nil {
			undoTag()
			return err
		}
		log.Info("rollback complete", "name", name)
		return nil
	}

	live, err := d.InspectContainer(ctx, liveID)
	if err != nil {
		log.Warn("could not inspect the running container before rollback", "name", name, "error", err)
	}
	sw := newSwap(d, log, name, liveID, live, 30)
	if err := sw.replace(ctx, rc, true, verify); err != nil {
		undoTag()
		if undoErr := sw.undo(ctx); undoErr != nil {
			return fmt.Errorf("rollback %s: %w (and the running container could not be restored: %v)", name, err, undoErr)
		}
		return fmt.Errorf("rollback %s, running container kept: %w", name, err)
	}
	sw.commit(ctx, false)

	log.Info("rollback complete", "name", name, "container_id", truncateID(sw.newID))
	return nil
}

// createAndStart creates and starts a rollback container when no container
// holds the name. A container that is created but fails to start is kept,
// so there is something to inspect and start by hand.
func createAndStart(ctx context.Context, d docker.API, name string, rc docker.Recreate, verify func(context.Context, string) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	newID, err := d.CreateContainer(ctx, name, rc.Config, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		return fmt.Errorf("create rollback container: %w", err)
	}
	if err := verify(ctx, newID); err != nil {
		_ = d.RemoveContainer(ctx, newID)
		return err
	}
	if err := d.StartContainer(ctx, newID); err != nil {
		return fmt.Errorf("start rollback container: %w", err)
	}
	return nil
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
func ensureImage(ctx context.Context, d docker.API, ref, imageID string, log *logging.Logger) (func(), error) {
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
	return retag(ctx, d, ref, imageID, log)
}

// retag points the tag ref at imageID, so that a container running that
// image shows it by name and Sentinel's scans can resolve it. The returned
// func points ref back at the image it named before.
func retag(ctx context.Context, d docker.API, ref, imageID string, log *logging.Logger) (func(), error) {
	noop := func() {}
	previous, prevErr := d.ImageID(ctx, ref)
	if prevErr == nil && previous == imageID {
		return noop, nil
	}
	if strings.Contains(ref, "@") {
		return noop, fmt.Errorf("%s does not resolve to image %s", ref, shortImageID(imageID))
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
	return rollback(ctx, d, name, data, log)
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

	if err := RollbackFromStore(ctx, u.docker, u.store, name, u.log); err != nil {
		u.publishEvent(events.EventContainerUpdate, name, "rollback failed: "+err.Error())
		u.notifier.Notify(context.WithoutCancel(ctx), notify.Event{
			Type:          notify.EventRollbackFailed,
			ContainerName: name,
			Error:         err.Error(),
			Timestamp:     u.clock.Now(),
		})
		return err
	}
	u.publishEvent(events.EventContainerUpdate, name, "rollback succeeded")
	return nil
}
