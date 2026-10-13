package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/moby/moby/api/types/container"
)

// OutcomeFile is the file, in the data directory, the helper records its
// result in. The new instance reports it on startup and removes it.
const OutcomeFile = "self-update-outcome.json"

// Result values of an Outcome.
const (
	// ResultInProgress marks the swap as under way: the helper writes it before
	// it touches anything, and the old container must be left alone until it
	// is replaced by the final outcome.
	ResultInProgress = "in_progress"
	ResultSuccess    = "success"
	ResultRolledBack = "rolled_back"
	ResultFailed     = "failed"
)

// Outcome is what the helper records.
type Outcome struct {
	Result        string    `json:"result"`
	ContainerName string    `json:"container_name,omitempty"`
	Error         string    `json:"error,omitempty"`
	OldImage      string    `json:"old_image"`
	NewImage      string    `json:"new_image"`
	FinishedAt    time.Time `json:"finished_at"`
	// The fields below journal an in-progress swap, so an old instance that
	// comes back after a restart can undo it (see Recover).
	OldID      string                   `json:"old_id,omitempty"`
	NewID      string                   `json:"new_id,omitempty"`
	FinalName  string                   `json:"final_name,omitempty"`
	OldRestart *container.RestartPolicy `json:"old_restart,omitempty"`
}

// Runner carries the helper's dependencies. Poll is how often the new
// container is inspected while it settles (default 1s).
type Runner struct {
	Docker Docker
	Log    Logger
	Poll   time.Duration
}

// Run does the swap the Spec describes and records the outcome. The old
// container is stopped first, which frees its published ports and database
// lock; if the new container then fails to start, or does not stay up, it is
// removed and the old one is started again under its own name.
func (r Runner) Run(ctx context.Context, spec Spec) Outcome {
	if err := WriteOutcome(spec.OutcomeDir, Outcome{
		Result: ResultInProgress, OldImage: spec.OldImage, NewImage: spec.NewImage, FinishedAt: time.Now().UTC(),
		OldID: spec.OldID, NewID: spec.NewID, FinalName: spec.FinalName, OldRestart: &spec.OldRestart,
	}); err != nil {
		r.Log.Warn("self-update helper: could not mark the swap as in progress", "error", err)
	}
	out := r.run(ctx, spec)
	out.ContainerName, out.OldImage, out.NewImage, out.FinishedAt = spec.FinalName, spec.OldImage, spec.NewImage, time.Now().UTC()
	if err := WriteOutcome(spec.OutcomeDir, out); err != nil {
		r.Log.Warn("self-update helper: could not record the outcome", "error", err)
	}
	return out
}

func (r Runner) run(ctx context.Context, spec Spec) Outcome {
	d := r.Docker
	poll := r.Poll
	if poll == 0 {
		poll = time.Second
	}

	// Let the old instance report its result before it is stopped.
	if !sleepCtx(ctx, spec.Delay) {
		return Outcome{Result: ResultFailed, Error: ctx.Err().Error()}
	}

	// Between "stop old" and "start new" a daemon restart would leave no
	// Sentinel: a container stopped by hand is not restarted by unless-stopped
	// or on-failure. Under "always" it comes back with the daemon.
	r.setRestart(ctx, spec.OldID, container.RestartPolicy{Name: container.RestartPolicyAlways})

	r.Log.Info("self-update helper: stopping the old container", "id", spec.OldID)
	if err := d.StopContainer(ctx, spec.OldID, spec.StopTimeout); err != nil {
		r.Log.Error("self-update helper: could not stop the old container, giving up", "error", err)
		r.removeNew(ctx, spec)
		_ = d.RenameContainer(ctx, spec.OldID, spec.FinalName)
		r.setRestart(ctx, spec.OldID, spec.OldRestart)
		return Outcome{Result: ResultFailed, Error: fmt.Sprintf("stop the old container: %v", err)}
	}

	r.Log.Info("self-update helper: starting the new container", "id", spec.NewID)
	if err := d.StartContainer(ctx, spec.NewID); err != nil {
		return r.rollBack(ctx, spec, fmt.Sprintf("start the new container: %v", err))
	}

	if reason := r.settle(ctx, spec, poll); reason != "" {
		return r.rollBack(ctx, spec, reason)
	}

	r.Log.Info("self-update helper: new container is up, removing the old one", "old", spec.OldName)
	// The swap is committed: the new container now restarts as it was meant
	// to, and the old one must not come back with the daemon.
	if spec.NewRestart.Name != "" && spec.NewRestart.Name != container.RestartPolicyDisabled {
		r.setRestart(ctx, spec.NewID, spec.NewRestart)
	}
	if err := d.RemoveContainer(ctx, spec.OldID); err != nil {
		r.Log.Warn("self-update helper: could not remove the old container; it is cleaned up at the next start", "error", err)
		r.setRestart(ctx, spec.OldID, container.RestartPolicy{Name: container.RestartPolicyDisabled})
	}
	return Outcome{Result: ResultSuccess}
}

// settle waits until the new container has stayed running (and healthy, when
// it has a health check) for spec.Settle. It returns why the swap should be
// undone, or "" when the new container is good.
func (r Runner) settle(ctx context.Context, spec Spec, poll time.Duration) string {
	start := time.Now()
	// A health check that is still starting gets twice the settle time.
	limit := 2 * spec.Settle
	for {
		info, err := r.Docker.InspectContainer(ctx, spec.NewID)
		switch {
		case err != nil:
			return fmt.Sprintf("inspect the new container: %v", err)
		case info.State == nil || !info.State.Running || info.State.Restarting:
			return "the new container stopped or is restarting"
		}
		elapsed := time.Since(start)
		health := info.State.Health
		switch {
		case health != nil && health.Status == container.Unhealthy:
			return "the new container is unhealthy"
		case elapsed >= spec.Settle && (health == nil || health.Status == container.Healthy):
			return ""
		case elapsed >= limit:
			return "the new container did not become healthy in time"
		}
		if !sleepCtx(ctx, poll) {
			return "interrupted: " + ctx.Err().Error()
		}
	}
}

// setRestart changes a container's restart policy, when the Docker client can.
func (r Runner) setRestart(ctx context.Context, id string, p container.RestartPolicy) {
	if p.Name == "" {
		p.Name = container.RestartPolicyDisabled // a container with no policy has "no"
	}
	u, ok := r.Docker.(docker.RestartPolicyUpdater)
	if !ok {
		return
	}
	if err := u.UpdateRestartPolicy(ctx, id, p); err != nil {
		r.Log.Warn("self-update helper: could not change the restart policy", "id", id, "policy", string(p.Name), "error", err)
	}
}

// removeNew removes the not-yet-good replacement.
func (r Runner) removeNew(ctx context.Context, spec Spec) {
	_ = r.Docker.StopContainer(ctx, spec.NewID, 10)
	_ = r.Docker.RemoveContainer(ctx, spec.NewID)
}

// rollBack removes the new container and starts the old one again under its
// own name.
func (r Runner) rollBack(ctx context.Context, spec Spec, reason string) Outcome {
	r.Log.Error("self-update helper: the new container is not good, restoring the old one", "reason", reason)
	r.removeNew(ctx, spec)
	if err := r.Docker.RenameContainer(ctx, spec.OldID, spec.FinalName); err != nil {
		r.Log.Error("self-update helper: could not rename the old container back", "error", err)
	}
	if err := r.Docker.StartContainer(ctx, spec.OldID); err != nil {
		r.Log.Error("self-update helper: could not start the old container again", "error", err)
		return Outcome{Result: ResultFailed, Error: fmt.Sprintf("%s; and the old container would not start again: %v", reason, err)}
	}
	r.setRestart(ctx, spec.OldID, spec.OldRestart)
	return Outcome{Result: ResultRolledBack, Error: reason}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// WriteOutcome records out in dir. An empty dir records nothing.
func WriteOutcome(dir string, out Outcome) error {
	if dir == "" {
		return nil
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, OutcomeFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, OutcomeFile))
}

// ReportOutcome logs the outcome an earlier self-update left in dir, then
// removes it. It returns the outcome, or nil when there is none.
func ReportOutcome(dir string, log Logger) *Outcome {
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, OutcomeFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Debug("self-update outcome unreadable", "error", err)
		}
		return nil
	}
	var out Outcome
	if json.Unmarshal(data, &out) == nil && out.Result == ResultInProgress {
		// The swap is still under way (or its helper died): leave the marker
		// for whoever reads it next, and do not report it.
		log.Debug("a self-update is still in progress")
		return nil
	}
	_ = os.Remove(path)
	if err := json.Unmarshal(data, &out); err != nil {
		log.Warn("self-update outcome file is corrupt, ignored", "error", err)
		return nil
	}
	switch out.Result {
	case ResultSuccess:
		log.Info("self-update completed", "from", out.OldImage, "to", out.NewImage)
	case ResultRolledBack:
		log.Warn("self-update was rolled back: the new version did not start cleanly, this is the previous version",
			"attempted", out.NewImage, "reason", out.Error)
	default:
		log.Error("self-update failed", "attempted", out.NewImage, "reason", out.Error)
	}
	return &out
}

// inProgressStale is how long an in-progress marker is believed. A helper
// finishes in about a minute; a marker older than this belongs to a helper
// that died, and must not hold the cleanup back for ever.
const inProgressStale = 15 * time.Minute

// InProgress reports whether the data directory holds a fresh in-progress
// marker, that is a swap whose helper has not finished.
func InProgress(dir string) bool {
	if dir == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, OutcomeFile))
	if err != nil {
		return false
	}
	var out Outcome
	if json.Unmarshal(data, &out) != nil || out.Result != ResultInProgress {
		return false
	}
	return time.Since(out.FinishedAt) < inProgressStale
}

// WatchOutcome reports the outcome of a self-update that was still in progress
// when this instance started. The helper writes the final outcome about a
// settle period after the new instance came up, so the first look at startup
// sees "in progress". It polls, for at most bound, until the marker turns
// final, then logs it (ReportOutcome) and calls onOutcome once with it. It
// returns at once when there is no in-progress marker, and the goroutine stops
// when ctx ends.
func WatchOutcome(ctx context.Context, dir string, log Logger, bound, poll time.Duration, onOutcome func(*Outcome)) {
	if readMarker(dir) == nil {
		return
	}
	go func() {
		deadline := time.NewTimer(bound)
		defer deadline.Stop()
		tick := time.NewTicker(poll)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				log.Warn("a self-update was still marked in progress after waiting; its outcome is not reported")
				return
			case <-tick.C:
				if _, err := os.Stat(filepath.Join(dir, OutcomeFile)); err != nil {
					return // somebody else reported it
				}
				if readMarker(dir) != nil {
					continue
				}
				if out := ReportOutcome(dir, log); out != nil && onOutcome != nil {
					onOutcome(out)
				}
				return
			}
		}
	}()
}
