package selfupdate

import (
	"context"
	"regexp"
	"strings"

	"github.com/moby/moby/api/types/container"
)

// oldInstanceRE matches the name a superseded instance is renamed to.
var oldInstanceRE = regexp.MustCompile(`^(.+)-old-\d+$`)

// CleanupLeftovers removes "<name>-old-<unix>" containers left behind by
// earlier self-updates, and helper containers that did not remove themselves.
// An old instance is only removed when it carries the sentinel.self label, is
// exited or created (never paused, restarting or dead), and the instance that
// replaced it (the same name without the suffix) is running. A helper is only
// removed when it has exited. Anonymous volumes are kept.
func CleanupLeftovers(ctx context.Context, d Docker, log Logger) {
	containers, err := d.ListAllContainers(ctx)
	if err != nil {
		log.Debug("self-update cleanup: list containers failed", "error", err)
		return
	}
	running := make(map[string]bool)
	for _, c := range containers {
		if c.State == container.StateRunning && len(c.Names) > 0 {
			running[strings.TrimPrefix(c.Names[0], "/")] = true
		}
	}
	for _, c := range containers {
		if len(c.Names) == 0 || (c.State != container.StateExited && c.State != container.StateCreated) {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		isHelper := c.Labels[HelperLabel] == "true"
		isOld := false
		if !isHelper && c.Labels["sentinel.self"] == "true" {
			if m := oldInstanceRE.FindStringSubmatch(name); m != nil && running[m[1]] {
				isOld = true
			}
		}
		if !isHelper && !isOld {
			continue
		}
		if err := d.RemoveContainer(ctx, c.ID); err != nil {
			log.Warn("self-update cleanup: remove failed", "name", name, "error", err)
			continue
		}
		log.Info("removed a leftover container from an earlier self-update", "name", name)
	}
}
