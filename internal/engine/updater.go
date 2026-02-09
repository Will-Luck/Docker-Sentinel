package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/GiteaLN/Docker-Sentinel/internal/clock"
	"github.com/GiteaLN/Docker-Sentinel/internal/config"
	"github.com/GiteaLN/Docker-Sentinel/internal/docker"
	"github.com/GiteaLN/Docker-Sentinel/internal/logging"
	"github.com/GiteaLN/Docker-Sentinel/internal/registry"
	"github.com/GiteaLN/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// ScanResult summarises a single scan cycle.
type ScanResult struct {
	Total     int
	Skipped   int
	AutoCount int
	Queued    int
	Updated   int
	Failed    int
	Errors    []error
}

// Updater performs container scanning and update operations.
type Updater struct {
	docker  docker.API
	checker *registry.Checker
	store   *store.Store
	queue   *Queue
	cfg     *config.Config
	log     *logging.Logger
	clock   clock.Clock
}

// NewUpdater creates an Updater with all dependencies.
func NewUpdater(d docker.API, checker *registry.Checker, s *store.Store, q *Queue, cfg *config.Config, log *logging.Logger, clk clock.Clock) *Updater {
	return &Updater{
		docker:  d,
		checker: checker,
		store:   s,
		queue:   q,
		cfg:     cfg,
		log:     log,
		clock:   clk,
	}
}

// Scan lists running containers, checks for updates, and processes them
// according to each container's policy.
func (u *Updater) Scan(ctx context.Context) ScanResult {
	result := ScanResult{}

	containers, err := u.docker.ListContainers(ctx)
	if err != nil {
		u.log.Error("failed to list containers", "error", err)
		result.Errors = append(result.Errors, err)
		return result
	}
	result.Total = len(containers)

	for _, c := range containers {
		if ctx.Err() != nil {
			return result
		}

		name := containerName(c)
		labels := c.Labels
		policy := docker.ContainerPolicy(labels, u.cfg.DefaultPolicy)

		// Skip pinned containers.
		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned container", "name", name)
			result.Skipped++
			continue
		}

		// Skip Sentinel itself (avoid self-update loops).
		if isSentinel(labels) {
			u.log.Debug("skipping sentinel container", "name", name)
			result.Skipped++
			continue
		}

		// Check the registry for an update.
		imageRef := c.Image
		check := u.checker.Check(ctx, imageRef)

		if check.Error != nil {
			u.log.Warn("registry check failed", "name", name, "image", imageRef, "error", check.Error)
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", name, check.Error))
			continue
		}

		if check.IsLocal {
			u.log.Debug("local/unresolvable image, skipping", "name", name, "image", imageRef)
			result.Skipped++
			continue
		}

		if !check.UpdateAvailable {
			u.log.Debug("up to date", "name", name, "image", imageRef)
			continue
		}

		u.log.Info("update available", "name", name, "image", imageRef,
			"local_digest", check.LocalDigest, "remote_digest", check.RemoteDigest)

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++
			if err := u.UpdateContainer(ctx, c.ID, name); err != nil {
				u.log.Error("auto-update failed", "name", name, "error", err)
				result.Failed++
				result.Errors = append(result.Errors, err)
			} else {
				result.Updated++
			}

		case docker.PolicyManual:
			u.queue.Add(PendingUpdate{
				ContainerID:   c.ID,
				ContainerName: name,
				CurrentImage:  imageRef,
				CurrentDigest: check.LocalDigest,
				RemoteDigest:  check.RemoteDigest,
				DetectedAt:    u.clock.Now(),
			})
			u.log.Info("update queued for manual approval", "name", name)
			result.Queued++
		}
	}

	return result
}

// UpdateContainer performs the full update lifecycle for a single container:
// snapshot → pull → stop → remove → create → start → validate → (rollback on failure).
func (u *Updater) UpdateContainer(ctx context.Context, id, name string) error {
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

	oldImage := inspect.Config.Image
	u.log.Info("saved snapshot", "name", name, "image", oldImage)

	// 2. Mark maintenance window.
	if err := u.store.SetMaintenance(name, true); err != nil {
		u.log.Warn("failed to set maintenance flag", "name", name, "error", err)
	}

	// 3. Pull the new image.
	u.log.Info("pulling image", "name", name, "image", oldImage)
	if err := u.docker.PullImage(ctx, oldImage); err != nil {
		_ = u.store.SetMaintenance(name, false)
		return fmt.Errorf("pull image for %s: %w", name, err)
	}

	// Get new image digest for the record.
	newDigest, _ := u.docker.ImageDigest(ctx, oldImage)

	// 4. Stop and remove the old container.
	u.log.Info("stopping old container", "name", name)
	if err := u.docker.StopContainer(ctx, id, 30); err != nil {
		u.log.Warn("stop failed, proceeding with force remove", "name", name, "error", err)
	}
	if err := u.docker.RemoveContainer(ctx, id); err != nil {
		_ = u.store.SetMaintenance(name, false)
		return fmt.Errorf("remove old container %s: %w", name, err)
	}

	// 5. Create and start the new container.
	newConfig := cloneConfig(inspect.Config)
	addMaintenanceLabel(newConfig)

	hostConfig := inspect.HostConfig
	netConfig := rebuildNetworkingConfig(inspect.NetworkSettings)

	u.log.Info("creating new container", "name", name, "image", oldImage)
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
	u.log.Info("waiting grace period", "name", name, "duration", u.cfg.GracePeriod)
	select {
	case <-u.clock.After(u.cfg.GracePeriod):
	case <-ctx.Done():
		return ctx.Err()
	}

	healthy, err := u.validateContainer(ctx, newID)
	if err != nil || !healthy {
		u.log.Error("validation failed, rolling back", "name", name, "error", err)
		_ = u.docker.StopContainer(ctx, newID, 10)
		_ = u.docker.RemoveContainer(ctx, newID)
		u.doRollback(ctx, name, snapshotData, start)
		return fmt.Errorf("new container %s failed validation", name)
	}

	// 7. Success — clear maintenance and record.
	_ = u.store.SetMaintenance(name, false)
	u.queue.Remove(name)

	duration := u.clock.Since(start)
	_ = u.store.RecordUpdate(store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		OldImage:      oldImage,
		OldDigest:     extractDigestForRecord(inspect),
		NewImage:      oldImage,
		NewDigest:     newDigest,
		Outcome:       "success",
		Duration:      duration,
	})

	u.log.Info("update complete", "name", name, "duration", duration)
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
	}
	_ = u.store.SetMaintenance(name, false)

	_ = u.store.RecordUpdate(store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: name,
		Outcome:       "rollback",
		Duration:      u.clock.Since(start),
		Error:         "update validation failed",
	})
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
	cfg.Labels["sentinel.maintenance"] = "true"
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

// truncateID safely truncates a container ID to 12 characters for logging.
func truncateID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
