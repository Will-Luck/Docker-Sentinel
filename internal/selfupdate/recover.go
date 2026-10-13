package selfupdate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/moby/moby/api/types/container"
)

// Recover undoes a swap a host restart interrupted, from the old instance's
// side. If the host went down between the helper stopping the old container
// and starting the new one, the old container (restart policy "always" during
// the swap) comes back under its parked "<name>-old-<unix>" name while the
// replacement was never started. This instance, finding itself so named with
// no helper alive and a replacement that is created or exited, removes the
// replacement, takes its name back and puts its restart policy back as the
// in-progress marker in dataDir recorded it. hostname is this container's
// hostname, whose prefix is its container ID. It returns the rolled-back
// outcome, or nil when there was nothing to recover; the marker is removed.
func Recover(ctx context.Context, d Docker, hostname, dataDir string, log Logger) *Outcome {
	if len(hostname) < 12 {
		return nil
	}
	containers, err := d.ListAllContainers(ctx)
	if err != nil {
		return nil
	}

	var self *container.Summary
	for i := range containers {
		if strings.HasPrefix(containers[i].ID, hostname) {
			self = &containers[i]
			break
		}
	}
	if self == nil || len(self.Names) == 0 {
		return nil
	}
	m := oldInstanceRE.FindStringSubmatch(strings.TrimPrefix(self.Names[0], "/"))
	if m == nil {
		return nil
	}
	base := m[1]

	marker := readMarker(dataDir)
	var replacement *container.Summary
	for i := range containers {
		c := &containers[i]
		if c.ID == self.ID || len(c.Names) == 0 {
			continue
		}
		if c.Labels[HelperLabel] == "true" && !helperDead(*c) {
			return nil // a helper is still at work on this swap
		}
		if strings.TrimPrefix(c.Names[0], "/") == base {
			replacement = c
		}
	}
	if replacement == nil {
		return nil
	}
	switch replacement.State {
	case container.StateCreated, container.StateExited:
		// Never started, or gone: nothing of the new version is running.
	case container.StateRunning, container.StateRestarting:
		// Only an uncommitted swap whose helper has died is undone: this
		// instance's own in-progress marker says so, and no helper is alive. A
		// replacement running without that was committed and stays.
		if marker == nil || marker.OldID != self.ID || (marker.NewID != "" && marker.NewID != replacement.ID) {
			return nil
		}
	default:
		return nil
	}

	log.Warn("this instance was parked by a self-update that never finished (the host restarted mid-swap); putting it back",
		"parked_as", self.Names[0], "replacement", replacement.ID)
	if replacement.State == container.StateRunning || replacement.State == container.StateRestarting {
		_ = d.StopContainer(ctx, replacement.ID, 10)
	}
	if err := d.RemoveContainer(ctx, replacement.ID); err != nil {
		log.Error("self-update recovery: could not remove the replacement", "error", err)
		return nil
	}
	if err := d.RenameContainer(ctx, self.ID, base); err != nil {
		log.Error("self-update recovery: could not take the name back", "name", base, "error", err)
		return nil
	}

	out := &Outcome{Result: ResultRolledBack, ContainerName: base,
		Error: "the host restarted before the new version was started; the previous version was put back"}
	if marker != nil && marker.OldID == self.ID {
		out.OldImage, out.NewImage, out.FinishedAt = marker.OldImage, marker.NewImage, marker.FinishedAt
		if marker.OldRestart != nil {
			if u, ok := d.(docker.RestartPolicyUpdater); ok {
				if err := u.UpdateRestartPolicy(ctx, self.ID, *marker.OldRestart); err != nil {
					log.Warn("self-update recovery: could not restore the restart policy", "error", err)
				}
			}
		}
	}
	if out.FinishedAt.IsZero() {
		out.FinishedAt = timeNow()
	}
	if dataDir != "" {
		_ = os.Remove(filepath.Join(dataDir, OutcomeFile))
	}
	log.Warn("self-update recovered: the previous version is running again", "name", base)
	return out
}

// ReadOutcomeFile returns the outcome file in dir whatever its result (in
// progress included), or nil when there is none or it is unreadable.
func ReadOutcomeFile(dir string) *Outcome {
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, OutcomeFile))
	if err != nil {
		return nil
	}
	var out Outcome
	if json.Unmarshal(data, &out) != nil || out.Result == "" {
		return nil
	}
	return &out
}

func readMarker(dir string) *Outcome {
	if dir == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, OutcomeFile))
	if err != nil {
		return nil
	}
	var out Outcome
	if json.Unmarshal(data, &out) != nil || out.Result != ResultInProgress {
		return nil
	}
	return &out
}
