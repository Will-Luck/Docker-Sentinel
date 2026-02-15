package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/guardian"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// UpdateContainer performs the full update lifecycle for a single container:
// snapshot → pull → stop → remove → create → start → validate → (rollback on failure).
// Returns ErrUpdateInProgress if the container already has an update running.
//
// targetImage overrides the image to pull for semver version bumps (e.g.
// "dxflrs/garage:v2.2.0"). When empty, the current image tag is re-pulled
// (correct for :latest-style updates where the tag is mutable).
func (u *Updater) UpdateContainer(ctx context.Context, id, name, targetImage string) error {
	if !u.tryLock(name) {
		return ErrUpdateInProgress
	}
	defer u.unlock(name)

	start := u.clock.Now()

	// 1. Inspect and snapshot the current container.
	inspect, err := u.docker.InspectContainer(ctx, id)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", name, err)
	}

	snapshotData, err := json.Marshal(inspect)
	if err != nil {
		return fmt.Errorf("marshal snapshot for %s: %w", name, err)
	}
	if err := u.store.SaveSnapshot(name, snapshotData); err != nil {
		return fmt.Errorf("save snapshot for %s: %w", name, err)
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
	u.log.Info("saved snapshot", "name", name, "image", oldImage)
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

	// 2.5. Run pre-update hooks.
	if u.hooks != nil && u.cfg.HooksEnabled() {
		if err := u.hooks.RunPreUpdate(ctx, id, name); err != nil {
			if errors.Is(err, hooks.ErrSkipUpdate) {
				u.log.Info("pre-update hook requested skip", "name", name)
				_ = u.store.SetMaintenance(name, false)
				return nil
			}
			u.log.Warn("pre-update hook failed", "name", name, "error", err)
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
	newDigest, _ := u.docker.ImageDigest(ctx, pullImage)

	// 4. Stop and remove the old container.
	u.log.Info("stopping old container", "name", name)
	if err := u.docker.StopContainer(ctx, id, 30); err != nil {
		u.log.Warn("stop failed, proceeding with force remove", "name", name, "error", err)
	}
	if err := u.docker.RemoveContainer(ctx, id); err != nil {
		if mErr := u.store.SetMaintenance(name, false); mErr != nil {
			u.log.Warn("failed to clear maintenance flag after remove failure", "name", name, "error", mErr)
		}
		return fmt.Errorf("remove old container %s: %w", name, err)
	}

	// 5. Create and start the new container.
	newConfig := cloneConfig(inspect.Config)
	if targetImage != "" {
		newConfig.Image = targetImage
	}
	addMaintenanceLabel(newConfig)

	hostConfig := inspect.HostConfig
	netConfig := rebuildNetworkingConfig(inspect.NetworkSettings)

	u.log.Info("creating new container", "name", name, "image", pullImage)
	newID, err := u.docker.CreateContainer(ctx, name, newConfig, hostConfig, netConfig)
	if err != nil {
		u.log.Error("create failed, rolling back", "name", name, "error", err)
		u.doRollback(ctx, name, snapshotData, start)
		return fmt.Errorf("create new container %s: %w", name, err)
	}

	if err := u.docker.StartContainer(ctx, newID); err != nil {
		u.log.Error("start failed, rolling back", "name", name, "error", err)
		// Clean up the failed new container, then rollback.
		_ = u.docker.RemoveContainer(ctx, newID)
		u.doRollback(ctx, name, snapshotData, start)
		return fmt.Errorf("start new container %s: %w", name, err)
	}

	// 6. Wait grace period and validate.
	gracePeriod := u.cfg.GracePeriod()
	u.log.Info("waiting grace period", "name", name, "duration", gracePeriod)
	select {
	case <-u.clock.After(gracePeriod):
	case <-ctx.Done():
		return ctx.Err()
	}

	healthy, err := u.validateContainer(ctx, newID)
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
		_ = u.docker.StopContainer(ctx, newID, 10)
		_ = u.docker.RemoveContainer(ctx, newID)
		u.doRollback(ctx, name, snapshotData, start)
		return fmt.Errorf("new container %s failed validation", name)
	}

	// 6.5. Run post-update hooks.
	if u.hooks != nil && u.cfg.HooksEnabled() {
		if err := u.hooks.RunPostUpdate(ctx, newID, name); err != nil {
			u.log.Warn("post-update hook failed", "name", name, "error", err)
		}
	}

	// 7. Remove maintenance label for Guardian compatibility.
	finaliseNewID, finaliseErr := u.finaliseContainer(ctx, newID, name)
	if finaliseErr != nil {
		var fErr *finaliseError
		if errors.As(finaliseErr, &fErr) && finaliseStageIsDestructive(fErr.stage) {
			// Container is likely DOWN — old was removed but new create/start failed.
			u.log.Error("finalise failed at destructive stage, container is likely down",
				"name", name, "stage", fErr.stage, "error", fErr.err)

			// On start failure the new container exists but isn't running — remove it
			// before rollback to avoid name conflict.
			if fErr.stage == "start" {
				if rmErr := u.docker.RemoveContainer(ctx, finaliseNewID); rmErr != nil {
					u.log.Warn("failed to remove broken finalise container before rollback",
						"name", name, "error", rmErr)
				}
			}

			u.doRollback(ctx, name, snapshotData, start)

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

		// Non-destructive stage (inspect or stop) — container is likely still
		// running with the old image. Don't rollback but don't record success.
		u.log.Warn("finalise failed at non-destructive stage, container may still be running with maintenance label",
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
			Outcome:       "finalise_warning",
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

	u.notifier.Notify(ctx, notify.Event{
		Type:          notify.EventUpdateSucceeded,
		ContainerName: name,
		OldImage:      oldImage,
		NewImage:      pullImage,
		NewDigest:     newDigest,
		Timestamp:     u.clock.Now(),
	})

	// Clear notification state so re-detection gets a fresh notification.
	_ = u.store.ClearNotifyState(name)

	// Clear ignored versions — container moved past them.
	_ = u.store.ClearIgnoredVersions(name)

	// 9. Clean old snapshots — keep only the most recent one.
	if err := u.store.DeleteOldSnapshots(name, 1); err != nil {
		u.log.Warn("failed to clean old snapshots", "name", name, "error", err)
	}

	// 10. Handle shared network namespaces.
	u.repairNetworkNamespace(ctx, finaliseNewID, name)

	// 11. Clean up old image if enabled.
	u.cleanupOldImage(ctx, oldImageID, name)

	// 12. Restart dependents (dependency-aware).
	if u.deps != nil && u.cfg.DependencyAware() {
		for _, dep := range u.deps.Dependents(name) {
			depContainers, err := u.docker.ListContainers(ctx)
			if err != nil {
				u.log.Warn("deps: failed to list containers", "error", err)
				break
			}
			for _, dc := range depContainers {
				if containerName(dc) == dep {
					u.log.Info("restarting dependent container", "dependent", dep, "provider", name)
					if err := u.docker.RestartContainer(ctx, dc.ID); err != nil {
						u.log.Warn("failed to restart dependent", "dependent", dep, "error", err)
					}
					break
				}
			}
		}
	}

	u.log.Info("update complete", "name", name, "duration", duration)
	u.publishEvent(events.EventContainerUpdate, name, "update succeeded")
	return nil
}

// validateContainer checks that a container is running and not restarting.
func (u *Updater) validateContainer(ctx context.Context, id string) (bool, error) {
	inspect, err := u.docker.InspectContainer(ctx, id)
	if err != nil {
		return false, err
	}
	state := inspect.State
	if state == nil {
		return false, fmt.Errorf("container state is nil")
	}
	return state.Running && !state.Restarting, nil
}

// doRollback performs a rollback and records the failure.
func (u *Updater) doRollback(ctx context.Context, name string, snapshotData []byte, start time.Time) {
	if err := rollback(ctx, u.docker, name, snapshotData, u.log); err != nil {
		u.log.Error("rollback also failed", "name", name, "error", err)
		u.publishEvent(events.EventContainerUpdate, name, "rollback failed")
		u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventRollbackFailed,
			ContainerName: name,
			Error:         err.Error(),
			Timestamp:     u.clock.Now(),
		})
	} else {
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
		Error:         "update validation failed",
	}); err != nil {
		u.log.Warn("failed to persist rollback record", "name", name, "error", err)
	}
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

// cloneConfig creates a shallow copy of the container config with cloned labels.
func cloneConfig(cfg *container.Config) *container.Config {
	if cfg == nil {
		return &container.Config{}
	}
	clone := *cfg
	clone.Labels = maps.Clone(cfg.Labels)
	return &clone
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
// The process is: inspect -> clone config without label -> stop -> remove
// -> create -> start. Returns the new container ID.
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

	cleanConfig := cloneConfig(inspect.Config)
	delete(cleanConfig.Labels, guardian.MaintenanceLabel)

	hostConfig := inspect.HostConfig
	netConfig := rebuildNetworkingConfig(inspect.NetworkSettings)

	u.log.Info("finalising container (removing maintenance label)", "name", name)

	if err := u.docker.StopContainer(ctx, id, 10); err != nil {
		return id, &finaliseError{stage: "stop", err: err}
	}

	if err := u.docker.RemoveContainer(ctx, id); err != nil {
		return id, &finaliseError{stage: "remove", err: err}
	}

	newID, err := u.docker.CreateContainer(ctx, name, cleanConfig, hostConfig, netConfig)
	if err != nil {
		return id, &finaliseError{stage: "create", err: err}
	}

	if err := u.docker.StartContainer(ctx, newID); err != nil {
		return newID, &finaliseError{stage: "start", err: err}
	}

	u.log.Info("finalised container", "name", name, "new_id", truncateID(newID))
	return newID, nil
}

// rebuildNetworkingConfig extracts only the IPAM config, aliases, and driver opts
// from NetworkSettings — NOT operational fields like Gateway or IPAddress.
func rebuildNetworkingConfig(ns *container.NetworkSettings) *network.NetworkingConfig {
	if ns == nil || len(ns.Networks) == 0 {
		return nil
	}

	endpoints := make(map[string]*network.EndpointSettings)
	for netName, ep := range ns.Networks {
		endpoints[netName] = &network.EndpointSettings{
			IPAMConfig: ep.IPAMConfig,
			Aliases:    ep.Aliases,
			DriverOpts: ep.DriverOpts,
			NetworkID:  ep.NetworkID,
			MacAddress: ep.MacAddress,
		}
	}
	return &network.NetworkingConfig{
		EndpointsConfig: endpoints,
	}
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
// restarts any dependents that share this container's namespace (if it's a provider).
func (u *Updater) repairNetworkNamespace(ctx context.Context, id, name string) {
	inspect, err := u.docker.InspectContainer(ctx, id)
	if err != nil {
		u.log.Warn("namespace check: inspect failed", "name", name, "error", err)
		return
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

	// Case 2: other containers may share this container's namespace — restart them.
	containers, err := u.docker.ListContainers(ctx)
	if err != nil {
		u.log.Warn("namespace check: list failed", "error", err)
		return
	}

	for _, c := range containers {
		cName := containerName(c)
		if cName == name {
			continue // skip self
		}
		dep, err := u.docker.InspectContainer(ctx, c.ID)
		if err != nil {
			continue
		}
		if dep.HostConfig == nil || !dep.HostConfig.NetworkMode.IsContainer() {
			continue
		}
		ref := dep.HostConfig.NetworkMode.ConnectedContainer()
		if ref == name || ref == id {
			u.log.Info("restarting network dependent", "dependent", cName, "provider", name)
			if err := u.docker.RestartContainer(ctx, c.ID); err != nil {
				u.log.Warn("failed to restart dependent", "dependent", cName, "error", err)
			}
		}
	}
}

// effectiveNotifyMode returns the notification mode for a container,
// falling back to the global default_notify_mode setting.
func (u *Updater) effectiveNotifyMode(name string) string {
	pref, err := u.store.GetNotifyPref(name)
	if err == nil && pref != nil && pref.Mode != "" && pref.Mode != "default" {
		return pref.Mode
	}
	if u.settings != nil {
		val, _ := u.settings.LoadSetting("default_notify_mode")
		if val != "" {
			return val
		}
	}
	return "default"
}
