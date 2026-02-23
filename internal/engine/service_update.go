package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/swarm"
)

// scanServices checks pre-fetched Swarm services for image updates,
// routing them through the same policy/queue/notification flow as containers.
// The services list is fetched once in Scan() to avoid duplicate API calls.
func (u *Updater) scanServices(ctx context.Context, services []swarm.Service, mode ScanMode, result *ScanResult, filters []string, reserve int) {
	result.Services = len(services)

	for _, svc := range services {
		if ctx.Err() != nil {
			return
		}

		name := svc.Spec.Name
		labels := svc.Spec.Labels
		if labels == nil {
			labels = map[string]string{}
		}

		rawImageRef := svc.Spec.TaskTemplate.ContainerSpec.Image
		// Swarm auto-pins digests (nginx:1.27@sha256:abc...) — strip for registry checks.
		// The tag is what matters; the digest is Swarm's internal tracking.
		imageRef := rawImageRef
		var specDigest string
		if i := strings.Index(imageRef, "@sha256:"); i > 0 {
			specDigest = imageRef[i+1:] // "sha256:..."
			imageRef = imageRef[:i]
		}
		tag := registry.ExtractTag(imageRef)
		resolved := ResolvePolicy(u.store, labels, name, tag, u.cfg.DefaultPolicy(), u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned service", "name", name)
			continue
		}

		if isSentinel(labels) {
			u.log.Debug("skipping sentinel service", "name", name)
			continue
		}

		if MatchesFilter(name, filters) {
			u.log.Debug("skipping filtered service", "name", name)
			continue
		}

		// Rate limit check.
		if u.rateTracker != nil {
			host := registry.RegistryHost(imageRef)
			canProceed, wait := u.rateTracker.CanProceed(host, reserve)
			if !canProceed {
				if mode == ScanManual {
					u.log.Warn("rate limit exhausted, stopping manual scan (services)", "registry", host, "resets_in", wait)
					result.RateLimited++
					break
				}
				result.RateLimited++
				continue
			}
		}

		var check registry.CheckResult
		if specDigest != "" {
			check = u.checker.CheckVersionedWithDigest(ctx, imageRef, specDigest)
		} else {
			// Try local image inspect first; fall back to registry digest
			// for multi-node swarm where images only exist on worker nodes.
			localDigest, err := u.docker.ImageDigest(ctx, imageRef)
			if err != nil {
				remoteDigest, rdErr := u.docker.DistributionDigest(ctx, imageRef)
				if rdErr != nil {
					u.log.Debug("service image not resolvable locally or remotely",
						"name", name, "image", imageRef, "error", rdErr)
					continue
				}
				localDigest = remoteDigest
			}
			check = u.checker.CheckVersionedWithDigest(ctx, imageRef, localDigest)
		}
		if check.Error != nil {
			u.log.Warn("service registry check failed", "name", name, "image", imageRef, "error", check.Error)
			result.Errors = append(result.Errors, fmt.Errorf("service %s: %w", name, check.Error))
			continue
		}

		if check.IsLocal || !check.UpdateAvailable {
			if !check.UpdateAvailable {
				if _, queued := u.queue.Get(name); queued {
					u.queue.Remove(name)
				}
			}
			continue
		}

		// Filter ignored versions.
		if len(check.NewerVersions) > 0 {
			ignored, _ := u.store.GetIgnoredVersions(name)
			if len(ignored) > 0 {
				ignoredSet := make(map[string]bool, len(ignored))
				for _, v := range ignored {
					ignoredSet[v] = true
				}
				var filtered []string
				for _, v := range check.NewerVersions {
					if !ignoredSet[v] {
						filtered = append(filtered, v)
					}
				}
				if len(filtered) == 0 {
					continue
				}
				check.NewerVersions = filtered
			}
		}

		u.log.Info("service update available", "name", name, "image", imageRef,
			"local_digest", check.LocalDigest, "remote_digest", check.RemoteDigest)
		u.publishEvent(events.EventServiceUpdate, name, "service update available")

		// Notifications — same dedup logic as containers.
		shouldNotify := true
		notifyMode := u.effectiveNotifyMode(name)
		switch notifyMode {
		case "muted":
			shouldNotify = false
		case "digest_only":
			shouldNotify = false
		default:
			state, _ := u.store.GetNotifyState(name)
			if state != nil && state.LastDigest == check.RemoteDigest && !state.LastNotified.IsZero() {
				if !state.SnoozedUntil.IsZero() && u.clock.Now().Before(state.SnoozedUntil) {
					shouldNotify = false
					u.log.Debug("notification snoozed", "name", name, "until", state.SnoozedUntil)
				} else if state.SnoozedUntil.IsZero() {
					shouldNotify = false
				}
				// If snooze expired, shouldNotify stays true — re-notify.
			}
		}

		notifyOK := false
		if shouldNotify {
			notifyOK = u.notifier.Notify(ctx, notify.Event{
				Type:          notify.EventUpdateAvailable,
				ContainerName: name,
				OldImage:      imageRef,
				OldDigest:     check.LocalDigest,
				NewDigest:     check.RemoteDigest,
				Timestamp:     u.clock.Now(),
			})
		}

		now := u.clock.Now()
		existing, _ := u.store.GetNotifyState(name)
		firstSeen := now
		if existing != nil && existing.FirstSeen.After(time.Time{}) {
			firstSeen = existing.FirstSeen
		}
		lastNotified := time.Time{}
		if existing != nil {
			lastNotified = existing.LastNotified
		}
		if notifyOK {
			lastNotified = now
		}
		snoozeDur := docker.ContainerNotifySnooze(labels)
		var snoozedUntil time.Time
		if snoozeDur > 0 && notifyOK {
			snoozedUntil = now.Add(snoozeDur)
		}
		if err := u.store.SetNotifyState(name, &store.NotifyState{
			LastDigest:   check.RemoteDigest,
			LastNotified: lastNotified,
			FirstSeen:    firstSeen,
			SnoozedUntil: snoozedUntil,
		}); err != nil {
			u.log.Warn("failed to persist service notify state", "name", name, "error", err)
		}

		scanTarget := ""
		if len(check.NewerVersions) > 0 {
			scanTarget = replaceTag(imageRef, check.NewerVersions[0])
		}

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++
			if err := u.UpdateService(ctx, svc.ID, name, scanTarget); err != nil {
				u.log.Error("auto service update failed", "name", name, "error", err)
				result.Failed++
				result.Errors = append(result.Errors, err)
			} else {
				result.ServiceUpdates++
				result.Updated++
			}

		case docker.PolicyManual:
			u.queue.Add(PendingUpdate{
				ContainerID:            svc.ID,
				ContainerName:          name,
				CurrentImage:           imageRef,
				CurrentDigest:          check.LocalDigest,
				RemoteDigest:           check.RemoteDigest,
				DetectedAt:             u.clock.Now(),
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
				Type:                   "service",
			})
			u.log.Info("service update queued for approval", "name", name)
			u.publishEvent(events.EventQueueChange, name, "service queued for approval")
			result.Queued++
		}
	}
}

// UpdateService performs a rolling update on a Swarm service. It modifies the
// service spec to use the new image, then polls UpdateStatus until the rollout
// completes, pauses, or times out.
func (u *Updater) UpdateService(ctx context.Context, serviceID, name, targetImage string) error {
	if !u.tryLock(name) {
		return ErrUpdateInProgress
	}
	defer u.unlock(name)

	start := u.clock.Now()

	// Fresh inspect to get current version (optimistic locking).
	svc, err := u.docker.InspectService(ctx, serviceID)
	if err != nil {
		return fmt.Errorf("inspect service %s: %w", name, err)
	}

	oldImage := svc.Spec.TaskTemplate.ContainerSpec.Image

	// Build the new spec with the updated image.
	newSpec := svc.Spec
	if targetImage != "" {
		newSpec.TaskTemplate.ContainerSpec.Image = targetImage
	}

	u.notifier.Notify(ctx, notify.Event{
		Type:          notify.EventUpdateStarted,
		ContainerName: name,
		OldImage:      oldImage,
		NewImage:      targetImage,
		Timestamp:     u.clock.Now(),
	})

	u.publishEvent(events.EventServiceUpdate, name, "service update started")

	if err := u.docker.UpdateService(ctx, serviceID, svc.Meta.Version, newSpec, ""); err != nil {
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventUpdateFailed,
			ContainerName: name,
			OldImage:      oldImage,
			NewImage:      targetImage,
			Error:         err.Error(),
			Timestamp:     u.clock.Now(),
		})
		return fmt.Errorf("update service %s: %w", name, err)
	}

	// Poll for rollout completion.
	outcome, pollErr := u.pollServiceUpdate(ctx, serviceID, name)

	duration := u.clock.Since(start)

	record := store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		OldImage:      oldImage,
		NewImage:      targetImage,
		Outcome:       outcome,
		Duration:      duration,
		Type:          "service",
	}
	if pollErr != nil {
		record.Error = pollErr.Error()
	}
	if err := u.store.RecordUpdate(record); err != nil {
		u.log.Warn("failed to record service update history", "name", name, "error", err)
	}

	switch outcome {
	case "success":
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventUpdateSucceeded,
			ContainerName: name,
			OldImage:      oldImage,
			NewImage:      targetImage,
			Timestamp:     u.clock.Now(),
		})
		u.publishEvent(events.EventServiceUpdate, name, "service update succeeded")

		// Housekeeping.
		u.queue.Remove(name)
		if err := u.store.ClearNotifyState(name); err != nil {
			u.log.Warn("failed to clear service notify state after update", "name", name, "error", err)
		}
		if err := u.store.ClearIgnoredVersions(name); err != nil {
			u.log.Warn("failed to clear service ignored versions after update", "name", name, "error", err)
		}

	case "rollback":
		// Apply rollback policy setting — change the service's policy to prevent
		// the next scan from immediately retrying the same broken update.
		if rp := u.rollbackPolicy(); rp == "manual" || rp == "pinned" {
			if err := u.store.SetPolicyOverride(name, rp); err != nil {
				u.log.Warn("failed to set rollback policy override", "name", name, "policy", rp, "error", err)
			} else {
				u.log.Info("policy changed after rollback", "name", name, "policy", rp)
			}
		}
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventRollbackOK,
			ContainerName: name,
			OldImage:      oldImage,
			NewImage:      targetImage,
			Timestamp:     u.clock.Now(),
		})
		u.publishEvent(events.EventServiceUpdate, name, "service rolled back by Swarm")

	default: // "failed", "timeout"
		errMsg := outcome
		if pollErr != nil {
			errMsg = pollErr.Error()
		}
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventUpdateFailed,
			ContainerName: name,
			OldImage:      oldImage,
			NewImage:      targetImage,
			Error:         errMsg,
			Timestamp:     u.clock.Now(),
		})
	}

	return pollErr
}

const (
	serviceUpdateTimeout   = 10 * time.Minute
	serviceUpdatePollDelay = 5 * time.Second
)

// pollServiceUpdate polls the service's UpdateStatus until it reaches a
// terminal state or the timeout expires. Returns the outcome string and
// any error. Uses u.clock.After for testability with mock clocks.
func (u *Updater) pollServiceUpdate(ctx context.Context, serviceID, name string) (string, error) {
	deadline := u.clock.Now().Add(serviceUpdateTimeout)

	for {
		select {
		case <-ctx.Done():
			return "failed", ctx.Err()
		case <-u.clock.After(serviceUpdatePollDelay):
			if u.clock.Now().After(deadline) {
				return "timeout", fmt.Errorf("service %s update timed out after %s", name, serviceUpdateTimeout)
			}

			svc, err := u.docker.InspectService(ctx, serviceID)
			if err != nil {
				u.log.Warn("failed to poll service update status", "name", name, "error", err)
				continue
			}

			if svc.UpdateStatus == nil {
				continue
			}

			switch svc.UpdateStatus.State {
			case swarm.UpdateStateCompleted:
				return "success", nil
			case swarm.UpdateStatePaused:
				return "failed", fmt.Errorf("service %s update paused: %s", name, svc.UpdateStatus.Message)
			case swarm.UpdateStateRollbackCompleted:
				return "rollback", fmt.Errorf("service %s rolled back: %s", name, svc.UpdateStatus.Message)
			case swarm.UpdateStateRollbackStarted:
				u.log.Warn("service rolling back", "name", name, "message", svc.UpdateStatus.Message)
				// Keep polling until rollback completes.
			}
		}
	}
}
