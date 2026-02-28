package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
)

// ---------------------------------------------------------------------------
// Multi-host (cluster) scanning
// ---------------------------------------------------------------------------

// scanRemoteHosts iterates connected agents and scans their containers for
// updates. Registry checks run server-side (shared rate limit pool); the
// actual pull/restart is dispatched to the remote agent via ClusterScanner.
func (u *Updater) scanRemoteHosts(ctx context.Context, mode ScanMode, result *ScanResult, filters []string, reserve int) {
	hosts := u.cluster.ConnectedHosts()
	if len(hosts) == 0 {
		return
	}

	u.log.Info("scanning remote hosts", "count", len(hosts))

	for _, hostID := range hosts {
		if ctx.Err() != nil {
			return
		}

		hostCtx, ok := u.cluster.HostInfo(hostID)
		if !ok {
			continue
		}

		u.scanRemoteHost(ctx, hostID, hostCtx, mode, result, filters, reserve)
	}
}

// scanRemoteHost scans a single remote host's containers for updates.
// Policy resolution, filtering, and registry checks all happen server-side.
// Only the container update itself is dispatched to the remote agent.
func (u *Updater) scanRemoteHost(ctx context.Context, hostID string, host HostContext, mode ScanMode, result *ScanResult, filters []string, reserve int) {
	containers, err := u.cluster.ListContainers(ctx, hostID)
	if err != nil {
		u.log.Error("failed to list remote containers", "host", host.HostName, "error", err)
		return
	}

	u.log.Info("scanning remote host", "host", host.HostName, "containers", len(containers))

	remoteDefault := u.cfg.DefaultPolicy()
	if u.settings != nil {
		if v, err := u.settings.LoadSetting(store.SettingClusterRemotePolicy); err == nil && v != "" {
			remoteDefault = v
		}
	}

	for _, c := range containers {
		if ctx.Err() != nil {
			return
		}

		// Skip Swarm task containers — managed by the orchestrator.
		if _, isTask := c.Labels["com.docker.swarm.task"]; isTask {
			continue
		}

		result.Total++

		// Skip based on policy (same resolution as local containers).
		tag := registry.ExtractTag(c.Image)
		resolved := ResolvePolicy(u.store, c.Labels, store.ScopedKey(hostID, c.Name), tag, remoteDefault, u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned remote container", "host", host.HostName, "name", c.Name)
			result.Skipped++
			continue
		}

		// Sentinel on remote hosts is checked for updates but never auto-updated.
		remoteSelf := isSentinel(c.Labels)

		// Skip containers matching filter patterns.
		if MatchesFilter(c.Name, filters) {
			u.log.Debug("skipping filtered remote container", "host", host.HostName, "name", c.Name)
			result.Skipped++
			continue
		}

		// Rate limit check (shared server-side pool).
		if u.rateTracker != nil {
			regHost := registry.RegistryHost(c.Image)
			canProceed, wait := u.rateTracker.CanProceed(regHost, reserve)
			if !canProceed {
				if mode == ScanManual {
					u.log.Warn("rate limit exhausted during remote scan, stopping",
						"registry", regHost, "resets_in", wait)
					result.RateLimited++
					return
				}
				u.log.Debug("rate limit low, skipping remote container",
					"host", host.HostName, "name", c.Name, "registry", regHost)
				result.RateLimited++
				continue
			}
		}

		// Registry check (server-side). Use the agent-reported digest
		// instead of local Docker inspect — the image may not exist on
		// the server's Docker daemon.
		semverScope := docker.ContainerSemverScope(c.Labels)
		includeRE, excludeRE := docker.ContainerTagFilters(c.Labels)
		check := u.checker.CheckVersionedWithDigest(ctx, c.Image, c.ImageDigest, semverScope, includeRE, excludeRE)
		if check.Error != nil {
			u.log.Warn("registry check failed for remote container",
				"host", host.HostName, "name", c.Name, "error", check.Error)
			continue
		}

		if check.IsLocal || !check.UpdateAvailable {
			continue
		}

		u.log.Info("remote update available",
			"host", host.HostName, "name", c.Name, "image", c.Image,
			"remote_digest", check.RemoteDigest)

		// Build target image for semver version bumps.
		scanTarget := ""
		if len(check.NewerVersions) > 0 {
			scanTarget = replaceTag(c.Image, check.NewerVersions[0])
		}

		scopedName := store.ScopedKey(hostID, c.Name)

		// Remote sentinel is always queued (never auto-updated via scan).
		if remoteSelf {
			u.queue.Add(PendingUpdate{
				ContainerName:          c.Name,
				CurrentImage:           c.Image,
				CurrentDigest:          c.ImageDigest,
				RemoteDigest:           check.RemoteDigest,
				DetectedAt:             u.clock.Now(),
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
				HostID:                 hostID,
				HostName:               host.HostName,
			})
			u.log.Info("remote sentinel update detected, queued for manual action",
				"host", host.HostName, "name", c.Name)
			result.Queued++
			continue
		}

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++
			if u.isDryRun() {
				u.log.Info("dry-run: would update remote container", "host", host.HostName, "name", c.Name, "target", scanTarget)
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: scopedName,
					OldImage:      c.Image,
					NewImage:      scanTarget,
					Outcome:       "dry_run",
				})
				continue
			}
			// Delay check using the scoped name for remote containers.
			delay := docker.ContainerUpdateDelay(c.Labels)
			if delay == 0 {
				delay = u.globalUpdateDelay()
			}
			if delay > 0 {
				state, _ := u.store.GetNotifyState(scopedName)
				if state != nil && !state.FirstSeen.IsZero() {
					age := u.clock.Now().Sub(state.FirstSeen)
					if age < delay {
						u.log.Info("remote update delayed", "host", host.HostName, "name", c.Name,
							"age", age.Round(time.Minute), "required", delay)
						result.Skipped++
						continue
					}
				} else {
					u.log.Info("remote update delay: first detection, waiting",
						"host", host.HostName, "name", c.Name, "delay", delay)
					result.Skipped++
					continue
				}
			}

			// Dispatch update to the remote agent.
			ur, updateErr := u.cluster.UpdateContainer(ctx, hostID, c.Name, scanTarget, check.RemoteDigest)
			if updateErr != nil {
				u.log.Error("remote update failed",
					"host", host.HostName, "name", c.Name, "error", updateErr)
				result.Errors = append(result.Errors, fmt.Errorf("%s/%s: %w", host.HostName, c.Name, updateErr))
				result.Failed++
				continue
			}

			// Record update in host-scoped history.
			if err := u.store.RecordUpdate(store.UpdateRecord{
				Timestamp:     u.clock.Now(),
				ContainerName: scopedName,
				OldImage:      ur.OldImage,
				OldDigest:     ur.OldDigest,
				NewImage:      ur.NewImage,
				NewDigest:     ur.NewDigest,
				Outcome:       ur.Outcome,
				Duration:      ur.Duration,
			}); err != nil {
				u.log.Warn("failed to record remote update history", "name", scopedName, "error", err)
			}

			// SSE event with host context.
			if u.events != nil {
				u.events.Publish(events.SSEEvent{
					Type:          events.EventContainerUpdate,
					ContainerName: c.Name,
					HostName:      host.HostName,
					Message:       fmt.Sprintf("updated %s on %s: %s", c.Name, host.HostName, ur.Outcome),
					Timestamp:     u.clock.Now(),
				})
			}

			if ur.Outcome == "success" {
				result.Updated++
			} else {
				result.Failed++
			}

		case docker.PolicyManual:
			// Queue for manual approval with host context.
			u.queue.Add(PendingUpdate{
				ContainerName:          c.Name,
				CurrentImage:           c.Image,
				CurrentDigest:          c.ImageDigest,
				RemoteDigest:           check.RemoteDigest,
				DetectedAt:             u.clock.Now(),
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
				HostID:                 hostID,
				HostName:               host.HostName,
			})
			u.log.Info("remote update queued for approval",
				"host", host.HostName, "name", c.Name)
			u.publishEvent(events.EventQueueChange, scopedName, "queued for approval")
			result.Queued++
		}
	}
}

// scanPortainerEndpoints iterates Portainer endpoints and scans their containers.
// Registry checks run server-side; updates are dispatched via PortainerScanner.
func (u *Updater) scanPortainerEndpoints(ctx context.Context, mode ScanMode, result *ScanResult, filters []string, reserve int) {
	u.portainer.ResetCache()

	endpoints, err := u.portainer.Endpoints(ctx)
	if err != nil {
		u.log.Error("failed to list Portainer endpoints", "error", err)
		return
	}
	if len(endpoints) == 0 {
		return
	}

	u.log.Info("scanning Portainer endpoints", "count", len(endpoints))

	for _, ep := range endpoints {
		if ctx.Err() != nil {
			return
		}
		u.scanPortainerEndpoint(ctx, ep, mode, result, filters, reserve)
	}
}

// scanPortainerEndpoint scans a single Portainer endpoint's containers for updates.
func (u *Updater) scanPortainerEndpoint(ctx context.Context, ep PortainerEndpointInfo, mode ScanMode, result *ScanResult, filters []string, reserve int) {
	containers, err := u.portainer.EndpointContainers(ctx, ep.ID)
	if err != nil {
		u.log.Error("failed to list Portainer endpoint containers", "endpoint", ep.Name, "error", err)
		return
	}

	u.log.Info("scanning Portainer endpoint", "endpoint", ep.Name, "containers", len(containers))

	hostID := fmt.Sprintf("portainer:%d", ep.ID)
	remoteDefault := u.cfg.DefaultPolicy()

	// Track redeployed stacks to avoid re-triggering the same stack multiple times.
	redeployedStacks := make(map[int]bool)

	for _, c := range containers {
		if ctx.Err() != nil {
			return
		}

		result.Total++

		tag := registry.ExtractTag(c.Image)
		resolved := ResolvePolicy(u.store, c.Labels, store.ScopedKey(hostID, c.Name), tag, remoteDefault, u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned Portainer container", "endpoint", ep.Name, "name", c.Name)
			result.Skipped++
			continue
		}

		// Sentinel on Portainer-managed hosts is checked for updates but never auto-updated.
		remoteSelf := isSentinel(c.Labels)

		if MatchesFilter(c.Name, filters) {
			u.log.Debug("skipping filtered Portainer container", "endpoint", ep.Name, "name", c.Name)
			result.Skipped++
			continue
		}

		if u.rateTracker != nil {
			regHost := registry.RegistryHost(c.Image)
			canProceed, wait := u.rateTracker.CanProceed(regHost, reserve)
			if !canProceed {
				if mode == ScanManual {
					u.log.Warn("rate limit exhausted during Portainer scan, stopping",
						"registry", regHost, "resets_in", wait)
					result.RateLimited++
					return
				}
				u.log.Debug("rate limit low, skipping Portainer container",
					"endpoint", ep.Name, "name", c.Name, "registry", regHost)
				result.RateLimited++
				continue
			}
		}

		// No image digest available from the Portainer list API — pass "" so
		// CheckVersionedWithDigest falls back to a full registry check.
		semverScope := docker.ContainerSemverScope(c.Labels)
		includeRE, excludeRE := docker.ContainerTagFilters(c.Labels)
		check := u.checker.CheckVersionedWithDigest(ctx, c.Image, "", semverScope, includeRE, excludeRE)
		if check.Error != nil {
			u.log.Warn("registry check failed for Portainer container",
				"endpoint", ep.Name, "name", c.Name, "error", check.Error)
			continue
		}

		if check.IsLocal || !check.UpdateAvailable {
			continue
		}

		// Filter out ignored versions so they don't trigger queuing.
		if len(check.NewerVersions) > 0 {
			ignored, _ := u.store.GetIgnoredVersions(store.ScopedKey(hostID, c.Name))
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
					u.log.Debug("all newer versions ignored", "endpoint", ep.Name, "name", c.Name, "ignored", ignored)
					continue
				}
				check.NewerVersions = filtered
			}
		}

		u.log.Info("Portainer update available",
			"endpoint", ep.Name, "name", c.Name, "image", c.Image)

		scanTarget := ""
		if len(check.NewerVersions) > 0 {
			scanTarget = replaceTag(c.Image, check.NewerVersions[0])
		}

		// Fix 1: fallback to c.Image when semver resolution yields no target.
		if scanTarget == "" {
			scanTarget = c.Image
		}

		scopedName := store.ScopedKey(hostID, c.Name)

		// Portainer-managed Sentinel is always queued (never auto-updated via scan).
		if remoteSelf {
			u.queue.Add(PendingUpdate{
				ContainerName:          c.Name,
				CurrentImage:           c.Image,
				RemoteDigest:           check.RemoteDigest,
				DetectedAt:             u.clock.Now(),
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
				HostID:                 hostID,
				HostName:               ep.Name,
			})
			u.log.Info("Portainer sentinel update detected, queued for manual action",
				"endpoint", ep.Name, "name", c.Name)
			result.Queued++
			continue
		}

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++
			if u.isDryRun() {
				u.log.Info("dry-run: would update Portainer container",
					"endpoint", ep.Name, "name", c.Name, "target", scanTarget)
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: scopedName,
					OldImage:      c.Image,
					NewImage:      scanTarget,
					Outcome:       "dry_run",
				})
				continue
			}

			// Delay check: skip if the update hasn't been available long enough.
			delay := docker.ContainerUpdateDelay(c.Labels)
			if delay == 0 {
				delay = u.globalUpdateDelay()
			}
			if delay > 0 {
				state, _ := u.store.GetNotifyState(scopedName)
				if state != nil && !state.FirstSeen.IsZero() {
					age := u.clock.Now().Sub(state.FirstSeen)
					if age < delay {
						u.log.Info("Portainer update delayed",
							"endpoint", ep.Name, "name", c.Name,
							"age", age.Round(time.Minute), "required", delay)
						result.Skipped++
						continue
					}
				} else {
					u.log.Info("Portainer update delay: first detection, waiting",
						"endpoint", ep.Name, "name", c.Name, "delay", delay)
					result.Skipped++
					continue
				}
			}

			var updateErr error
			if c.StackID != 0 {
				// Stack container — redeploy the whole stack once.
				if redeployedStacks[c.StackID] {
					result.Updated++
					continue
				}
				updateErr = u.portainer.RedeployStack(ctx, c.StackID, ep.ID)
				if updateErr == nil {
					redeployedStacks[c.StackID] = true
				}
			} else {
				// Standalone container.
				updateErr = u.portainer.UpdateStandaloneContainer(ctx, ep.ID, c.ID, scanTarget)
			}

			outcome := "success"
			if updateErr != nil {
				u.log.Error("Portainer update failed",
					"endpoint", ep.Name, "name", c.Name, "error", updateErr)
				result.Errors = append(result.Errors, fmt.Errorf("%s/%s: %w", ep.Name, c.Name, updateErr))
				result.Failed++
				outcome = "failed"
			} else {
				result.Updated++
			}

			if err := u.store.RecordUpdate(store.UpdateRecord{
				Timestamp:     u.clock.Now(),
				ContainerName: scopedName,
				OldImage:      c.Image,
				NewImage:      scanTarget,
				Outcome:       outcome,
			}); err != nil {
				u.log.Warn("failed to record Portainer update history", "name", scopedName, "error", err)
			}

			if u.events != nil {
				u.events.Publish(events.SSEEvent{
					Type:          events.EventContainerUpdate,
					ContainerName: c.Name,
					HostName:      ep.Name,
					Message:       fmt.Sprintf("updated %s on %s: %s", c.Name, ep.Name, outcome),
					Timestamp:     u.clock.Now(),
				})
			}

		case docker.PolicyManual:
			u.queue.Add(PendingUpdate{
				ContainerName:          c.Name,
				CurrentImage:           c.Image,
				RemoteDigest:           check.RemoteDigest,
				DetectedAt:             u.clock.Now(),
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
				HostID:                 hostID,
				HostName:               ep.Name,
			})
			u.log.Info("Portainer update queued for approval",
				"endpoint", ep.Name, "name", c.Name)
			u.publishEvent(events.EventQueueChange, scopedName, "queued for approval")
			result.Queued++
		}
	}
}

// checkGHCRAlternatives probes GHCR for alternatives to Docker Hub images.
// Runs as a background goroutine after each scan. Skips images that already
// have a valid (non-expired) cache entry.
func (u *Updater) checkGHCRAlternatives(ctx context.Context, containers []container.Summary) {
	// Check if GHCR detection is enabled (default: true).
	if u.settings != nil {
		val, err := u.settings.LoadSetting("ghcr_check_enabled")
		if err != nil {
			u.log.Debug("failed to load ghcr_check_enabled", "error", err)
		}
		if val == "false" {
			return
		}
	}

	// Gather credentials for Docker Hub and GHCR.
	var hubCred, ghcrCred *registry.RegistryCredential
	if cs := u.checker.CredentialStore(); cs != nil {
		creds, _ := cs.GetRegistryCredentials()
		hubCred = registry.FindByRegistry(creds, "docker.io")
		ghcrCred = registry.FindByRegistry(creds, "ghcr.io")
	}

	checked := 0
	for _, c := range containers {
		if ctx.Err() != nil {
			break
		}

		host := registry.RegistryHost(c.Image)
		if host != "docker.io" {
			continue
		}

		repo := registry.RepoPath(c.Image)
		tag := registry.ExtractTag(c.Image)
		if tag == "" {
			tag = "latest"
		}

		// Skip if already cached and not expired.
		if _, ok := u.ghcrCache.Get(repo, tag); ok {
			continue
		}

		// Rate limit check: each GHCR alternative check makes ~2 requests
		// to Docker Hub and ~2 to GHCR. Skip if either registry is low.
		if u.rateTracker != nil {
			if ok, _ := u.rateTracker.CanProceed("docker.io", 5); !ok {
				u.log.Debug("GHCR check: Docker Hub rate limit low, stopping")
				break
			}
			if ok, _ := u.rateTracker.CanProceed("ghcr.io", 5); !ok {
				u.log.Debug("GHCR check: GHCR rate limit low, stopping")
				break
			}
		}

		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		alt, err := registry.CheckGHCRAlternative(checkCtx, c.Image, hubCred, ghcrCred)
		cancel()

		if err != nil {
			u.log.Debug("GHCR check failed", "image", c.Image, "error", err)
			continue
		}
		if alt == nil {
			continue // skipped (library image or non-docker.io)
		}

		u.ghcrCache.Set(repo, tag, *alt)
		checked++

		if alt.Available {
			match := "different build"
			if alt.DigestMatch {
				match = "identical"
			}
			u.log.Info("GHCR alternative found", "image", c.Image, "ghcr", alt.GHCRImage, "digest", match)
		}
	}

	// Persist cache to DB.
	if u.ghcrSaver != nil && checked > 0 {
		if data, err := u.ghcrCache.Export(); err == nil {
			if err := u.ghcrSaver(data); err != nil {
				u.log.Warn("failed to persist GHCR cache", "error", err)
			}
		}
	}

	// Emit SSE event so the UI refreshes.
	if checked > 0 {
		u.publishEvent(events.EventGHCRCheck, "", fmt.Sprintf("checked %d images", checked))
	}
}
