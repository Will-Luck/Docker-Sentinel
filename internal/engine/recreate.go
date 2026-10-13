package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/guardian"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// replacedSuffix is added to a container's name while Sentinel replaces it.
const replacedSuffix = "-sentinel-old"

// setAside reports whether name is a container that a journalled swap has
// renamed out of the way (<name>-sentinel-old). Such a container is never
// scanned or updated itself; a container that merely has such a name is.
func (u *Updater) setAside(name string) bool {
	base, ok := strings.CutSuffix(name, replacedSuffix)
	return ok && base != "" && u.store.HasSwap(base)
}

// swapTimeout bounds each group of Docker calls in a swap. The calls run
// without the caller's cancellation, so a shutdown cannot stop a swap
// halfway and leave the container missing.
const swapTimeout = 3 * time.Minute

// Swap kinds, recorded in the journal.
const (
	swapUpdate   = "update"
	swapFinalise = "finalise"
	swapRollback = "rollback"
	swapConsumer = "consumer"
)

// swapJournal persists swap records (see store.SwapRecord). *store.Store
// implements it. A nil journal turns journalling off.
type swapJournal interface {
	SaveSwap(rec store.SwapRecord) error
	DeleteSwap(name string) error
}

// swap replaces a container without ever leaving its service missing.
//
// replace renames the old container aside (name + replacedSuffix), creates
// the replacement under the real name, stops the old one and starts the
// replacement. Nothing is deleted on the way, so any failure can be undone:
// undo removes the half-made replacement, gives the old container its name
// back and starts it again if it was running. Once the replacement is known
// to be good, commit removes the old container. Every step is written to the
// journal first, so RecoverInterruptedUpdates can finish or undo a swap that
// Sentinel did not complete.
type swap struct {
	d       docker.API
	log     *logging.Logger
	journal swapJournal
	rec     store.SwapRecord

	name        string
	oldID       string
	oldRunning  bool
	oldLabelled bool // the old container carries the maintenance label
	stopTimeout int
	restart     container.RestartPolicy

	renamed        bool // the old container carries the replaced name
	stopped        bool // the old container was stopped by this swap
	restartChanged bool // the old container's restart policy was set to "no"
	newID          string
	newStarted     bool
}

// swapSpec describes the container a swap replaces.
type swapSpec struct {
	kind        string
	name        string
	oldID       string
	old         container.InspectResponse // the old container, as inspected
	stopTimeout int
	snapshot    []byte // the old container's inspect JSON, kept for recovery
	// removeVolumes makes commit remove the old container's anonymous
	// volumes (remove_volumes: the replacement started on fresh ones).
	removeVolumes bool
}

func newSwap(d docker.API, log *logging.Logger, j swapJournal, spec swapSpec) *swap {
	s := &swap{
		d:       d,
		log:     log,
		journal: j,
		name:    spec.name,
		oldID:   spec.oldID,
		// A missing state is treated as running, so a restore never
		// leaves the service stopped.
		oldRunning:  spec.old.State == nil || spec.old.State.Running,
		stopTimeout: spec.stopTimeout,
	}
	if spec.old.Config != nil {
		s.oldLabelled = guardian.HasMaintenanceLabel(spec.old.Config.Labels)
	}
	autoRemove := false
	if spec.old.HostConfig != nil {
		autoRemove = spec.old.HostConfig.AutoRemove
		s.restart = spec.old.HostConfig.RestartPolicy
	}
	s.rec = store.SwapRecord{
		Name:          spec.name,
		Kind:          spec.kind,
		OldID:         spec.oldID,
		OldRunning:    s.oldRunning,
		AutoRemove:    autoRemove,
		RemoveVolumes: spec.removeVolumes,
		Snapshot:      spec.snapshot,
	}
	return s
}

// swapError says at which step a swap failed.
type swapError struct {
	stage string // "journal", "rename", "create", "verify", "stop" or "start"
	err   error
}

func (e *swapError) Error() string { return e.stage + ": " + e.err.Error() }
func (e *swapError) Unwrap() error { return e.err }

// save writes the swap's journal record.
func (s *swap) save() error {
	if s.journal == nil {
		return nil
	}
	return s.journal.SaveSwap(s.rec)
}

// forget removes the swap's journal record, once nothing is left to do.
func (s *swap) forget() {
	if s.journal == nil {
		return
	}
	if err := s.journal.DeleteSwap(s.name); err != nil {
		s.log.Warn("could not clear the swap journal", "name", s.name, "error", err)
	}
}

// replace swaps in the replacement described by rc. When start is false
// the replacement is created but not started (for a container that was not
// running). verify, if set, checks the created container before the old
// one is stopped. On error, call undo (or undoOrRetry).
func (s *swap) replace(ctx context.Context, rc docker.Recreate, start bool, verify func(ctx context.Context, id string) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	s.removeLeftover(ctx)
	s.rec.Started = time.Now().UTC()
	if err := s.save(); err != nil {
		return &swapError{stage: "journal", err: err}
	}
	if err := s.d.RenameContainer(ctx, s.oldID, s.name+replacedSuffix); err != nil {
		s.forget()
		return &swapError{stage: "rename", err: err}
	}
	s.renamed = true

	newID, err := s.d.CreateContainer(ctx, s.name, rc.Config, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		return &swapError{stage: "create", err: err}
	}
	s.newID = newID
	s.rec.NewID = newID
	if err := s.save(); err != nil {
		s.log.Warn("could not journal the replacement", "name", s.name, "error", err)
	}
	if verify != nil {
		if err := verify(ctx, newID); err != nil {
			return &swapError{stage: "verify", err: err}
		}
	}

	if s.oldRunning {
		s.disableRestart(ctx)
		if err := s.d.StopContainer(ctx, s.oldID, s.stopTimeout); err != nil {
			// Never start the replacement while the old container may still
			// run on the same volumes and ports.
			old, inspectErr := s.d.InspectContainer(ctx, s.oldID)
			if inspectErr != nil || old.State == nil || old.State.Running {
				return &swapError{stage: "stop", err: err}
			}
			s.log.Warn("stopping the old container reported an error but it has stopped", "name", s.name, "error", err)
		}
		s.stopped = true
	}
	if !start {
		return nil
	}
	if err := s.d.StartContainer(ctx, newID); err != nil {
		return &swapError{stage: "start", err: err}
	}
	s.newStarted = true
	return nil
}

// removeLeftover removes a stopped <name>-sentinel-old that an earlier swap
// left behind after it had finished (its removal failed), so the name is
// free. It only does so when the container being replaced runs and is not
// itself an unvalidated update, which shows that swap had completed.
func (s *swap) removeLeftover(ctx context.Context) {
	if !s.oldRunning || s.oldLabelled {
		return
	}
	left, err := s.d.InspectContainer(ctx, s.name+replacedSuffix)
	if err != nil || left.ID == "" || left.ID == s.oldID || (left.State != nil && left.State.Running) {
		return
	}
	s.log.Warn("removing a container an earlier update left behind", "name", s.name+replacedSuffix)
	if err := s.d.RemoveContainer(ctx, left.ID); err != nil {
		s.log.Warn("could not remove the leftover container", "name", s.name+replacedSuffix, "error", err)
	}
}

// disableRestart sets the old container's restart policy to "no" before it
// is stopped, so a reboot during the swap cannot start it next to its
// replacement. undo puts the policy back. Failing to change it only loses
// that protection.
func (s *swap) disableRestart(ctx context.Context) {
	updater, ok := s.d.(docker.RestartPolicyUpdater)
	if !ok || s.restart.IsNone() {
		return
	}
	s.rec.RestartPolicy = string(s.restart.Name)
	s.rec.RestartRetries = s.restart.MaximumRetryCount
	s.rec.RestartChanged = true
	if err := s.save(); err != nil {
		s.log.Warn("could not journal the restart policy", "name", s.name, "error", err)
	}
	if err := updater.UpdateRestartPolicy(ctx, s.oldID, container.RestartPolicy{Name: container.RestartPolicyDisabled}); err != nil {
		s.log.Warn("could not disable the old container's restart policy", "name", s.name, "error", err)
		return
	}
	s.restartChanged = true
}

// changed reports whether replace got far enough to change anything. When
// it did not (the rename failed), there is nothing to undo.
func (s *swap) changed() bool {
	return s.renamed || s.newID != ""
}

// undo reverts whatever replace did. It returns an error when the old
// container could not be brought back, for example because the daemon
// already deleted a --rm container on stop. It can be called again.
func (s *swap) undo(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	if s.newID != "" {
		if s.newStarted {
			// Stop it cleanly first: it may share volumes with the old
			// container, which starts on them next.
			if err := s.d.StopContainer(ctx, s.newID, s.stopTimeout); err != nil {
				s.log.Warn("stopping the replacement failed", "name", s.name, "error", err)
			}
		}
		if err := s.d.RemoveContainer(ctx, s.newID); err != nil && !docker.IsNotFound(err) {
			return fmt.Errorf("remove the replacement: %w", err)
		}
		s.newID = ""
		s.newStarted = false
	}
	if s.renamed {
		if err := s.d.RenameContainer(ctx, s.oldID, s.name); err != nil {
			return fmt.Errorf("give the old container its name back: %w", err)
		}
		s.renamed = false
	}
	if s.restartChanged {
		if updater, ok := s.d.(docker.RestartPolicyUpdater); ok {
			if err := updater.UpdateRestartPolicy(ctx, s.oldID, s.restart); err != nil {
				s.log.Warn("could not restore the old container's restart policy", "name", s.name, "error", err)
			}
		}
		s.restartChanged = false
	}
	if s.stopped {
		if err := s.d.StartContainer(ctx, s.oldID); err != nil {
			return fmt.Errorf("start the old container again: %w", err)
		}
		s.stopped = false
	}
	s.forget()
	return nil
}

// undoOrRetry undoes the swap and, if that fails while the old container
// still exists, tries once more. gone reports that the old container no
// longer exists (a --rm container the daemon deleted on stop), so only
// rebuilding it from its snapshot can bring the service back. When the old
// container exists but cannot be restored, it is never rebuilt, which would
// leave two copies.
func (s *swap) undoOrRetry(ctx context.Context) (gone bool, err error) {
	if err = s.undo(ctx); err == nil {
		return false, nil
	}
	if _, inspectErr := s.d.InspectContainer(context.WithoutCancel(ctx), s.oldID); docker.IsNotFound(inspectErr) {
		return true, err
	}
	s.log.Warn("restoring the old container failed, trying again", "name", s.name, "error", err)
	if err = s.undo(ctx); err == nil {
		return false, nil
	}
	return false, err
}

// commit removes the old container once the replacement is good. A
// failure leaves the stopped old container under its replaced name and the
// journal record in place, so RecoverInterruptedUpdates removes it at the
// next start (or the next swap of the same container does).
func (s *swap) commit(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	s.rec.Committed = true
	if err := s.save(); err != nil {
		s.log.Warn("could not journal the commit", "name", s.name, "error", err)
	}
	var err error
	if s.rec.RemoveVolumes {
		err = s.d.RemoveContainerWithVolumes(ctx, s.oldID)
	} else {
		err = s.d.RemoveContainer(ctx, s.oldID)
	}
	switch {
	case err == nil, docker.IsNotFound(err):
		// A --rm container is deleted by the daemon when it stops.
		s.forget()
	default:
		s.log.Warn("could not remove the replaced container", "name", s.name+replacedSuffix, "error", err)
	}
	s.renamed = false
	s.stopped = false
	s.restartChanged = false
}

// RecoverInterruptedUpdates finishes or undoes the container swaps that an
// update, rollback, finalise or network-dependent recreate left half done
// because Sentinel stopped part way (a shutdown, a crash, a host reboot).
// Call it once at startup, before any scan or update can run. It acts only on
// swaps recorded in the journal, so a container whose own name happens to
// end in -sentinel-old is never touched. For each record:
//
//   - the swap was committed and the replacement exists: the old container
//     is removed;
//   - otherwise, while the old container exists: the replacement (by its
//     journalled ID only) is removed, the old container gets its name and
//     restart policy back, and is started if it was running;
//   - the old container is gone (a --rm container): the replacement is kept
//     and started;
//   - both are gone: the old container is rebuilt from its snapshot.
//
// It then clears maintenance flags left set in the store and finalises any
// running container still carrying the maintenance label, so
// Docker-Guardian sees it again.
func (u *Updater) RecoverInterruptedUpdates(ctx context.Context) {
	recs, err := u.store.ListSwaps()
	if err != nil {
		u.log.Warn("startup recovery: could not read the swap journal", "error", err)
	}
	for _, rec := range recs {
		u.log.Warn("startup recovery: resolving an interrupted container swap", "name", rec.Name, "kind", rec.Kind)
		changed, err := u.recoverSwap(ctx, rec)
		if err != nil {
			u.log.Error("startup recovery: could not resolve the swap, leaving it for the next start", "name", rec.Name, "error", err)
			continue
		}
		if err := u.store.DeleteSwap(rec.Name); err != nil {
			u.log.Warn("startup recovery: could not clear the swap journal", "name", rec.Name, "error", err)
		}
		if changed {
			if current, err := u.docker.InspectContainer(ctx, rec.Name); err == nil {
				u.afterProviderChange(ctx, rec.Name, current.ID, rec.OldID, rec.NewID)
			}
		}
	}

	all, err := u.docker.ListAllContainers(ctx)
	if err != nil {
		u.log.Warn("startup recovery: could not list containers", "error", err)
		return
	}
	for _, c := range all {
		name := containerName(c)
		if on, err := u.store.GetMaintenance(name); err == nil && on {
			if err := u.store.SetMaintenance(name, false); err != nil {
				u.log.Warn("startup recovery: could not clear the maintenance flag", "name", name, "error", err)
			}
		}
		if c.State == container.StateRunning && guardian.HasMaintenanceLabel(c.Labels) {
			u.log.Warn("startup recovery: finishing an interrupted update (removing the maintenance label)", "name", name)
			newID, err := u.finaliseContainer(ctx, c.ID, name)
			if err != nil {
				u.log.Error("startup recovery: could not finalise the container", "name", name, "error", err)
				continue
			}
			u.afterProviderChange(ctx, name, newID, c.ID)
		}
	}
}

// recoverSwap resolves one journalled swap; see RecoverInterruptedUpdates.
// changed reports that the container running under the name was replaced
// or (re)started, so the containers depending on it need repairing.
func (u *Updater) recoverSwap(ctx context.Context, rec store.SwapRecord) (changed bool, err error) {
	old, err := u.docker.InspectContainer(ctx, rec.OldID)
	oldGone := docker.IsNotFound(err)
	if err != nil && !oldGone {
		return false, fmt.Errorf("inspect the old container: %w", err)
	}
	var replacement container.InspectResponse
	newGone := true
	if rec.NewID != "" {
		replacement, err = u.docker.InspectContainer(ctx, rec.NewID)
		switch {
		case err == nil:
			newGone = false
		case !docker.IsNotFound(err):
			return false, fmt.Errorf("inspect the replacement: %w", err)
		}
	}

	switch {
	case !oldGone && rec.Committed && !newGone:
		u.log.Warn("startup recovery: the swap had finished, removing the replaced container", "name", rec.Name)
		if rec.RemoveVolumes {
			err = u.docker.RemoveContainerWithVolumes(ctx, rec.OldID)
		} else {
			err = u.docker.RemoveContainer(ctx, rec.OldID)
		}
		if err != nil && !docker.IsNotFound(err) {
			return false, fmt.Errorf("remove the replaced container: %w", err)
		}
		return true, nil

	case !oldGone:
		u.log.Warn("startup recovery: the swap had not finished, restoring the old container", "name", rec.Name)
		if !newGone {
			if replacement.State != nil && replacement.State.Running {
				if err := u.docker.StopContainer(ctx, rec.NewID, 10); err != nil {
					u.log.Warn("startup recovery: stopping the replacement failed", "name", rec.Name, "error", err)
				}
			}
			if err := u.docker.RemoveContainer(ctx, rec.NewID); err != nil && !docker.IsNotFound(err) {
				return false, fmt.Errorf("remove the unfinished replacement: %w", err)
			}
		}
		if containerNameOf(old) != rec.Name {
			if err := u.docker.RenameContainer(ctx, rec.OldID, rec.Name); err != nil {
				return false, fmt.Errorf("give the old container its name back: %w", err)
			}
		}
		if rec.RestartChanged {
			if updater, ok := u.docker.(docker.RestartPolicyUpdater); ok {
				policy := container.RestartPolicy{Name: container.RestartPolicyMode(rec.RestartPolicy), MaximumRetryCount: rec.RestartRetries}
				if err := updater.UpdateRestartPolicy(ctx, rec.OldID, policy); err != nil {
					u.log.Warn("startup recovery: could not restore the restart policy", "name", rec.Name, "error", err)
				}
			}
		}
		if rec.OldRunning && (old.State == nil || !old.State.Running) {
			if err := u.docker.StartContainer(ctx, rec.OldID); err != nil {
				return false, fmt.Errorf("start the restored container: %w", err)
			}
			return true, nil
		}
		return false, nil

	case !newGone:
		u.log.Warn("startup recovery: the old container is gone, keeping its replacement", "name", rec.Name)
		if rec.OldRunning && (replacement.State == nil || !replacement.State.Running) {
			if err := u.docker.StartContainer(ctx, rec.NewID); err != nil {
				return false, fmt.Errorf("start the replacement: %w", err)
			}
		}
		return true, nil

	default:
		if rec.Snapshot == nil {
			return false, errors.New("both containers are gone and no snapshot was journalled")
		}
		u.log.Warn("startup recovery: both containers are gone, rebuilding from the snapshot", "name", rec.Name)
		_, err := rollbackContainer(ctx, u.docker, nil, rec.Name, rec.Snapshot, u.log)
		return err == nil, err
	}
}

// containerNameOf returns an inspected container's name without the
// leading slash.
func containerNameOf(c container.InspectResponse) string {
	if len(c.Name) > 0 && c.Name[0] == '/' {
		return c.Name[1:]
	}
	return c.Name
}
