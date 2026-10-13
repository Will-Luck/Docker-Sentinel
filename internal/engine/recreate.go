package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/guardian"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// replacedSuffix is added to a container's name while Sentinel replaces it.
const replacedSuffix = "-sentinel-old"

// swapLabel carries the swap token on a replacement container.
const swapLabel = "sentinel.swap"

// swapRecoveryAttempts is how many failed recovery attempts of a swap pass
// before the user is notified.
const swapRecoveryAttempts = 3

// ErrSwapPending is returned for a container whose earlier swap did not
// finish; nothing is changed until it is resolved.
var ErrSwapPending = errors.New("an earlier update of this container did not finish")

// ErrNoSwap is returned by DiscardSwap when there is no record to discard.
var ErrNoSwap = errors.New("no unfinished swap is recorded for this container")

// setAside reports whether name is a container that a journalled swap has
// renamed out of the way (<name>-sentinel-old). Such a container is never
// scanned or updated itself; a container that merely has such a name is.
func (u *Updater) setAside(name string) bool {
	if base, ok := strings.CutSuffix(name, replacedSuffix); ok && base != "" && u.store.HasSwap(base) {
		return true
	}
	// A failed replacement set aside by an undo (<name>-sentinel-failed,
	// perhaps with a timestamp), while its journal record is kept.
	if i := strings.LastIndex(name, failedSuffix); i > 0 {
		if rec, found, err := u.store.GetSwap(name[:i]); found && err == nil && rec.FailedName == name {
			return true
		}
	}
	return false
}

// swapTimeout bounds each group of Docker calls in a swap. The calls run
// without the caller's cancellation, so a shutdown cannot stop a swap
// halfway and leave the container missing.
var swapTimeout = 3 * time.Minute // a variable so tests can shorten it

// swapBudget is the time a group of swap calls that includes stopping a
// container gets: swapTimeout plus the stop timeout (in seconds) and a
// margin, so a container's own long stop timeout is never cut short.
func swapBudget(stopSeconds int) time.Duration {
	return swapTimeout + time.Duration(max(stopSeconds, 0))*time.Second + swapStopMargin
}

// swapStopMargin is the margin swapBudget adds (a variable so tests can
// shorten it).
var swapStopMargin = stopWatchMargin

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
	GetSwap(name string) (store.SwapRecord, bool, error)
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
	stopTimeout int
	restart     container.RestartPolicy

	renamed        bool // the old container carries the replaced name
	stopped        bool // the old container was stopped by this swap
	restartChanged bool // the old container's restart policy was set to "no"
	newID          string
	newStarted     bool
	kept           bool // undo left the record for recovery on purpose
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
		StopTimeout:   spec.stopTimeout,
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

// newSwapToken returns a random token for the sentinel.swap label.
func newSwapToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// replace swaps in the replacement described by rc. When start is false
// the replacement is created but not started (for a container that was not
// running). verify, if set, checks the created container before the old
// one is stopped. On error, call undo (or undoOrRetry) when changed().
func (s *swap) replace(ctx context.Context, rc docker.Recreate, start bool, verify func(ctx context.Context, id string) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapBudget(s.stopTimeout))
	defer cancel()

	if err := s.resolvePending(ctx); err != nil {
		return &swapError{stage: "journal", err: err}
	}
	s.rec.Token = newSwapToken()
	s.rec.Started = time.Now().UTC()
	if err := s.save(); err != nil {
		return &swapError{stage: "journal", err: err}
	}
	if err := s.d.RenameContainer(ctx, s.oldID, s.name+replacedSuffix); err != nil {
		s.forget()
		return &swapError{stage: "rename", err: err}
	}
	s.renamed = true

	// The token label lets recovery recognise the replacement even if
	// Sentinel stops before its ID is journalled.
	cfg := *rc.Config
	cfg.Labels = maps.Clone(rc.Config.Labels)
	if cfg.Labels == nil {
		cfg.Labels = make(map[string]string)
	}
	cfg.Labels[swapLabel] = s.rec.Token
	newID, err := s.d.CreateContainer(ctx, s.name, &cfg, rc.HostConfig, rc.NetworkingConfig)
	if err != nil {
		return &swapError{stage: "create", err: err}
	}
	s.newID = newID
	s.rec.NewID = newID
	if err := s.save(); err != nil {
		return &swapError{stage: "journal", err: fmt.Errorf("journal the replacement: %w", err)}
	}
	if verify != nil {
		if err := verify(ctx, newID); err != nil {
			return &swapError{stage: "verify", err: err}
		}
	}

	if s.oldRunning {
		s.disableRestart(ctx)
		// The daemon carries on with a stop when Sentinel goes away, so it
		// can stop the old container after recovery has put it back. The
		// journal says when that can happen, and recovery watches for it.
		s.rec.Stopping = true
		if err := s.save(); err != nil {
			s.rec.Stopping = false
			return &swapError{stage: "journal", err: fmt.Errorf("journal the stop: %w", err)}
		}
		err := s.d.StopContainer(ctx, s.oldID, s.stopTimeout)
		if ctx.Err() == nil {
			// The call returned: the daemon is done with the stop. If this
			// save fails, recovery only watches for nothing.
			s.rec.Stopping = false
			_ = s.save()
		}
		if err != nil {
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

// resolvePending deals with the journal record an earlier swap of the same
// container left. A committed one only had the old container's removal
// left to do, which is done now. An uncommitted one is the only way back to
// that swap's old container, so the new swap refuses rather than overwrite
// it; the scan retries its recovery.
func (s *swap) resolvePending(ctx context.Context) error {
	if s.journal == nil {
		return nil
	}
	rec, ok, err := s.journal.GetSwap(s.name)
	if err != nil || !ok {
		return err
	}
	if !rec.Committed {
		return fmt.Errorf("%w (%s)", ErrSwapPending, s.name)
	}
	if rec.OldID != s.oldID {
		if rec.RemoveVolumes {
			err = s.d.RemoveContainerWithVolumes(ctx, rec.OldID)
		} else {
			err = s.d.RemoveContainer(ctx, rec.OldID)
		}
		if err != nil && !docker.IsNotFound(err) && !rec.AutoRemove {
			return fmt.Errorf("remove the container an earlier swap replaced: %w", err)
		}
	}
	return s.journal.DeleteSwap(s.name)
}

// disableRestart sets the old container's restart policy to "no" before it
// is stopped, so a reboot during the swap cannot start it next to its
// replacement. undo puts the policy back. The change is journalled first and
// skipped when that fails, so the policy can never be lost; failing to
// change it only loses the protection.
func (s *swap) disableRestart(ctx context.Context) {
	updater, ok := s.d.(docker.RestartPolicyUpdater)
	if !ok || s.restart.IsNone() {
		return
	}
	s.rec.RestartPolicy = string(s.restart.Name)
	s.rec.RestartRetries = s.restart.MaximumRetryCount
	s.rec.RestartChanged = true
	if err := s.save(); err != nil {
		s.log.Warn("could not journal the restart policy, leaving it as it is", "name", s.name, "error", err)
		s.rec.RestartChanged = false
		return
	}
	if err := updater.UpdateRestartPolicy(ctx, s.oldID, container.RestartPolicy{Name: container.RestartPolicyDisabled}); err != nil {
		s.log.Warn("could not disable the old container's restart policy", "name", s.name, "error", err)
		s.rec.RestartChanged = false
		_ = s.save()
		return
	}
	s.restartChanged = true
}

// changed reports whether replace got far enough to change anything. When
// it did not (the rename failed), there is nothing to undo.
func (s *swap) changed() bool {
	return s.renamed || s.newID != ""
}

// undo reverts whatever replace did and clears the journal record. It
// returns an error when the old container could not be fully brought back
// (its name, its restart policy, or running again); the record then stays,
// so the swap is retried later. A failed rename back or policy restore does
// not stop it starting the old container (under its set-aside name if need
// be), so the service runs meanwhile. It can be called again.
func (s *swap) undo(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapBudget(s.stopTimeout))
	defer cancel()
	// A record undo leaves behind is not an interruption: say so, so
	// recovery reports it as unfinished (see recordRecovery).
	defer func() {
		if (err != nil || s.journal != nil && s.kept) && !s.rec.Unresolved {
			s.rec.Unresolved = true
			_ = s.save()
		}
	}()

	if s.newID != "" {
		if s.newStarted {
			// Stop it cleanly first: it may share volumes with the old
			// container, which starts on them next.
			if err := s.d.StopContainer(ctx, s.newID, s.stopTimeout); err != nil {
				s.log.Warn("stopping the replacement failed", "name", s.name, "error", err)
			}
		}
		if err := s.d.RemoveContainer(ctx, s.newID); err != nil && !docker.IsNotFound(err) {
			// It holds the name the old container needs: set it aside, so
			// the service runs again now, and leave its removal to recovery.
			if aside := s.setFailedAside(ctx, err); aside != nil {
				return aside
			}
		}
		s.newID = ""
		s.newStarted = false
	}
	var renameErr error
	if s.renamed {
		if err := s.d.RenameContainer(ctx, s.oldID, s.name); err != nil {
			renameErr = err
		} else {
			s.renamed = false
		}
	}
	var policyErr error
	if s.restartChanged {
		if updater, ok := s.d.(docker.RestartPolicyUpdater); ok {
			if err := updater.UpdateRestartPolicy(ctx, s.oldID, s.restart); err != nil {
				policyErr = err
			} else {
				s.restartChanged = false
			}
		} else {
			s.restartChanged = false
		}
	}
	// A stop that did not return (it ran out of time) may have stopped the
	// old container, or may still be going in the daemon.
	stopUnknown := !s.stopped && s.rec.Stopping
	if stopUnknown {
		if old, err := s.d.InspectContainer(ctx, s.oldID); err == nil && old.State != nil && !old.State.Running {
			s.stopped = true
		}
	}
	if s.stopped {
		if err := s.d.StartContainer(ctx, s.oldID); err != nil {
			return fmt.Errorf("start the old container again: %w", err)
		}
		s.stopped = false
	}
	if renameErr != nil {
		return fmt.Errorf("give the old container its name back (it runs as %s meanwhile): %w", s.name+replacedSuffix, renameErr)
	}
	if policyErr != nil {
		return fmt.Errorf("restore the old container's restart policy: %w", policyErr)
	}
	if stopUnknown {
		// The daemon may still stop the old container: keep the record, with
		// the stop in flight, so recovery watches it (see resolvePendingSwap).
		s.log.Warn("the old container is back, but its stop did not return; recovery watches it", "name", s.name)
		s.kept = true
		return nil
	}
	if s.rec.FailedName != "" {
		// The old container is back; the record stays so recovery removes
		// the set-aside replacement.
		s.log.Warn("the failed replacement could not be removed; it is set aside and removed by the next recovery",
			"name", s.name, "set_aside_as", s.rec.FailedName)
		s.kept = true
		return nil
	}
	s.forget()
	return nil
}

// leftoverName names the container a swap record leaves besides the one
// under its name: the set-aside failed replacement, or the old container.
func leftoverName(rec store.SwapRecord) string {
	if rec.FailedName != "" {
		return rec.FailedName
	}
	return rec.Name + replacedSuffix
}

// failedSuffix is added to a failed replacement's name when it cannot be
// removed and is set aside.
const failedSuffix = "-sentinel-failed"

// setFailedAside renames the replacement, which could not be removed
// (removeErr), out of the way to <name>-sentinel-failed (with a timestamp
// when that is taken) and turns its restart policy off, so the old
// container can have its name back. The name is journalled first, so the
// scans leave it alone and recovery removes it. It returns an error when
// the replacement could not be renamed either.
func (s *swap) setFailedAside(ctx context.Context, removeErr error) error {
	s.log.Warn("could not remove the failed replacement, setting it aside", "name", s.name, "error", removeErr)
	if updater, ok := s.d.(docker.RestartPolicyUpdater); ok {
		if err := updater.UpdateRestartPolicy(ctx, s.newID, container.RestartPolicy{Name: container.RestartPolicyDisabled}); err != nil {
			s.log.Warn("could not turn off the failed replacement's restart policy", "name", s.name, "error", err)
		}
	}
	names := []string{s.name + failedSuffix, s.name + failedSuffix + "-" + time.Now().UTC().Format("20060102-150405")}
	var err error
	for _, target := range names {
		s.rec.FailedName = target
		if saveErr := s.save(); saveErr != nil {
			s.log.Warn("could not journal the set-aside name", "name", s.name, "error", saveErr)
		}
		if err = s.d.RenameContainer(ctx, s.newID, target); err == nil {
			return nil
		}
	}
	s.rec.FailedName = ""
	_ = s.save()
	return fmt.Errorf("remove the replacement: %w; setting it aside failed too: %v", removeErr, err)
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
// committed journal record in place, so the scan's recovery or the next swap
// of the same container removes it.
func (s *swap) commit(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapTimeout)
	defer cancel()

	s.rec.Committed = true
	saveErr := s.save()
	if saveErr != nil {
		s.log.Warn("could not journal the commit", "name", s.name, "error", saveErr)
	}
	var err error
	if s.rec.RemoveVolumes {
		err = s.d.RemoveContainerWithVolumes(ctx, s.oldID)
	} else {
		err = s.d.RemoveContainer(ctx, s.oldID)
	}
	switch {
	case err == nil, docker.IsNotFound(err), s.rec.AutoRemove:
		// A --rm container is deleted by the daemon when it stops, so its
		// removal may fail while the daemon is still at it.
		s.forget()
	default:
		s.log.Warn("could not remove the replaced container", "name", s.name+replacedSuffix, "error", err)
		// The record must say committed, or recovery would put the replaced
		// container back over the validated one.
		s.rec.Unresolved = true
		if err := s.save(); err != nil {
			s.log.Error("could not journal the commit; recovery may restore the replaced container", "name", s.name, "error", err)
		}
	}
	s.renamed = false
	s.stopped = false
	s.restartChanged = false
}

// pendingSwapError explains, for users, a container whose earlier swap did
// not finish.
func (u *Updater) pendingSwapError(name string) error {
	rec, _, _ := u.store.GetSwap(name)
	switch {
	case !rec.WatchUntil.IsZero():
		return fmt.Errorf("%w: its old container was put back, and Sentinel watches it until %s in case Docker stops it; try again after that",
			ErrSwapPending, rec.WatchUntil.Format(time.TimeOnly))
	case rec.FailedName != "":
		return fmt.Errorf("%w: the failed replacement set aside as %s is still to be removed; Sentinel retries at every scan, or remove it by hand and discard the record with DELETE /api/swaps/%s",
			ErrSwapPending, rec.FailedName, name)
	}
	if rec.Name == "" {
		rec.Name = name
	}
	return fmt.Errorf("%w: Sentinel retries it at every scan; if it cannot, check %s and %s by hand and then discard the record with DELETE /api/swaps/%s",
		ErrSwapPending, name, leftoverName(rec), name)
}

// RecoverInterruptedUpdates finishes or undoes the container swaps that an
// update, rollback, finalise or network-dependent recreate left half done
// because Sentinel stopped part way (a shutdown, a crash, a host reboot).
// Call it once at startup, before any scan or update can run; each scan
// then retries whatever is still pending. It acts only on swaps recorded in
// the journal, so a container whose own name happens to end in
// -sentinel-old is never touched. See recoverSwap for what happens to each.
//
// It then clears maintenance flags left set in the store and finalises any
// running container still carrying the maintenance label, so
// Docker-Guardian sees it again.
//
// An old container put back while the daemon may still be carrying out the
// stop Sentinel asked for is watched in the background, until ctx ends, so
// that it is started again if that stop ends it (see watchRestored).
func (u *Updater) RecoverInterruptedUpdates(ctx context.Context) {
	u.resolvePendingSwaps(ctx)

	all, err := u.docker.ListAllContainers(ctx)
	if err != nil {
		u.log.Warn("startup recovery: could not list containers", "error", err)
		return
	}
	for _, c := range all {
		name := containerName(c)
		if u.setAside(name) || u.store.HasSwap(name) {
			continue // still part of a swap that is not resolved
		}
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

// resolvePendingSwaps tries to resolve every journalled swap whose
// container no update holds right now. It runs at startup and at the start
// of each scan, so a cause that clears up (the daemon was busy, a name was
// briefly taken) needs no restart.
func (u *Updater) resolvePendingSwaps(ctx context.Context) {
	recs, err := u.store.ListSwaps()
	if err != nil {
		u.log.Warn("could not read the swap journal", "error", err)
		return
	}
	watch := false
	for _, rec := range recs {
		if !u.tryLock(rec.Name) {
			continue // a swap of it is in progress
		}
		if u.resolvePendingSwap(ctx, rec, u.clock.Now()) {
			watch = true
		}
		u.unlock(rec.Name)
	}
	if watch {
		u.watchRestored(ctx)
	}
}

// resolvePendingSwap recovers one journalled swap, as of now. After
// swapRecoveryAttempts failures it notifies once, saying what to do. It
// reports whether the record is kept to watch the restored container.
//
// The daemon carries on with a stop when Sentinel goes away. When the old
// container was being stopped (rec.Stopping), recovery may put it back while
// that stop is still running; the stop then ends it, and a restart policy
// does not start a container stopped that way. So the record is kept until
// the stop timeout, plus a margin, has passed (rec.WatchUntil): until then
// each resolution starts the container again if it has stopped, and logs and
// records that in the history.
func (u *Updater) resolvePendingSwap(ctx context.Context, rec store.SwapRecord, now time.Time) (watched bool) {
	watching := !rec.WatchUntil.IsZero()
	if !watching {
		u.log.Warn("resolving an interrupted container swap", "name", rec.Name, "kind", rec.Kind, "attempt", rec.Attempts+1)
	}
	changed, oldKept, action, err := u.recoverSwap(ctx, rec)
	if err != nil {
		// A failed resolution leaves the watch: the scans retry it.
		rec.WatchUntil = time.Time{}
		rec.Attempts++
		u.log.Error("could not resolve the swap, retrying at the next scan", "name", rec.Name, "attempts", rec.Attempts, "error", err)
		if rec.Attempts >= swapRecoveryAttempts && !rec.Notified {
			rec.Notified = true
			u.notifier.Notify(context.WithoutCancel(ctx), notify.Event{
				Type:          notify.EventRollbackFailed,
				ContainerName: rec.Name,
				Error: fmt.Sprintf("Sentinel could not finish an interrupted update of %s after %d attempts (%v). "+
					"Check %s and %s by hand, keep one working copy named %s, then discard the record with DELETE /api/swaps/%s.",
					rec.Name, rec.Attempts, err, rec.Name, leftoverName(rec), rec.Name, rec.Name),
				Timestamp: u.clock.Now(),
			})
		}
		if err := u.store.SaveSwap(rec); err != nil {
			u.log.Warn("could not update the swap journal", "name", rec.Name, "error", err)
		}
		return false
	}
	switch {
	case watching:
		if changed {
			u.startedAgain(ctx, rec, now)
		}
	case rec.FailedName != "":
		// The update recorded its own outcome; only the leftover is gone.
		u.log.Info("removed the failed replacement an undo had set aside", "name", rec.Name, "set_aside_as", rec.FailedName)
	default:
		u.recordRecovery(rec, action, now)
	}
	if oldKept && rec.Stopping {
		if !watching {
			rec.WatchUntil = now.Add(stopWatchWindow(rec.StopTimeout))
			u.log.Info("watching the restored container until the stop Sentinel had asked for has timed out",
				"name", rec.Name, "until", rec.WatchUntil)
		}
		if now.Before(rec.WatchUntil) {
			if err := u.store.SaveSwap(rec); err != nil {
				u.log.Warn("could not journal the watch of the restored container", "name", rec.Name, "error", err)
			}
			if changed {
				if current, err := u.docker.InspectContainer(ctx, rec.Name); err == nil {
					u.afterProviderChange(ctx, rec.Name, current.ID, rec.OldID, rec.NewID)
				}
			}
			return true
		}
	}
	if err := u.store.DeleteSwap(rec.Name); err != nil {
		u.log.Warn("could not clear the swap journal", "name", rec.Name, "error", err)
	}
	if err := u.store.SetMaintenance(rec.Name, false); err != nil {
		u.log.Warn("could not clear the maintenance flag", "name", rec.Name, "error", err)
	}
	u.finishRecovered(ctx, rec, changed)
	return false
}

// finishRecovered settles the container a resolved swap left under the name:
// the containers depending on it are repaired when it changed, and it is
// finalised when it runs with the maintenance label (the old container of a
// finalise swap, or a kept replacement), as at startup.
func (u *Updater) finishRecovered(ctx context.Context, rec store.SwapRecord, changed bool) {
	current, err := u.docker.InspectContainer(ctx, rec.Name)
	if err != nil {
		if !docker.IsNotFound(err) {
			u.log.Warn("could not inspect the recovered container", "name", rec.Name, "error", err)
		}
		return
	}
	if changed {
		u.afterProviderChange(ctx, rec.Name, current.ID, rec.OldID, rec.NewID)
	}
	if current.State == nil || !current.State.Running || current.Config == nil || !guardian.HasMaintenanceLabel(current.Config.Labels) {
		return
	}
	u.log.Warn("finishing an interrupted update (removing the maintenance label)", "name", rec.Name)
	newID, err := u.finaliseContainer(ctx, current.ID, rec.Name)
	if err != nil {
		u.log.Error("could not finalise the recovered container", "name", rec.Name, "error", err)
		return
	}
	u.afterProviderChange(ctx, rec.Name, newID, current.ID)
}

// What recovery did with an interrupted swap (recordRecovery).
const (
	actionFinished = "finished" // committed: the replaced container was removed
	actionRestored = "restored" // the old container was put back
	actionRebuilt  = "rebuilt"  // the old container was rebuilt from its snapshot
	actionKept     = "kept"     // the old container was gone: its replacement was kept
	actionDropped  = "dropped"  // both were gone and another container took the name
)

// recordRecovery writes the history row of an interrupted swap that
// recovery resolved, saying what it did.
func (u *Updater) recordRecovery(rec store.SwapRecord, action string, now time.Time) {
	what := map[string]string{
		swapUpdate:   "an update",
		swapFinalise: "the last step of an update",
		swapRollback: "a rollback",
		swapConsumer: "the recreate of a network dependent",
	}[rec.Kind]
	if what == "" {
		what = "a container swap"
	}
	outcome, done := "rollback", ""
	switch {
	case action == actionFinished:
		outcome, done = "success", "it had finished, so recovery removed the replaced container"
	case action == actionKept:
		outcome, done = "success", "the old container was gone, so recovery kept its replacement"
	case action == actionRestored && rec.Kind == swapFinalise:
		// The old container of a finalise is the validated update.
		outcome, done = "success", "recovery kept the updated container and removes its maintenance label"
	case action == actionRestored:
		done = "recovery put the old container back"
	case action == actionRebuilt:
		done = "recovery rebuilt the old container from its snapshot"
	case action == actionDropped:
		outcome, done = "failed", "both containers were gone and another container now has the name"
	}
	cause := "Sentinel was interrupted during " + what
	if rec.Unresolved {
		cause = "Sentinel could not finish " + what + " at the time"
	}
	entry := store.UpdateRecord{
		Timestamp:     now,
		ContainerName: rec.Name,
		Outcome:       outcome,
		Error:         cause + "; " + done,
	}
	var old container.InspectResponse
	if rec.Snapshot != nil && json.Unmarshal(rec.Snapshot, &old) == nil && old.Config != nil {
		entry.OldImage, entry.OldDigest = old.Config.Image, old.Image
	}
	if err := u.store.RecordUpdate(entry); err != nil {
		u.log.Warn("could not record the recovered swap in the history", "name", rec.Name, "error", err)
	}
}

// stopWatchMargin is added to a stop timeout for the daemon to kill the
// container once the timeout is over, and report it.
const stopWatchMargin = 10 * time.Second

// defaultStopTimeout is assumed for a record that does not give one.
const defaultStopTimeout = 30

// watchInterval is how often a watched container is checked.
const watchInterval = 2 * time.Second

// stopWatchWindow is how long a restored container is watched: the stop
// timeout Sentinel gave the daemon (in seconds), plus a margin.
func stopWatchWindow(timeout int) time.Duration {
	if timeout <= 0 {
		timeout = defaultStopTimeout
	}
	return time.Duration(timeout)*time.Second + stopWatchMargin
}

// startedAgain logs and records in the history that a container recovery
// had put back was stopped by the daemon and has been started again.
func (u *Updater) startedAgain(ctx context.Context, rec store.SwapRecord, now time.Time) {
	u.log.Warn("the daemon stopped the restored container after recovery (a stop Sentinel had asked for before it went down); started it again", "name", rec.Name)
	entry := store.UpdateRecord{
		Timestamp:     now,
		ContainerName: rec.Name,
		Outcome:       "rollback",
		Error: "the update was interrupted and the old container put back, but the daemon then finished " +
			"stopping it (Sentinel had asked it to before it went down); Sentinel started it again",
	}
	if c, err := u.docker.InspectContainer(ctx, rec.Name); err == nil && c.Config != nil {
		entry.OldImage = c.Config.Image
		entry.OldDigest = c.Image
	}
	if err := u.store.RecordUpdate(entry); err != nil {
		u.log.Warn("could not record the restart in the history", "name", rec.Name, "error", err)
	}
	u.publishEvent(events.EventContainerUpdate, rec.Name, "started again after an interrupted update")
}

// watchRestored starts the watch of the restored containers whose swap
// records say to (WatchUntil), unless it is running. It ends when no record
// is watched any more, or when ctx (or the shutdown context, once set) ends.
// Every watchInterval it starts a watched container again if it has stopped,
// and resolves each record whose watch is over; a record whose resolution
// fails leaves the watch for the scans to retry.
func (u *Updater) watchRestored(ctx context.Context) {
	if u.shutdownCtx != nil {
		ctx = u.shutdownCtx
	}
	if !u.watching.CompareAndSwap(false, true) {
		return
	}
	u.watchers.Add(1)
	go func() {
		defer u.watchers.Done()
		u.runWatch(ctx)
	}()
}

func (u *Updater) runWatch(ctx context.Context) {
	now := u.clock.Now()
	for {
		recs := u.watchedSwaps()
		if len(recs) == 0 {
			u.watching.Store(false)
			// A record may have been added since: carry on, unless another
			// watch has started for it.
			if len(u.watchedSwaps()) == 0 || !u.watching.CompareAndSwap(false, true) {
				return
			}
			continue
		}
		// The next check is due at the next interval or the first watch end,
		// with a floor so a record that is busy is not polled in a loop.
		wait := watchInterval
		for _, rec := range recs {
			wait = min(wait, rec.WatchUntil.Sub(now))
		}
		wait = max(wait, watchInterval/8)
		select {
		case <-ctx.Done():
			u.watching.Store(false)
			return
		case now = <-u.clock.After(wait):
		}
		for _, rec := range recs {
			u.checkWatched(ctx, rec.Name, now)
		}
	}
}

// watchedSwaps returns the swap records whose container is being watched.
func (u *Updater) watchedSwaps() []store.SwapRecord {
	recs, err := u.store.ListSwaps()
	if err != nil {
		u.log.Warn("could not read the swap journal", "error", err)
		return nil
	}
	var watched []store.SwapRecord
	for _, rec := range recs {
		if !rec.WatchUntil.IsZero() {
			watched = append(watched, rec)
		}
	}
	return watched
}

// checkWatched checks one watched container, as of now. A busy container
// (an update or a scan's recovery holds it) is checked at the next tick.
func (u *Updater) checkWatched(ctx context.Context, name string, now time.Time) {
	if !u.tryLock(name) {
		return
	}
	defer u.unlock(name)
	rec, found, err := u.store.GetSwap(name)
	if err != nil || !found || rec.WatchUntil.IsZero() {
		return // resolved meanwhile
	}
	// Once begun, a check runs to its end, like a swap.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), swapBudget(rec.StopTimeout))
	defer cancel()
	if now.Before(rec.WatchUntil) {
		c, err := u.docker.InspectContainer(ctx, rec.OldID)
		if err == nil && c.State != nil && c.State.Running {
			return
		}
	}
	u.resolvePendingSwap(ctx, rec, now)
}

// DiscardSwap removes the journal record of a swap that cannot be resolved,
// once the user has fixed the containers by hand. The record is logged
// first. It refuses while an update of the container is running.
func (u *Updater) DiscardSwap(name string) error {
	if !u.tryLock(name) {
		return ErrUpdateInProgress
	}
	defer u.unlock(name)
	rec, found, readErr := u.store.GetSwap(name)
	if !found {
		return ErrNoSwap
	}
	rec.Snapshot = nil // not useful in the log
	u.log.Warn("discarding a container swap record on request", "name", name, "record", fmt.Sprintf("%+v", rec), "read_error", readErr)
	return u.store.DeleteSwap(name)
}

// recoverSwap resolves one journalled swap. changed reports that the
// container running under the name was replaced or (re)started, so the
// containers depending on it need repairing. oldKept reports that the old
// container was kept and put back (the second case below).
//
//   - Committed and the replacement exists: the old container is removed.
//   - Otherwise, while the old container exists: the replacement (known by
//     its journalled ID, or by the swap token label when Sentinel stopped
//     before the ID was journalled) is removed, and the old container gets
//     its name and restart policy back and is started if it was running.
//   - The old container is gone (a --rm container): an update that was not
//     committed is rebuilt from its snapshot; for other swaps (or without a
//     snapshot) the replacement is kept and started.
//   - Both are gone: the old container is rebuilt from its snapshot, unless
//     some other container has taken the name meanwhile.
func (u *Updater) recoverSwap(ctx context.Context, rec store.SwapRecord) (changed, oldKept bool, action string, err error) {
	old, err := u.docker.InspectContainer(ctx, rec.OldID)
	oldGone := docker.IsNotFound(err)
	if err != nil && !oldGone {
		return false, false, "", fmt.Errorf("inspect the old container: %w", err)
	}
	replacement, newFound, err := u.findReplacement(ctx, rec)
	if err != nil {
		return false, false, "", err
	}

	switch {
	case !oldGone && rec.Committed && newFound:
		u.log.Warn("the swap had finished, removing the replaced container", "name", rec.Name)
		if rec.RemoveVolumes {
			err = u.docker.RemoveContainerWithVolumes(ctx, rec.OldID)
		} else {
			err = u.docker.RemoveContainer(ctx, rec.OldID)
		}
		if err != nil && !docker.IsNotFound(err) {
			return false, false, "", fmt.Errorf("remove the replaced container: %w", err)
		}
		return true, false, actionFinished, nil

	case !oldGone:
		if rec.WatchUntil.IsZero() {
			u.log.Warn("the swap had not finished, restoring the old container", "name", rec.Name)
		}
		if newFound {
			if err := u.removeReplacement(ctx, replacement); err != nil {
				return false, true, "", err
			}
		}
		changed, err = u.restoreOldContainer(ctx, rec, old)
		return changed, true, actionRestored, err

	case newFound && rec.Kind == swapUpdate && !rec.Committed && rec.Snapshot != nil:
		// The update was never validated, and its old container (a --rm
		// one) is gone: put the old version back from its snapshot.
		u.log.Warn("the old container is gone and the update was not validated, rebuilding it", "name", rec.Name)
		changed, err = u.replaceWithRebuild(ctx, rec, replacement)
		return changed, false, actionRebuilt, err

	case newFound:
		u.log.Warn("the old container is gone, keeping its replacement", "name", rec.Name)
		if rec.OldRunning && (replacement.State == nil || !replacement.State.Running) {
			if err := u.docker.StartContainer(ctx, replacement.ID); err != nil {
				if rec.Snapshot == nil {
					return false, false, "", fmt.Errorf("start the replacement: %w", err)
				}
				// It will not start: put the old container back instead.
				u.log.Warn("the replacement does not start, rebuilding the old container", "name", rec.Name, "error", err)
				changed, err = u.replaceWithRebuild(ctx, rec, replacement)
				return changed, false, actionRebuilt, err
			}
		}
		return true, false, actionKept, nil

	default:
		changed, err = u.rebuildFromJournal(ctx, rec)
		if err == nil && !changed {
			return false, false, actionDropped, nil
		}
		return changed, false, actionRebuilt, err
	}
}

// findReplacement looks up the swap's replacement: by its journalled ID, or,
// when Sentinel stopped before the ID was journalled, as the container
// holding the name that carries the swap's token label. Any other container
// holding the name is not the replacement and is never touched.
func (u *Updater) findReplacement(ctx context.Context, rec store.SwapRecord) (container.InspectResponse, bool, error) {
	id := rec.NewID
	if id == "" {
		if rec.Token == "" {
			return container.InspectResponse{}, false, nil
		}
		id = rec.Name
	}
	c, err := u.docker.InspectContainer(ctx, id)
	switch {
	case docker.IsNotFound(err):
		return container.InspectResponse{}, false, nil
	case err != nil:
		return container.InspectResponse{}, false, fmt.Errorf("inspect the replacement: %w", err)
	}
	if rec.NewID == "" && (c.ID == rec.OldID || c.Config == nil || c.Config.Labels[swapLabel] != rec.Token) {
		return container.InspectResponse{}, false, nil
	}
	return c, true, nil
}

// removeReplacement stops and removes a swap's replacement.
func (u *Updater) removeReplacement(ctx context.Context, c container.InspectResponse) error {
	if c.State != nil && c.State.Running {
		if err := u.docker.StopContainer(ctx, c.ID, stopTimeout(c.Config, 10)); err != nil {
			u.log.Warn("stopping the unfinished replacement failed", "name", containerNameOf(c), "error", err)
		}
	}
	if err := u.docker.RemoveContainer(ctx, c.ID); err != nil && !docker.IsNotFound(err) {
		return fmt.Errorf("remove the unfinished replacement: %w", err)
	}
	return nil
}

// restoreOldContainer gives a swap's old container its name and restart
// policy back and starts it if it was running. If the name cannot be given
// back, the old container is still started under its set-aside name and
// the error returned, so the service runs while the swap stays pending.
func (u *Updater) restoreOldContainer(ctx context.Context, rec store.SwapRecord, old container.InspectResponse) (bool, error) {
	var renameErr error
	if containerNameOf(old) != rec.Name {
		if holder, err := u.docker.InspectContainer(ctx, rec.Name); err == nil && holder.ID != rec.OldID {
			renameErr = fmt.Errorf("%s is held by another container (%s)", rec.Name, truncateID(holder.ID))
		} else if err := u.docker.RenameContainer(ctx, rec.OldID, rec.Name); err != nil {
			renameErr = err
		}
	}
	var policyErr error
	if rec.RestartChanged {
		if updater, ok := u.docker.(docker.RestartPolicyUpdater); ok {
			policy := container.RestartPolicy{Name: container.RestartPolicyMode(rec.RestartPolicy), MaximumRetryCount: rec.RestartRetries}
			policyErr = updater.UpdateRestartPolicy(ctx, rec.OldID, policy)
		}
	}
	changed := false
	if rec.OldRunning && (old.State == nil || !old.State.Running) {
		if err := u.docker.StartContainer(ctx, rec.OldID); err != nil {
			return false, fmt.Errorf("start the restored container: %w", err)
		}
		changed = true
	}
	if renameErr != nil {
		return changed, fmt.Errorf("give the old container its name back: %w", renameErr)
	}
	if policyErr != nil {
		return changed, fmt.Errorf("restore the restart policy: %w", policyErr)
	}
	return changed, nil
}

// replaceWithRebuild removes a swap's replacement and rebuilds the old
// container from the journalled snapshot in its place. The rebuild is
// prepared first, so the replacement is only removed once the old image is
// known to be on the host.
func (u *Updater) replaceWithRebuild(ctx context.Context, rec store.SwapRecord, replacement container.InspectResponse) (bool, error) {
	r, err := prepareRebuild(ctx, u.docker, rec.Name, rec.Snapshot, rec.Token, u.log, replacement.ID)
	if err != nil {
		return false, fmt.Errorf("rebuild the old container: %w", err)
	}
	if err := u.removeReplacement(ctx, replacement); err != nil {
		r.undoTag()
		return false, err
	}
	if _, err := r.run(ctx, u.docker, rec.OldRunning); err != nil {
		return false, err
	}
	return true, nil
}

// rebuildFromJournal rebuilds a swap's old container from the journalled
// snapshot, started only if it was running. A container holding the name
// with this swap's token is an earlier, interrupted rebuild: it is kept and
// started. When some other container has taken the name meanwhile, nothing
// is rebuilt and the record is dropped.
func (u *Updater) rebuildFromJournal(ctx context.Context, rec store.SwapRecord) (bool, error) {
	if rec.Snapshot == nil {
		return false, errors.New("the old container is gone and no snapshot was journalled")
	}
	u.log.Warn("rebuilding the old container from its snapshot", "name", rec.Name)
	_, err := rebuildFromSnapshot(ctx, u.docker, rec.Name, rec.Snapshot, rec.OldRunning, rec.Token, u.log)
	var held *nameHeldError
	if !errors.As(err, &held) {
		return err == nil, err
	}
	if rec.Token == "" || held.token != rec.Token {
		u.log.Warn("another container now holds the name, dropping the swap record", "name", rec.Name, "holder", truncateID(held.id))
		return false, nil
	}
	if rec.OldRunning {
		rebuilt, err := u.docker.InspectContainer(ctx, held.id)
		if err != nil {
			return false, fmt.Errorf("inspect the rebuilt container: %w", err)
		}
		if rebuilt.State == nil || !rebuilt.State.Running {
			if err := u.docker.StartContainer(ctx, held.id); err != nil {
				return false, fmt.Errorf("start the rebuilt container: %w", err)
			}
		}
	}
	return true, nil
}

// containerNameOf returns an inspected container's name without the
// leading slash.
func containerNameOf(c container.InspectResponse) string {
	if len(c.Name) > 0 && c.Name[0] == '/' {
		return c.Name[1:]
	}
	return c.Name
}
