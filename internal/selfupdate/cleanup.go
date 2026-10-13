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
//
// The old container is the helper's way back until the helper has judged the
// new instance, so none is touched while a helper is active (running, created,
// restarting or paused) or the outcome file in dataDir says the swap is in
// progress. The helper removes the old container itself when the swap
// succeeds; this only clears what a helper left behind, and only for an old
// instance that has the sentinel.self label, is exited or created, and whose
// replacement (the same name without the suffix) is running and, if it has a
// health check, healthy. A helper is only removed when it has exited.
// Anonymous volumes are kept.
func CleanupLeftovers(ctx context.Context, d Docker, dataDir string, log Logger) {
	containers, err := d.ListAllContainers(ctx)
	if err != nil {
		log.Debug("self-update cleanup: list containers failed", "error", err)
		return
	}

	helperActive := InProgress(dataDir)
	runningID := make(map[string]string)
	for _, c := range containers {
		if len(c.Names) == 0 {
			continue
		}
		if c.Labels[HelperLabel] == "true" && c.State != container.StateExited {
			helperActive = true
		}
		if c.State == container.StateRunning {
			runningID[strings.TrimPrefix(c.Names[0], "/")] = c.ID
		}
	}

	healthy := make(map[string]bool) // replacement name -> verdict, looked up once
	replacementGood := func(name string) bool {
		if v, ok := healthy[name]; ok {
			return v
		}
		good := false
		if id := runningID[name]; id != "" {
			if info, err := d.InspectContainer(ctx, id); err == nil && info.State != nil &&
				info.State.Running && !info.State.Restarting &&
				(info.State.Health == nil || info.State.Health.Status == container.Healthy) {
				good = true
			}
		}
		healthy[name] = good
		return good
	}

	for _, c := range containers {
		if len(c.Names) == 0 || (c.State != container.StateExited && c.State != container.StateCreated) {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		isHelper := c.Labels[HelperLabel] == "true" && c.State == container.StateExited
		if c.Labels[HelperLabel] == "true" && !isHelper {
			continue
		}
		isOld := false
		if !isHelper && c.Labels["sentinel.self"] == "true" && !helperActive {
			if m := oldInstanceRE.FindStringSubmatch(name); m != nil && replacementGood(m[1]) {
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
