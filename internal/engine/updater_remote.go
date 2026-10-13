package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
	"github.com/moby/moby/api/types/container"
	"github.com/robfig/cron/v3"
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
		scopedName := store.ScopedKey(hostID, c.Name)

		// An update of it is running: leave it to that update, which would
		// otherwise be queued or dispatched again from the old digest.
		if tr, ok := u.cluster.(remoteUpdateTracker); ok && tr.UpdateInFlight(hostID, c.Name) {
			u.log.Debug("remote update in progress, not checking", "host", host.HostName, "name", c.Name)
			result.Skipped++
			continue
		}

		// Skip based on policy (same resolution as local containers).
		tag := registry.ExtractTag(c.Image)
		resolved := ResolvePolicy(u.store, c.Labels, scopedName, tag, remoteDefault, u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned remote container", "host", host.HostName, "name", c.Name)
			u.dropQueued(scopedName, "pinned")
			result.Skipped++
			continue
		}

		// Sentinel on remote hosts is checked for updates but never auto-updated.
		remoteSelf := isSentinel(c.Labels)

		// Skip containers matching filter patterns.
		if MatchesFilter(c.Name, filters) {
			u.log.Debug("skipping filtered remote container", "host", host.HostName, "name", c.Name)
			u.dropQueued(scopedName, "filtered out")
			result.Skipped++
			continue
		}

		// Per-container schedule (sentinel.schedule), as for a local one.
		if !u.scheduleDue(scopedName, c.Labels) {
			result.Skipped++
			continue
		}

		// Rate limit check (shared server-side pool).
		// Skip this container but keep scanning — other registries may be available.
		if u.rateTracker != nil {
			regHost := registry.RegistryHost(c.Image)
			canProceed, wait := u.rateTracker.CanProceed(regHost, reserve)
			if !canProceed {
				u.log.Debug("rate limit low, skipping remote container",
					"host", host.HostName, "name", c.Name, "registry", regHost, "resets_in", wait)
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
		if !check.IsLocal {
			// Record scan time for per-container schedule tracking.
			_ = u.store.SetLastContainerScan(scopedName, u.clock.Now())
		}

		if check.IsLocal || !check.UpdateAvailable {
			// An update queued earlier no longer applies.
			u.dropQueued(scopedName, "now up to date")
			if !check.IsLocal {
				result.UpToDate++
			}
			continue
		}

		// "Ignore version" on a remote queue row saves under the scoped key.
		if u.filterIgnored(scopedName, &check) {
			u.log.Debug("all newer versions ignored", "host", host.HostName, "name", c.Name)
			u.dropQueued(scopedName, "version ignored")
			continue
		}

		u.log.Info("remote update available",
			"host", host.HostName, "name", c.Name, "image", c.Image,
			"remote_digest", check.RemoteDigest)
		u.publishEvent(events.EventContainerUpdate, scopedName, "update available")
		label := c.Name + " on " + host.HostName
		u.announceRemoteUpdate(ctx, scopedName, label, c, check)

		// Target image: the newer tag for a version bump, otherwise (a
		// same-tag update) the container's own image, which the agent
		// re-pulls. Agents before v2.17.0 fail on an empty target.
		scanTarget := c.Image
		if len(check.NewerVersions) > 0 {
			scanTarget = replaceTag(c.Image, check.NewerVersions[0])
		}

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
					HostID:        hostID,
					HostName:      host.HostName,
				})
				continue
			}
			// A paused host gets no new updates.
			if p, ok := u.cluster.(remoteHostPauser); ok && p.HostPaused(hostID) {
				u.log.Info("host is paused, not updating", "host", host.HostName, "name", c.Name)
				result.Skipped++
				continue
			}
			// Pull-only (global setting or sentinel.pull-only): the agent
			// pulls the image and the container is left as it is.
			if docker.ContainerPullOnly(c.Labels) || u.isPullOnly() {
				u.remotePullOnly(ctx, hostID, host, c, scanTarget, result)
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

			// Maintenance window: outside it, auto updates wait.
			if windowExpr := u.maintenanceWindow(); windowExpr != "" {
				win, err := ParseWindow(windowExpr)
				if err != nil {
					u.log.Warn("invalid maintenance window, proceeding with update (fail-open)", "name", scopedName, "window", windowExpr, "error", err)
				} else if win != nil && !win.IsOpen(u.clock.Now()) {
					u.log.Info("outside maintenance window, deferring remote auto-update", "host", host.HostName, "name", c.Name, "window", windowExpr)
					result.Skipped++
					continue
				}
			}

			// Dispatch update to the remote agent.
			start := u.clock.Now()
			u.notifier.Notify(ctx, notify.Event{
				Type: notify.EventUpdateStarted, ContainerName: label, OldImage: c.Image, Timestamp: start,
			})
			ur, updateErr := u.cluster.UpdateContainer(ctx, hostID, c.Name, scanTarget, check.RemoteDigest)
			if updateErr != nil {
				u.log.Error("remote update failed",
					"host", host.HostName, "name", c.Name, "error", updateErr)
				result.Errors = append(result.Errors, fmt.Errorf("%s/%s: %w", host.HostName, c.Name, updateErr))
				result.Failed++
				ur = RemoteUpdateResult{OldImage: c.Image, OldDigest: c.ImageDigest, NewImage: scanTarget,
					Outcome: "failed", Error: updateErr.Error(), Duration: u.clock.Since(start)}
			}
			if ur.Outcome == "skipped" {
				// A pre-update hook asked to skip it: nothing changed.
				u.log.Info("remote update skipped by a pre-update hook", "host", host.HostName, "name", c.Name)
				result.Skipped++
				continue
			}

			// Record update in host-scoped history. (The cluster server
			// publishes the result to the dashboard, with the host.)
			if err := u.store.RecordUpdate(store.UpdateRecord{
				Timestamp:     u.clock.Now(),
				ContainerName: scopedName,
				OldImage:      ur.OldImage,
				OldDigest:     ur.OldDigest,
				NewImage:      ur.NewImage,
				NewDigest:     ur.NewDigest,
				Outcome:       ur.Outcome,
				Error:         ur.Error,
				Duration:      ur.Duration,
				HostID:        hostID,
				HostName:      host.HostName,
			}); err != nil {
				u.log.Warn("failed to record remote update history", "name", scopedName, "error", err)
			}
			u.notifyRemoteResult(ctx, label, ur)
			if updateErr != nil {
				continue // counted as failed above
			}
			if ur.Outcome == "success" {
				result.Updated++
				if err := u.store.ClearNotifyState(scopedName); err != nil {
					u.log.Warn("failed to clear notify state after update", "name", scopedName, "error", err)
				}
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

// remoteUpdateTracker is implemented by a ClusterScanner that knows which
// remote updates are in flight.
type remoteUpdateTracker interface {
	UpdateInFlight(hostID, name string) bool
}

// remoteHostPauser is implemented by a ClusterScanner that knows which
// hosts are paused (no new updates).
type remoteHostPauser interface {
	HostPaused(hostID string) bool
}

// remotePuller is implemented by a ClusterScanner that can ask an agent to
// pull an image.
type remotePuller interface {
	PullImage(ctx context.Context, hostID, ref string) error
}

// scheduleDue reports whether a container with a sentinel.schedule label
// is due a check (key is its store key); one without the label always is.
func (u *Updater) scheduleDue(key string, labels map[string]string) bool {
	sched := docker.ContainerSchedule(labels)
	if sched == "" {
		return true
	}
	schedule, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(sched)
	if err != nil {
		u.log.Warn("invalid schedule", "name", key, "schedule", sched, "error", err)
		return true
	}
	lastChecked, _ := u.store.GetLastContainerScan(key)
	return lastChecked.IsZero() || !u.clock.Now().Before(schedule.Next(lastChecked))
}

// announceRemoteUpdate notifies that an update of a remote container (key
// is its scoped key, label how notifications name it) is available, with
// the notify mode, dedup and snooze of a local container, and keeps its
// notify state: FirstSeen is what the update delay counts from.
func (u *Updater) announceRemoteUpdate(ctx context.Context, key, label string, c RemoteContainer, check registry.CheckResult) {
	now := u.clock.Now()
	existing, _ := u.store.GetNotifyState(key)
	notifyOK := false
	if shouldNotifyUpdate(u.effectiveNotifyMode(key), existing, check.RemoteDigest, now) {
		notifyOK = u.notifier.Notify(ctx, notify.Event{
			Type:          notify.EventUpdateAvailable,
			ContainerName: label,
			OldImage:      c.Image,
			OldDigest:     c.ImageDigest,
			NewDigest:     check.RemoteDigest,
			Timestamp:     now,
		})
	}
	state := &store.NotifyState{LastDigest: check.RemoteDigest, FirstSeen: now}
	if existing != nil {
		if !existing.FirstSeen.IsZero() {
			state.FirstSeen = existing.FirstSeen
		}
		state.LastNotified = existing.LastNotified
		state.SnoozedUntil = existing.SnoozedUntil
	}
	if notifyOK {
		state.LastNotified = now
		if d := docker.ContainerNotifySnooze(c.Labels); d > 0 {
			state.SnoozedUntil = now.Add(d)
		}
	}
	if err := u.store.SetNotifyState(key, state); err != nil {
		u.log.Warn("failed to persist notify state", "name", key, "error", err)
	}
}

// NotifyRemote sends the notification of a remote update that was not
// started by a scan (the dashboard or the API): its start when ur is nil,
// else its outcome. A skipped update sends nothing.
func (u *Updater) NotifyRemote(ctx context.Context, hostName, name, oldImage string, ur *RemoteUpdateResult) {
	if u.notifier == nil || u.clock == nil {
		return
	}
	label := name + " on " + hostName
	if ur == nil {
		u.notifier.Notify(ctx, notify.Event{Type: notify.EventUpdateStarted, ContainerName: label, OldImage: oldImage, Timestamp: u.clock.Now()})
		return
	}
	if ur.Outcome == "skipped" {
		return
	}
	r := *ur
	if r.OldImage == "" {
		r.OldImage = oldImage
	}
	u.notifyRemoteResult(ctx, label, r)
}

// notifyRemoteResult notifies the outcome of a remote update.
func (u *Updater) notifyRemoteResult(ctx context.Context, label string, ur RemoteUpdateResult) {
	ev := notify.Event{ContainerName: label, OldImage: ur.OldImage, NewImage: ur.NewImage, NewDigest: ur.NewDigest, Timestamp: u.clock.Now()}
	switch ur.Outcome {
	case "success":
		ev.Type = notify.EventUpdateSucceeded
	case "rollback":
		ev.Type, ev.Error = notify.EventRollbackOK, ur.Error
	default:
		ev.Type, ev.Error = notify.EventUpdateFailed, ur.Error
	}
	u.notifier.Notify(ctx, ev)
}

// remotePullOnly asks the agent to pull target for a pull-only container,
// as a local pull-only update pulls and leaves the container alone.
func (u *Updater) remotePullOnly(ctx context.Context, hostID string, host HostContext, c RemoteContainer, target string, result *ScanResult) {
	key := store.ScopedKey(hostID, c.Name)
	p, ok := u.cluster.(remotePuller)
	if !ok {
		u.log.Warn("pull-only: this cluster cannot ask agents to pull, leaving the container alone", "host", host.HostName, "name", c.Name)
		result.Skipped++
		return
	}
	if err := p.PullImage(ctx, hostID, target); err != nil {
		u.log.Error("pull-only failed", "host", host.HostName, "name", c.Name, "error", err)
		result.Failed++
		result.Errors = append(result.Errors, fmt.Errorf("%s/%s: pull-only: %w", host.HostName, c.Name, err))
		return
	}
	_ = u.store.RecordUpdate(store.UpdateRecord{
		Timestamp:     u.clock.Now(),
		ContainerName: key,
		OldImage:      c.Image,
		NewImage:      target,
		Outcome:       "pull_only",
		HostID:        hostID,
		HostName:      host.HostName,
	})
	result.Updated++
}

// dropQueued removes the queued update of a remote container (by its
// scoped key) that no longer applies, saying why.
func (u *Updater) dropQueued(key, why string) {
	if _, queued := u.queue.Get(key); !queued {
		return
	}
	u.queue.Remove(key)
	u.log.Info("removed stale queue entry ("+why+")", "name", key)
	u.publishEvent(events.EventQueueChange, key, "removed: "+why)
}

// VerifyRemoteImage applies the signature gate of a local update (the
// container's sentinel.verify label, else the global mode) to image, which
// an agent is about to update the remote container name to. The hub checks
// it before the agent is asked to do anything. It returns an error when the
// update must not go ahead: the image is not signed and the mode is
// enforce, or the mode is enforce and cosign is not available.
func (u *Updater) VerifyRemoteImage(ctx context.Context, name string, labels map[string]string, image string) error {
	mode := u.verifyModeFor(labels)
	switch {
	case mode == verify.ModeDisabled:
		return nil
	case u.imgVerifier == nil:
		if mode == verify.ModeEnforce {
			u.log.Warn("signature verification is set to enforce but cosign is not available, refusing the remote update",
				"name", name, "image", image)
			return fmt.Errorf("update %s: %w", name, ErrVerifierUnavailable)
		}
		u.log.Warn("signature verification is set to warn but cosign is not available, updating without a signature check",
			"name", name, "image", image)
		return nil
	}
	result := u.imgVerifier.Verify(ctx, image)
	switch {
	case result.Verified:
		u.log.Info("signature verified", "name", name, "image", image)
	case mode == verify.ModeEnforce:
		u.log.Warn("signature verification failed, blocking the remote update", "name", name, "image", image, "error", result.Error)
		return fmt.Errorf("signature verification failed for %s: %s", name, result.Error)
	default:
		u.log.Warn("signature verification failed, proceeding (warn mode)", "name", name, "image", image, "error", result.Error)
	}
	return nil
}

// AgentSettings are the update settings the hub sends its agents
// (SettingsSync).
type AgentSettings struct {
	PollInterval     time.Duration
	GracePeriod      time.Duration
	LatestAutoUpdate bool
	ImageCleanup     bool
	ImageBackup      bool
	RemoveVolumes    bool
	HooksEnabled     bool
	DependencyAware  bool
	RollbackPolicy   string
}

// AgentSettings returns the settings the hub's agents follow, resolved as
// the hub resolves them for its own updates (a value saved in the dashboard
// wins where it does locally).
func (u *Updater) AgentSettings() AgentSettings {
	if u.cfg == nil {
		return AgentSettings{}
	}
	return AgentSettings{
		PollInterval:     u.cfg.PollInterval(),
		GracePeriod:      u.cfg.GracePeriod(),
		LatestAutoUpdate: u.cfg.LatestAutoUpdate(),
		ImageCleanup:     u.cfg.ImageCleanup(),
		ImageBackup:      u.isImageBackup(),
		RemoveVolumes:    u.isRemoveVolumes(),
		HooksEnabled:     u.cfg.HooksEnabled(),
		DependencyAware:  u.cfg.DependencyAware(),
		RollbackPolicy:   u.rollbackPolicy(),
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
