package engine

import (
	"context"
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

	if err := u.docker.RemoveImage(ctx, oldImageID); err != nil {
		u.log.Warn("cleanup: failed to remove old image", "image", oldImageID, "error", err)
		return
	}

	metrics.ImageCleanups.Inc()
	u.log.Info("cleanup: removed old image", "image", oldImageID, "container", containerName)
}
