package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/deps"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/guardian"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
	"github.com/moby/moby/api/types/container"
)

// UpdateContainer performs the full update lifecycle for a single container:
// snapshot → pull → swap (rename old aside, create new, stop old, start new)
// → validate → remove old, or restore the old container on failure.
// Returns ErrUpdateInProgress if the container already has an update running.
//
// targetImage overrides the image to pull for semver version bumps (e.g.
// "dxflrs/garage:v2.2.0"). When empty, the current image tag is re-pulled
// (correct for :latest-style updates where the tag is mutable).
func (u *Updater) UpdateContainer(ctx context.Context, id, name, targetImage string) error {
	if strings.HasSuffix(name, replacedSuffix) {
		return fmt.Errorf("%s was set aside while Sentinel replaced it and is not updated itself", name)
	}
	if !u.tryLock(name) {
		return ErrUpdateInProgress
	}
	defer u.unlock(name)

	start := u.clock.Now()

	// 1. Inspect the current container. Its snapshot is stored only once
	// this version has been replaced (after validation), so a failed or
	// skipped attempt never displaces the snapshot a rollback restores.
	inspect, err := u.docker.InspectContainer(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}

	snapshotData, err := json.Marshal(inspect)
	if err != nil {
		return fmt.Errorf("marshal snapshot for %s: %w", name, err)
	}

	if inspect.Config == nil {
		return fmt.Errorf("inspect %s: container config is nil", name)
	}

	oldImage := inspect.Config.Image
	oldImageID := inspect.Image // full image ID for cleanup
	// Determine which image to pull: targetImage (semver bump) or oldImage (mutable tag re-pull).
	pullImage := oldImage
	if targetImage != "" {
		pullImage = targetImage
	}
	u.log.Info("starting update", "name", name, "image", oldImage)
	u.publishEvent(events.EventContainerUpdate, name, "update started")

	u.notifier.Notify(ctx, notify.Event{
		Type:          notify.EventUpdateStarted,
		ContainerName: name,
		OldImage:      oldImage,
		Timestamp:     u.clock.Now(),
	})

	// 2. Mark maintenance window.
	if err := u.store.SetMaintenance(name, true); err != nil {
		u.log.Warn("failed to set maintenance flag", "name", name, "error", err)
	}

	// 2.5. Run pre-update hooks (store entries, else the old container's labels).
	// Exit 75 skips the update quietly; any other failure aborts it before the
	// pull and is recorded and notified as a failed update.
	if u.hooks != nil && u.cfg.HooksEnabled() {
		if err := u.hooks.RunPreUpdate(ctx, id, name, inspect.Config.Labels); err != nil {
			if errors.Is(err, hooks.ErrSkipUpdate) {
				u.log.Info("pre-update hook requested skip", "name", name)
				_ = u.store.SetMaintenance(name, false)
				return nil
			}
			u.log.Warn("pre-update hook failed, aborting update", "name", name, "error", err)
			if mErr := u.store.SetMaintenance(name, false); mErr != nil {
				u.log.Warn("failed to clear maintenance flag after pre-update hook failure", "name", name, "error", mErr)
			}
			// A cancelled update (e.g. shutdown) is not a hook failure: return
			// without recording or notifying, as the grace-period wait does.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if recErr := u.store.RecordUpdate(store.UpdateRecord{
				Timestamp:     u.clock.Now(),
				ContainerName: name,
				OldImage:      oldImage,
				OldDigest:     extractDigestForRecord(inspect),
				NewImage:      pullImage,
				Outcome:       "failed",
				Duration:      u.clock.Since(start),
				Error:         err.Error(),
			}); recErr != nil {
				u.log.Warn("failed to persist pre-update hook failure record", "name", name, "error", recErr)
			}
			metrics.UpdatesTotal.WithLabelValues("failed").Inc()
			u.publishEvent(events.EventContainerUpdate, name, "update failed")
			u.notifier.Notify(ctx, notify.Event{
				Type:          notify.EventUpdateFailed,
				ContainerName: name,
				OldImage:      oldImage,
				Error:         err.Error(),
				Timestamp:     u.clock.Now(),
			})
			return fmt.Errorf("pre-update hook for %s: %w", name, err)
		}
	}

	// 2.8. Retag current image as backup before pulling.
	if u.isImageBackup() {
		backupTag := u.backupTag(oldImage)
		if tagErr := u.docker.TagImage(ctx, oldImage, backupTag); tagErr != nil {
			u.log.Warn("image backup tag failed", "name", name, "tag", backupTag, "error", tagErr)
		} else {
			u.log.Info("image backup tag created", "name", name, "tag", backupTag)
		}
	}

	// 3. Pull the new image.
	u.log.Info("pulling image", "name", name, "image", pullImage)
	if err := u.docker.PullImage(ctx, pullImage); err != nil {
		if mErr := u.store.SetMaintenance(name, false); mErr != nil {
			u.log.Warn("failed to clear maintenance flag after pull failure", "name", name, "error", mErr)
		}
		return fmt.Errorf("pull image for %s: %w", name, err)
	}

	// Get new image digest for the record.
	newDigest, err := u.docker.ImageDigest(ctx, pullImage)
	if err != nil {
		u.log.Debug("could not resolve new image digest", "image", pullImage, "error", err)
	}

	// 3.5 Image ID guard: if the pull resolved to the same image, skip the update.
	newImageID, idErr := u.docker.ImageID(ctx, pullImage)
	if idErr != nil {
		u.log.Debug("could not resolve new image ID", "image", pullImage, "error", idErr)
	} else if oldImageID != "" && newImageID == oldImageID {
		u.log.Info("pull resolved to same image, skipping update", "name", name, "imageID", oldImageID)
		_ = u.store.SetMaintenance(name, false)
		duration := u.clock.Since(start)
		_ = u.store.RecordUpdate(store.UpdateRecord{
			Timestamp:     u.clock.Now(),
			ContainerName: name,
			OldImage:      oldImage,
			OldDigest:     oldImageID,
			NewImage:      pullImage,
			NewDigest:     newImageID,
			Outcome:       "identical",
			Duration:      duration,
		})
		// Cache the digest pair that the next scan will actually compare:
		// post-pull repo digest (ImageDigest) vs registry manifest digest
		// (DistributionDigest). For multi-arch images or registries that
		// rebuild manifests without changing content, these differ even
		// after pulling — causing repeated false "update available" hits.
		// The old code cached ImageDigest(oldImage) vs ImageDigest(pullImage)
		// which were identical for mutable tags, making the cache useless.
		if newDigest != "" {
			distDigest, _ := u.docker.DistributionDigest(ctx, pullImage)
			if distDigest != "" {
				_ = u.store.CacheDigestEquivalence(newDigest, distDigest)
			}
		}
		u.queue.Remove(name)
		u.publishEvent(events.EventContainerUpdate, name, "no change detected")
		return nil
	}

	// 3.6 Signature verification gate.
	if u.imgVerifier != nil && u.verifyMode != verify.ModeDisabled {
		effectiveMode := verify.ResolveMode(
			inspect.Config.Labels[verify.ContainerLabel], "", u.verifyMode,
		)
		if effectiveMode != verify.ModeDisabled {
			result := u.imgVerifier.Verify(ctx, pullImage)
			if !result.Verified {
				if effectiveMode == verify.ModeEnforce {
					u.log.Warn("signature verification failed, blocking update",
						"name", name, "image", pullImage, "error", result.Error)
					_ = u.store.SetMaintenance(name, false)
					return fmt.Errorf("signature verification failed for %s: %s", name, result.Error)
				}
				// Warn mode: log and proceed.
				u.log.Warn("signature verification failed, proceeding (warn mode)",
					"name", name, "image", pullImage, "error", result.Error)
			} else {
				u.log.Info("signature verified", "name", name, "image", pullImage)
			}
		}
	}

	// 3.7 Pre-update vulnerability scan gate.
	if u.imgScanner != nil && u.scanMode == scanner.ScanPreUpdate {
		scanResult, scanErr := u.imgScanner.Scan(ctx, pullImage)
		if scanErr != nil {
			// Scan tool failure should not block updates.
			u.log.Warn("pre-update scan failed, proceeding",
				"name", name, "image", pullImage, "error", scanErr)
		} else if scanResult.ExceedsThreshold(u.severityThresh) {
			u.log.Warn("pre-update scan found vulnerabilities above threshold, blocking update",
				"name", name, "image", pullImage,
				"critical", scanResult.Summary.Critical,
				"high", scanResult.Summary.High,
				"threshold", string(u.severityThresh))
			_ = u.store.SetMaintenance(name, false)
			return fmt.Errorf("pre-update scan blocked %s: %d critical, %d high vulnerabilities",
				name, scanResult.Summary.Critical, scanResult.Summary.High)
		} else {
			u.log.Info("pre-update scan passed",
				"name", name, "image", pullImage, "total_vulns", scanResult.Summary.Total)
		}
	}

	// 4. Build the replacement. It mounts the old container's anonymous
	// volumes and leaves out the old image's defaults.
	rc := docker.RecreateConfig(inspect, docker.RecreateOptions{
		Image:    targetImage,
		OldImage: u.imageDefaults(ctx, oldImageID, name),
	})
	newConfig := rc.Config
	u.writeBackHookLabels(name, newConfig)
	addMaintenanceLabel(newConfig)
	// remove_volumes must not delete the volumes the new container took over.
	removeVolumes := docker.ContainerRemoveVolumes(inspect.Config.Labels) || u.isRemoveVolumes()
	if removeVolumes && len(rc.CarriedVolumes) > 0 {
		u.log.Info("keeping the anonymous volumes the new container takes over", "name", name, "volumes", len(rc.CarriedVolumes))
		removeVolumes = false
	}

	// 5. Swap the new container in: rename the old one aside, create the new
	// one, stop the old one, start the new one. The old container is removed
	// only after the new one passes validation, so a failure can bring it back.
	u.log.Info("replacing container", "name", name, "image", pullImage)
	sw := newSwap(u.docker, u.log, name, id, inspect, 30)
	if err := sw.replace(ctx, rc, true, nil); err != nil {
		if !sw.changed() {
			u.log.Error("could not set the old container aside, nothing was changed", "name", name, "error", err)
			u.reportFailedUpdate(ctx, name, oldImage, pullImage, extractDigestForRecord(inspect), start, err)
			return fmt.Errorf("replace %s: %w", name, err)
		}
		u.log.Error("replacement failed, restoring the old container", "name", name, "error", err)
		u.restoreAfterFailedUpdate(ctx, sw, snapshotData, start, err)
		return fmt.Errorf("replace %s: %w", name, err)
	}
	newID := sw.newID

	// 6. Wait grace period and validate.
	gracePeriod := u.cfg.GracePeriod()
	if override := docker.ContainerGracePeriod(inspect.Config.Labels); override > 0 {
		gracePeriod = override
	}
	u.log.Info("waiting grace period", "name", name, "duration", gracePeriod)
	select {
	case <-u.clock.After(gracePeriod):
	case <-ctx.Done():
		u.abandonUpdate(ctx, sw, snapshotData)
		return ctx.Err()
	}

	healthy, err := u.validateContainer(ctx, newID)
	if ctx.Err() != nil {
		u.abandonUpdate(ctx, sw, snapshotData)
		return ctx.Err()
	}
	if err != nil || !healthy {
		u.log.Error("validation failed, rolling back", "name", name, "error", err)
		u.publishEvent(events.EventContainerUpdate, name, "update failed")
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventUpdateFailed,
			ContainerName: name,
			OldImage:      oldImage,
			Error:         fmt.Sprintf("validation failed: %v", err),
			Timestamp:     u.clock.Now(),
		})
		metrics.UpdatesTotal.WithLabelValues("failed").Inc()
		cause := errors.New("new container failed validation")
		if err != nil {
			cause = fmt.Errorf("validation failed: %w", err)
		}
		u.restoreAfterFailedUpdate(ctx, sw, snapshotData, start, cause)
		return fmt.Errorf("new container %s failed validation", name)
	}

	// The new container is good: remove the old one and keep its snapshot
	// for a later rollback.
	sw.commit(ctx, removeVolumes)
	if err := u.store.SaveSnapshot(name, snapshotData); err != nil {
		u.log.Warn("failed to save the rollback snapshot", "name", name, "error", err)
	}

	// 6.5. Run post-update hooks (store entries, else the new container's labels).
	if u.hooks != nil && u.cfg.HooksEnabled() {
		if err := u.hooks.RunPostUpdate(ctx, newID, name, newConfig.Labels); err != nil {
			u.log.Warn("post-update hook failed", "name", name, "error", err)
		}
	}

	// 7. Remove maintenance label for Guardian compatibility.
	finaliseNewID, finaliseErr := u.finaliseContainer(ctx, newID, name)
	if finaliseErr != nil {
		var fErr *finaliseError
		if errors.As(finaliseErr, &fErr) && fErr.down {
			// The validated container could not be put back after the
			// finalise swap failed, so the service may be missing: recreate
			// the old version from the snapshot.
			u.log.Error("finalise failed and the updated container could not be restored, rolling back",
				"name", name, "stage", fErr.stage, "error", fErr.err)
			u.reportRollback(ctx, name, rollback(ctx, u.docker, name, snapshotData, u.log), start, finaliseErr)

			// Record as failed — never as success.
			duration := u.clock.Since(start)
			if recErr := u.store.RecordUpdate(store.UpdateRecord{
				Timestamp:     u.clock.Now(),
				ContainerName: name,
				OldImage:      oldImage,
				OldDigest:     extractDigestForRecord(inspect),
				NewImage:      pullImage,
				NewDigest:     newDigest,
				Outcome:       "failed",
				Duration:      duration,
				Error:         finaliseErr.Error(),
			}); recErr != nil {
				u.log.Warn("failed to persist finalise failure record", "name", name, "error", recErr)
			}

			u.publishEvent(events.EventContainerUpdate, name, "finalise failed — rollback attempted")
			u.notifier.Notify(ctx, notify.Event{
				Type:          notify.EventUpdateFailed,
				ContainerName: name,
				OldImage:      oldImage,
				Error:         finaliseErr.Error(),
				Timestamp:     u.clock.Now(),
			})
			return finaliseErr
		}

		// The validated container is still in place, running the new image
		// with the maintenance label. Don't roll back, but don't record
		// success either.
		u.log.Warn("finalise failed, container is running with the maintenance label",
			"name", name, "error", finaliseErr)

		if mErr := u.store.SetMaintenance(name, false); mErr != nil {
			u.log.Warn("failed to clear maintenance flag", "name", name, "error", mErr)
		}
		u.queue.Remove(name)

		duration := u.clock.Since(start)
		if recErr := u.store.RecordUpdate(store.UpdateRecord{
			Timestamp:     u.clock.Now(),
			ContainerName: name,
			OldImage:      oldImage,
			OldDigest:     extractDigestForRecord(inspect),
			NewImage:      pullImage,
			NewDigest:     newDigest,
			Outcome:       "partial",
			Duration:      duration,
			Error:         finaliseErr.Error(),
		}); recErr != nil {
			u.log.Warn("failed to persist finalise warning record", "name", name, "error", recErr)
		}

		u.publishEvent(events.EventContainerUpdate, name, "update complete with finalise warning")
		return finaliseErr
	}

	// 8. Success — clear maintenance and record.
	_ = finaliseNewID // new ID tracked internally; not needed further
	if err := u.store.SetMaintenance(name, false); err != nil {
		u.log.Warn("failed to clear maintenance flag", "name", name, "error", err)
	}
	u.queue.Remove(name)

	duration := u.clock.Since(start)
	if err := u.store.RecordUpdate(store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		OldImage:      oldImage,
		OldDigest:     extractDigestForRecord(inspect),
		NewImage:      pullImage,
		NewDigest:     newDigest,
		Outcome:       "success",
		Duration:      duration,
	}); err != nil {
		u.log.Warn("failed to persist update record", "name", name, "error", err)
	}

	metrics.UpdatesTotal.WithLabelValues("success").Inc()
	metrics.UpdateDuration.Observe(duration.Seconds())

	// Clear stale digest equivalence entries for this image now that a real update succeeded.
	_ = u.store.ClearDigestEquivalence(pullImage)

	u.notifier.Notify(ctx, notify.Event{
		Type:          notify.EventUpdateSucceeded,
		ContainerName: name,
		OldImage:      oldImage,
		NewImage:      pullImage,
		NewDigest:     newDigest,
		Timestamp:     u.clock.Now(),
	})

	// Clear notification state so re-detection gets a fresh notification.
	if err := u.store.ClearNotifyState(name); err != nil {
		u.log.Warn("failed to clear notify state after update", "name", name, "error", err)
	}

	// Post-update vulnerability scan (informational, async).
	if u.imgScanner != nil && u.scanMode == scanner.ScanPostUpdate {
		go func() {
			scanResult, scanErr := u.imgScanner.Scan(context.Background(), pullImage)
			if scanErr != nil {
				u.log.Warn("post-update scan failed", "name", name, "image", pullImage, "error", scanErr)
				return
			}
			u.log.Info("post-update scan complete",
				"name", name, "image", pullImage,
				"total", scanResult.Summary.Total,
				"critical", scanResult.Summary.Critical,
				"high", scanResult.Summary.High)
		}()
	}

	// Compose file sync (opt-in).
	if u.isComposeSync() {
		labels := inspect.Config.Labels
		workDir := labels["com.docker.compose.project.working_dir"]
		configFiles := labels["com.docker.compose.project.config_files"]
		svcName := labels["com.docker.compose.service"]
		if workDir != "" && configFiles != "" && svcName != "" {
			composePath := configFiles
			if i := strings.Index(composePath, ","); i >= 0 {
				composePath = composePath[:i]
			}
			if !strings.HasPrefix(composePath, "/") {
				composePath = workDir + "/" + composePath
			}
			if err := UpdateComposeTag(composePath, svcName, pullImage); err != nil {
				u.log.Warn("compose sync failed", "name", name, "file", composePath, "error", err)
			} else {
				u.log.Info("compose file updated", "name", name, "file", composePath)
			}
		}
	}

	// Clear ignored versions — container moved past them.
	if err := u.store.ClearIgnoredVersions(name); err != nil {
		u.log.Warn("failed to clear ignored versions after update", "name", name, "error", err)
	}

	// 9. Clean old snapshots — keep only the most recent one.
	if err := u.store.DeleteOldSnapshots(name, 1); err != nil {
		u.log.Warn("failed to clean old snapshots", "name", name, "error", err)
	}

	// 10. Handle shared network namespaces. Consumers reference the provider
	// by the ID it had before the update (the original or the step-5 one).
	repaired := u.repairNetworkNamespace(ctx, finaliseNewID, name, id, newID)

	// 11. Clean up old image if enabled.
	u.cleanupOldImage(ctx, oldImageID, name)

	// 12. Restart dependents (dependency-aware).
	if u.cfg.DependencyAware() {
		u.restartDependents(ctx, name, repaired)
	}

	u.log.Info("update complete", "name", name, "duration", duration)
	u.publishEvent(events.EventContainerUpdate, name, "update succeeded")
	return nil
}

// validateContainer checks that a container is running, not restarting, and
// healthy (if a healthcheck is defined).
func (u *Updater) validateContainer(ctx context.Context, id string) (bool, error) {
	inspect, err := u.docker.InspectContainer(ctx, id)
	if err != nil {
		return false, err
	}
	state := inspect.State
	if state == nil {
		return false, fmt.Errorf("container state is nil")
	}
	if !state.Running || state.Restarting {
		return false, nil
	}
	if state.Health == nil || state.Health.Status == "" {
		return true, nil
	}
	switch state.Health.Status {
	case "healthy":
		return true, nil
	case "unhealthy":
		return false, nil
	case "starting":
		return u.waitForHealthy(ctx, id)
	default:
		return true, nil
	}
}

const healthPollInterval = 2 * time.Second
const healthPollTimeout = 60 * time.Second

// waitForHealthy polls the container's healthcheck status until it resolves
// to healthy/unhealthy or the timeout expires.
func (u *Updater) waitForHealthy(ctx context.Context, id string) (bool, error) {
	deadline := u.clock.Now().Add(healthPollTimeout)
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-u.clock.After(healthPollInterval):
			if u.clock.Now().After(deadline) {
				u.log.Warn("healthcheck timeout waiting for healthy", "id", id)
				return false, nil
			}
			inspect, err := u.docker.InspectContainer(ctx, id)
			if err != nil {
				return false, err
			}
			if inspect.State == nil || inspect.State.Health == nil {
				return true, nil
			}
			switch inspect.State.Health.Status {
			case "healthy":
				return true, nil
			case "unhealthy":
				return false, nil
			}
		}
	}
}

// restoreAfterFailedUpdate brings the old container back after a failed
// update swap. When that is impossible (the daemon deletes a --rm container
// on stop, or the rename back fails) it recreates the container from the
// snapshot instead. Either way the outcome is recorded as a rollback.
func (u *Updater) restoreAfterFailedUpdate(ctx context.Context, sw *swap, snapshotData []byte, start time.Time, cause error) {
	u.reportRollback(ctx, sw.name, u.restoreOld(ctx, sw, snapshotData), start, cause)
}

// abandonUpdate puts the old container back when an update is cancelled
// part way, for example by a shutdown during the grace period. Like a
// cancelled pre-update hook it records and notifies nothing.
func (u *Updater) abandonUpdate(ctx context.Context, sw *swap, snapshotData []byte) {
	u.log.Warn("update interrupted, restoring the old container", "name", sw.name)
	if err := u.restoreOld(ctx, sw, snapshotData); err != nil {
		u.log.Error("could not restore the old container after an interrupted update", "name", sw.name, "error", err)
	}
	if err := u.store.SetMaintenance(sw.name, false); err != nil {
		u.log.Warn("failed to clear maintenance flag", "name", sw.name, "error", err)
	}
}

// restoreOld undoes an update swap. When the old container cannot be
// brought back it is recreated from the snapshot; the error is that of the
// last attempt.
func (u *Updater) restoreOld(ctx context.Context, sw *swap, snapshotData []byte) error {
	ctx = context.WithoutCancel(ctx)
	if err := sw.undo(ctx); err != nil {
		u.log.Warn("could not restore the old container, recreating it from the snapshot", "name", sw.name, "error", err)
		return rollback(ctx, u.docker, sw.name, snapshotData, u.log)
	}
	u.log.Info("old container restored", "name", sw.name)
	// A :latest pull moved the tag to the new image. Point it back at the
	// image the restored container runs, or the dashboard shows a bare
	// image ID and the next scan cannot resolve the container.
	var old container.InspectResponse
	if json.Unmarshal(snapshotData, &old) == nil && old.Config != nil && old.Image != "" {
		if _, err := retag(ctx, u.docker, old.Config.Image, old.Image, u.log); err != nil {
			u.log.Warn("could not point the tag back at the old image", "name", sw.name, "error", err)
		}
	}
	return nil
}

// reportFailedUpdate records and notifies an update that failed before it
// changed the container.
func (u *Updater) reportFailedUpdate(ctx context.Context, name, oldImage, newImage, oldDigest string, start time.Time, cause error) {
	ctx = context.WithoutCancel(ctx)
	if err := u.store.SetMaintenance(name, false); err != nil {
		u.log.Warn("failed to clear maintenance flag", "name", name, "error", err)
	}
	if err := u.store.RecordUpdate(store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		OldImage:      oldImage,
		OldDigest:     oldDigest,
		NewImage:      newImage,
		Outcome:       "failed",
		Duration:      u.clock.Since(start),
		Error:         cause.Error(),
	}); err != nil {
		u.log.Warn("failed to persist update failure record", "name", name, "error", err)
	}
	metrics.UpdatesTotal.WithLabelValues("failed").Inc()
	u.publishEvent(events.EventContainerUpdate, name, "update failed")
	u.notifier.Notify(ctx, notify.Event{
		Type:          notify.EventUpdateFailed,
		ContainerName: name,
		OldImage:      oldImage,
		Error:         cause.Error(),
		Timestamp:     u.clock.Now(),
	})
}

// reportRollback records and notifies the result of rolling a failed update
// back. rbErr is the rollback's own error; cause is why the update failed.
func (u *Updater) reportRollback(ctx context.Context, name string, rbErr error, start time.Time, cause error) {
	// Report even when the update was cancelled by a shutdown.
	ctx = context.WithoutCancel(ctx)
	reason := "update failed"
	if cause != nil {
		reason = cause.Error()
	}
	if rbErr != nil {
		u.log.Error("rollback also failed", "name", name, "error", rbErr)
		u.publishEvent(events.EventContainerUpdate, name, "rollback failed")
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventRollbackFailed,
			ContainerName: name,
			Error:         rbErr.Error(),
			Timestamp:     u.clock.Now(),
		})
		reason += "; rollback failed: " + rbErr.Error()
	} else {
		// Apply rollback policy setting — change the container's policy to prevent
		// the next scan from immediately retrying the same broken update.
		if rp := u.rollbackPolicy(); rp == "manual" || rp == "pinned" {
			if err := u.store.SetPolicyOverride(name, rp); err != nil {
				u.log.Warn("failed to set rollback policy override", "name", name, "policy", rp, "error", err)
			} else {
				u.log.Info("policy changed after rollback", "name", name, "policy", rp)
			}
		}
		u.publishEvent(events.EventContainerUpdate, name, "rollback succeeded")
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventRollbackOK,
			ContainerName: name,
			Timestamp:     u.clock.Now(),
		})
		metrics.UpdatesTotal.WithLabelValues("rollback").Inc()
	}
	if err := u.store.SetMaintenance(name, false); err != nil {
		u.log.Warn("failed to clear maintenance flag after rollback", "name", name, "error", err)
	}

	if err := u.store.RecordUpdate(store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		Outcome:       "rollback",
		Duration:      u.clock.Since(start),
		Error:         reason,
	}); err != nil {
		u.log.Warn("failed to persist rollback record", "name", name, "error", err)
	}
}

// backupTag builds the backup image reference for the current image before an update.
// Format: base-repo:sentinel-backup-20060102-150405
func (u *Updater) backupTag(imageRef string) string {
	ts := u.clock.Now().Format("20060102-150405")
	base := imageRef
	if i := strings.Index(base, "@"); i >= 0 {
		base = base[:i]
	}
	if i := strings.LastIndex(base, ":"); i >= 0 {
		if j := strings.LastIndex(base, "/"); j < i {
			base = base[:i]
		}
	}
	return base + ":sentinel-backup-" + ts
}

// containerName extracts the container name, stripping the leading /.
func containerName(c container.Summary) string {
	if len(c.Names) > 0 {
		name := c.Names[0]
		if len(name) > 0 && name[0] == '/' {
			return name[1:]
		}
		return name
	}
	return c.ID[:12]
}

// isSentinel returns true if the container appears to be Sentinel itself.
func isSentinel(labels map[string]string) bool {
	return labels["sentinel.self"] == "true"
}

// imageDefaults returns the config of the image a container was created
// from, so the recreate can drop the values that came from it. It returns
// nil, which keeps the old config whole, when the image cannot be read.
func (u *Updater) imageDefaults(ctx context.Context, imageID, name string) *container.Config {
	reader, ok := u.docker.(docker.ImageConfigReader)
	if !ok || imageID == "" {
		return nil
	}
	cfg, err := reader.ImageConfig(ctx, imageID)
	if err != nil {
		u.log.Warn("could not read the old image's defaults, keeping the old config as it is",
			"name", name, "image", imageID, "error", err)
		return nil
	}
	return cfg
}

// writeBackHookLabels copies the container's stored hooks onto the new
// config as sentinel.hook.* labels when SENTINEL_HOOKS_WRITE_LABELS is on.
// finaliseContainer clones this config, so the labels survive it.
func (u *Updater) writeBackHookLabels(name string, cfg *container.Config) {
	if u.hooks == nil || !u.cfg.HooksWriteLabels() {
		return
	}
	labels, err := u.hooks.WriteBackLabels(name, cfg.Labels)
	if err != nil {
		u.log.Warn("failed to write hook labels", "name", name, "error", err)
		return
	}
	cfg.Labels = labels
}

// addMaintenanceLabel sets sentinel.maintenance=true on the config.
func addMaintenanceLabel(cfg *container.Config) {
	if cfg.Labels == nil {
		cfg.Labels = make(map[string]string)
	}
	cfg.Labels[guardian.MaintenanceLabel] = "true"
}

// finaliseContainer replaces the running container with an identical one
// that has the sentinel.maintenance label removed. This ensures
// Docker-Guardian can see the container after a successful update.
//
// It recreates the container through a swap, so a failure puts the
// validated container back (or sets down on the error when it cannot).
// Returns the new container ID, or id when nothing changed.
func (u *Updater) finaliseContainer(ctx context.Context, id, name string) (string, error) {
	inspect, err := u.docker.InspectContainer(ctx, id)
	if err != nil {
		return id, &finaliseError{stage: "inspect", err: err}
	}

	if inspect.Config == nil {
		return id, &finaliseError{stage: "inspect", err: fmt.Errorf("container config is nil for %s", name)}
	}

	// If the maintenance label is not present, nothing to do.
	if !guardian.HasMaintenanceLabel(inspect.Config.Labels) {
		return id, nil
	}

	if inspect.HostConfig == nil {
		return id, &finaliseError{stage: "inspect", err: fmt.Errorf("container HostConfig is nil for %s", name)}
	}

	rc := docker.RecreateConfig(inspect, docker.RecreateOptions{})
	delete(rc.Config.Labels, guardian.MaintenanceLabel)

	u.log.Info("finalising container (removing maintenance label)", "name", name)

	sw := newSwap(u.docker, u.log, name, id, inspect, 10)
	if err := sw.replace(ctx, rc, true, nil); err != nil {
		fErr := &finaliseError{stage: "swap", err: err}
		var se *swapError
		if errors.As(err, &se) {
			fErr.stage, fErr.err = se.stage, se.err
		}
		if undoErr := sw.undo(ctx); undoErr != nil {
			fErr.err = fmt.Errorf("%w; putting the updated container back failed: %v", fErr.err, undoErr)
			fErr.down = true
		}
		return id, fErr
	}
	// The finalised container mounts the same volumes, so keep them.
	sw.commit(ctx, false)

	u.log.Info("finalised container", "name", name, "new_id", truncateID(sw.newID))
	return sw.newID, nil
}

// extractDigestForRecord gets the image digest from an inspect response.
func extractDigestForRecord(inspect container.InspectResponse) string {
	if inspect.Image != "" {
		return inspect.Image
	}
	return ""
}

// replaceTag replaces the tag portion of an image reference.
// e.g. replaceTag("dxflrs/garage:v2.1.0", "v2.2.0") → "dxflrs/garage:v2.2.0"
func replaceTag(imageRef, newTag string) string {
	if i := strings.LastIndex(imageRef, ":"); i >= 0 {
		// Ensure the colon separates repo from tag (not registry:port).
		candidate := imageRef[i+1:]
		if !strings.Contains(candidate, "/") {
			return imageRef[:i+1] + newTag
		}
	}
	return imageRef + ":" + newTag
}

// truncateID safely truncates a container ID to 12 characters for logging.
func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// repairNetworkNamespace handles shared-namespace containers after an update.
// It verifies the updated container's own namespace (if it's a consumer) and
// repairs the running containers that share this container's namespace (if
// it's a provider). id is the provider's new ID and oldIDs the IDs it had
// before the update. The daemon stores network_mode container:<name> as
// container:<ID>, so consumers normally reference an old ID; a restart
// cannot rejoin a removed container, so they are recreated on the new one.
// Consumers that reference the name are restarted. It returns the names of
// the consumers it repaired.
func (u *Updater) repairNetworkNamespace(ctx context.Context, id, name string, oldIDs ...string) map[string]bool {
	repaired := make(map[string]bool)
	inspect, err := u.docker.InspectContainer(ctx, id)
	if err != nil {
		u.log.Warn("namespace check: inspect failed", "name", name, "error", err)
		return repaired
	}

	// Case 1: this container uses another's namespace — verify it joined.
	if inspect.HostConfig != nil && inspect.HostConfig.NetworkMode.IsContainer() {
		if inspect.NetworkSettings == nil || inspect.NetworkSettings.SandboxKey == "" {
			u.log.Warn("shared namespace broken, restarting consumer",
				"name", name, "provider", inspect.HostConfig.NetworkMode.ConnectedContainer())
			if err := u.docker.RestartContainer(ctx, id); err != nil {
				u.log.Error("failed to restart namespace consumer", "name", name, "error", err)
			}
		}
	}

	// Case 2: other containers may share this container's namespace.
	containers, err := u.docker.ListContainers(ctx)
	if err != nil {
		u.log.Warn("namespace check: list failed", "error", err)
		return repaired
	}

	for _, c := range containers {
		cName := containerName(c)
		if cName == name || strings.HasSuffix(cName, replacedSuffix) {
			continue // skip self, and containers set aside by a swap
		}
		dep, err := u.docker.InspectContainer(ctx, c.ID)
		if err != nil {
			continue
		}
		if dep.HostConfig == nil || !dep.HostConfig.NetworkMode.IsContainer() {
			continue
		}
		ref := dep.HostConfig.NetworkMode.ConnectedContainer()
		switch {
		case ref == name || sameContainerID(ref, id):
			u.log.Info("restarting network dependent", "dependent", cName, "provider", name)
			if err := u.docker.RestartContainer(ctx, c.ID); err != nil {
				u.log.Warn("failed to restart dependent", "dependent", cName, "error", err)
			}
			repaired[cName] = true
		case slices.ContainsFunc(oldIDs, func(old string) bool { return sameContainerID(ref, old) }):
			u.recreateNamespaceConsumer(ctx, c.ID, cName, dep, id, name)
			repaired[cName] = true
		}
	}
	return repaired
}

// restartDependents restarts the running containers that depend on name
// (sentinel.depends-on or compose depends_on labels, or its network
// namespace), so they reconnect to the updated container. skip holds the
// namespace consumers repairNetworkNamespace already handled. The graph is
// built from the containers running now, so it is never stale.
func (u *Updater) restartDependents(ctx context.Context, name string, skip map[string]bool) {
	running, err := u.docker.ListContainers(ctx)
	if err != nil {
		u.log.Warn("deps: failed to list containers", "error", err)
		return
	}
	ids := make(map[string]string, len(running))
	infos := make([]deps.ContainerInfo, 0, len(running))
	for _, c := range running {
		n := containerName(c)
		ids[n] = c.ID
		infos = append(infos, deps.ContainerInfo{
			ID:          c.ID,
			Name:        n,
			Labels:      c.Labels,
			NetworkMode: c.HostConfig.NetworkMode,
		})
	}
	for _, dep := range deps.Build(infos).Dependents(name) {
		if skip[dep] || strings.HasSuffix(dep, replacedSuffix) {
			continue
		}
		u.log.Info("restarting dependent container", "dependent", dep, "provider", name)
		if err := u.docker.RestartContainer(ctx, ids[dep]); err != nil {
			u.log.Warn("failed to restart dependent", "dependent", dep, "error", err)
		}
	}
}

// recreateNamespaceConsumer recreates a container that shared the network
// namespace of a provider that has since been replaced, so that it joins
// the provider's new container. It goes through a swap, so a failure
// leaves the consumer as it was.
func (u *Updater) recreateNamespaceConsumer(ctx context.Context, consumerID, consumerName string, consumer container.InspectResponse, providerID, providerName string) {
	if !u.tryLock(consumerName) {
		u.log.Warn("network dependent is being updated, not recreating it", "dependent", consumerName, "provider", providerName)
		return
	}
	defer u.unlock(consumerName)

	rc := docker.RecreateConfig(consumer, docker.RecreateOptions{})
	rc.HostConfig.NetworkMode = container.NetworkMode("container:" + providerID)
	u.log.Info("recreating network dependent on the updated provider", "dependent", consumerName, "provider", providerName)

	sw := newSwap(u.docker, u.log, consumerName, consumerID, consumer, 10)
	if err := sw.replace(ctx, rc, sw.oldRunning, nil); err != nil {
		u.log.Error("failed to recreate network dependent", "dependent", consumerName, "error", err)
		if undoErr := sw.undo(ctx); undoErr != nil {
			u.log.Error("failed to restore network dependent", "dependent", consumerName, "error", undoErr)
		}
		return
	}
	sw.commit(ctx, false)
}

// sameContainerID reports whether two container IDs name the same
// container, allowing for one of them to be a short (12+ digit) prefix.
func sameContainerID(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if len(a) < 12 || len(b) < 12 {
		return false
	}
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// effectiveNotifyMode returns the notification mode for a container,
// falling back to the global default_notify_mode setting.
func (u *Updater) effectiveNotifyMode(name string) string {
	pref, err := u.store.GetNotifyPref(name)
	if err == nil && pref != nil && pref.Mode != "" && pref.Mode != "default" {
		return pref.Mode
	}
	if u.settings != nil {
		val, err := u.settings.LoadSetting("default_notify_mode")
		if err != nil {
			u.log.Debug("failed to load default_notify_mode", "error", err)
		}
		if val != "" {
			return val
		}
	}
	return "default"
}
