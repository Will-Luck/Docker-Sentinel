package engine

import (
	"context"
	"slices"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
)

// cleanupOldImage removes the previous image after a successful update.
// It checks that no other running container references the old image ID
// before removal. Feature controlled by config.ImageCleanup().
func (u *Updater) cleanupOldImage(ctx context.Context, oldImageID, containerName string) {
	if !u.cfg.ImageCleanup() {
		return
	}
	if oldImageID == "" {
		return
	}

	containers, err := u.docker.ListContainers(ctx)
	if err != nil {
		u.log.Warn("cleanup: failed to list containers", "error", err)
		return
	}

	for _, c := range containers {
		inspect, err := u.docker.InspectContainer(ctx, c.ID)
		if err != nil {
			continue
		}
		if inspect.Image == oldImageID {
			cName := strings.TrimPrefix(inspect.Name, "/")
			u.log.Info("cleanup: old image still in use, skipping removal",
				"image", oldImageID, "used_by", cName)
			return
		}
	}

	// An image kept as a backup stays: deleting it by ID would take the
	// backup tag with it.
	if u.isImageBackup() {
		backedUp, err := u.hasBackupTag(ctx, oldImageID)
		if err != nil || backedUp {
			u.log.Info("cleanup: old image kept as an image backup", "image", oldImageID, "error", err)
			return
		}
	}

	if err := u.docker.RemoveImage(ctx, oldImageID); err != nil {
		u.log.Warn("cleanup: failed to remove old image", "image", oldImageID, "error", err)
		return
	}

	metrics.ImageCleanups.Inc()
	u.log.Info("cleanup: removed old image", "image", oldImageID, "container", containerName)
}

// imageBackupsKept is how many image backups are kept per repository.
const imageBackupsKept = 3

// hasBackupTag reports whether the image carries an image backup tag.
func (u *Updater) hasBackupTag(ctx context.Context, imageID string) (bool, error) {
	images, err := u.docker.ListImages(ctx)
	if err != nil {
		return false, err
	}
	for _, img := range images {
		if img.ID == imageID && slices.ContainsFunc(img.RepoTags, func(t string) bool { return strings.Contains(t, backupTagMarker) }) {
			return true, nil
		}
	}
	return false, nil
}

// pruneImageBackups removes the backup tags of imageRef's repository beyond
// the newest imageBackupsKept (the tags sort by their timestamp). Removing
// a tag deletes the image only when nothing else uses it.
func (u *Updater) pruneImageBackups(ctx context.Context, imageRef string) {
	images, err := u.docker.ListImages(ctx)
	if err != nil {
		u.log.Debug("image backup: could not list images to prune old backups", "error", err)
		return
	}
	prefix := backupBase(imageRef) + backupTagMarker
	var tags []string
	for _, img := range images {
		for _, t := range img.RepoTags {
			if strings.HasPrefix(t, prefix) {
				tags = append(tags, t)
			}
		}
	}
	slices.Sort(tags)
	for _, t := range tags[:max(len(tags)-imageBackupsKept, 0)] {
		if err := u.docker.RemoveImage(ctx, t); err != nil {
			u.log.Warn("image backup: could not remove an old backup", "tag", t, "error", err)
			continue
		}
		u.log.Info("image backup: removed an old backup", "tag", t)
	}
}
