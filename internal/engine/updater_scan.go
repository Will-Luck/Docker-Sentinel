package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/swarm"
	"github.com/robfig/cron/v3"
)

// ScanMode controls rate limit headroom during scans.
type ScanMode int

const (
	// ScanScheduled keeps higher headroom (reserve 10) — silently skips rate-limited containers.
	ScanScheduled ScanMode = iota
	// ScanManual uses almost all quota (reserve 2) — stops scanning on exhaustion.
	ScanManual
)

// ScanResult summarises a single scan cycle.
type ScanResult struct {
	Total       int
	Skipped     int
	AutoCount   int
	Queued      int
	Updated     int
	Failed      int
	RateLimited int // containers skipped due to rate limits
	UpToDate    int // containers where the registry confirmed no update
	Errors      []error

	// Swarm service stats (only populated when Swarm mode is active).
	Services       int
	ServiceUpdates int
}

// ClusterScanner provides access to remote host containers for multi-host scanning.
// Nil when clustering is disabled — single-host mode has zero overhead.
type ClusterScanner interface {
	// ConnectedHosts returns the IDs of all currently connected agent hosts.
	ConnectedHosts() []string
	// HostInfo returns info about a specific host.
	HostInfo(hostID string) (HostContext, bool)
	// ListContainers requests a container list from a remote agent.
	// Blocks until the agent responds or context is cancelled.
	ListContainers(ctx context.Context, hostID string) ([]RemoteContainer, error)
	// UpdateContainer requests an update on a remote agent.
	// Blocks until the agent responds with the result.
	UpdateContainer(ctx context.Context, hostID string, containerName, targetImage, targetDigest string) (RemoteUpdateResult, error)
}

// PortainerScanner provides Portainer endpoint scanning.
type PortainerScanner interface {
	Endpoints(ctx context.Context) ([]PortainerEndpointInfo, error)
	EndpointContainers(ctx context.Context, endpointID int) ([]PortainerContainerResult, error)
	ResetCache()
	RedeployStack(ctx context.Context, stackID, endpointID int) error
	UpdateStandaloneContainer(ctx context.Context, endpointID int, containerID, newImage string) error
}

// PortainerEndpointInfo identifies a Portainer-managed Docker environment.
type PortainerEndpointInfo struct {
	ID   int
	Name string
}

// PortainerContainerResult is a container from a Portainer-managed environment.
type PortainerContainerResult struct {
	ID          string
	Name        string
	Image       string
	ImageDigest string // repo digest from Portainer image inspect
	State       string
	Labels      map[string]string
	EndpointID  int
	StackID     int
	StackName   string
}

// EndpointConfig holds per-endpoint user and auto-detected settings.
type EndpointConfig struct {
	Enabled bool
	Blocked bool
}

// PortainerInstance represents a single Portainer server with its scanner
// and per-endpoint configuration. The engine iterates all instances during scan.
type PortainerInstance struct {
	ID        string
	Name      string
	Scanner   PortainerScanner
	Endpoints map[int]EndpointConfig
}

// HostContext identifies a remote host for scoped operations.
type HostContext struct {
	HostID   string
	HostName string
}

// RemoteContainer is a simplified container representation from a remote agent.
type RemoteContainer struct {
	ID          string
	Name        string
	Image       string
	ImageDigest string
	State       string
	Labels      map[string]string
}

// RemoteUpdateResult from a remote agent.
type RemoteUpdateResult struct {
	ContainerName string
	OldImage      string
	OldDigest     string
	NewImage      string
	NewDigest     string
	Outcome       string
	Error         string
	Duration      time.Duration
}

// loadFilters reads filter patterns from the settings store.
func (u *Updater) loadFilters() []string {
	if u.settings == nil {
		return nil
	}
	val, err := u.settings.LoadSetting("filters")
	if err != nil {
		return nil
	}
	if val == "" {
		return nil
	}
	var patterns []string
	for _, p := range strings.Split(val, "\n") {
		p = strings.TrimSpace(p)
		if p != "" {
			patterns = append(patterns, p)
		}
	}
	return patterns
}

// publishEvent emits an SSE event if the event bus is configured.
// For remote containers the name may be a scoped key ("hostID::name");
// this is split so the SSE event carries proper HostID and ContainerName fields.
func (u *Updater) publishEvent(evtType events.EventType, name, message string) {
	if u.events == nil {
		return
	}
	evt := events.SSEEvent{
		Type:          evtType,
		ContainerName: name,
		Message:       message,
		Timestamp:     u.clock.Now(),
	}
	if idx := strings.Index(name, "::"); idx >= 0 {
		evt.HostID = name[:idx]
		evt.ContainerName = name[idx+2:]
	}
	u.events.Publish(evt)
}

// isDryRun returns true when dry_run mode is enabled in settings.
// In dry-run mode, updates are detected and recorded but never executed.
func (u *Updater) isDryRun() bool {
	if u.settings == nil {
		return false
	}
	val, err := u.settings.LoadSetting("dry_run")
	if err != nil {
		return false
	}
	return val == "true"
}

// isPullOnly returns true when pull_only mode is enabled in settings.
// In pull-only mode, the new image is pulled but the container is not restarted.
func (u *Updater) isPullOnly() bool {
	if u.settings == nil {
		return false
	}
	val, err := u.settings.LoadSetting("pull_only")
	if err != nil {
		return false
	}
	return val == "true"
}

// globalUpdateDelay reads the update_delay setting from the settings store.
// Returns 0 if not set or unparseable.
func (u *Updater) globalUpdateDelay() time.Duration {
	if u.settings == nil {
		return 0
	}
	val, err := u.settings.LoadSetting("update_delay")
	if err != nil || val == "" {
		return 0
	}
	d, err := docker.ParseDurationWithDays(val)
	if err != nil {
		return 0
	}
	return d
}

// rollbackPolicy returns the configured rollback policy, checking both the
// in-memory Config and the persisted SettingsStore. This ensures the engine
// respects UI-configured values even after restart (Config is hydrated from
// env vars only; SettingsStore has the user's persisted preference).
func (u *Updater) rollbackPolicy() string {
	if rp := u.cfg.RollbackPolicy(); rp != "" {
		return rp
	}
	if u.settings != nil {
		if val, err := u.settings.LoadSetting("rollback_policy"); err == nil && val != "" {
			return val
		}
	}
	return ""
}

// isImageBackup returns true when image backup (retag before pull) is enabled.
func (u *Updater) isImageBackup() bool {
	if u.settings != nil {
		val, err := u.settings.LoadSetting("image_backup")
		if err == nil && val == "true" {
			return true
		}
	}
	return u.cfg.ImageBackup()
}

// scanConcurrency returns the number of parallel registry checks to use.
// Returns 1 (sequential) unless overridden via settings or env.
func (u *Updater) scanConcurrency() int {
	if u.settings != nil {
		if val, err := u.settings.LoadSetting("scan_concurrency"); err == nil && val != "" {
			if n, err := strconv.Atoi(val); err == nil && n >= 1 {
				return n
			}
		}
	}
	if n := u.cfg.ScanConcurrency(); n > 1 {
		return n
	}
	return 1
}

// isRemoveVolumes returns true when anonymous volume removal is enabled globally.
func (u *Updater) isRemoveVolumes() bool {
	if u.settings != nil {
		val, err := u.settings.LoadSetting("remove_volumes")
		if err == nil && val == "true" {
			return true
		}
	}
	return u.cfg.RemoveVolumes()
}

// isComposeSync returns true when compose file sync is enabled via settings.
func (u *Updater) isComposeSync() bool {
	if u.settings == nil {
		return false
	}
	val, err := u.settings.LoadSetting("compose_sync")
	if err != nil {
		return false
	}
	return val == "true"
}

// maintenanceWindow returns the active maintenance window expression,
// checking the persisted settings store first, then falling back to config.
func (u *Updater) maintenanceWindow() string {
	if u.settings != nil {
		val, err := u.settings.LoadSetting("maintenance_window")
		if err == nil && val != "" {
			return val
		}
	}
	return u.cfg.MaintenanceWindow()
}

// Scan lists running containers, checks for updates, and processes them
// according to each container's policy. The mode controls rate limit headroom.
func (u *Updater) Scan(ctx context.Context, mode ScanMode) ScanResult {
	scanStart := time.Now()
	result := ScanResult{}
	u.selfUpdateQueued.Store(false)

	if c := u.scanConcurrency(); c > 1 {
		u.log.Info("scan concurrency enabled (experimental)", "concurrency", c)
	}

	containers, err := u.docker.ListContainers(ctx)
	if err != nil {
		u.log.Error("failed to list containers", "error", err)
		result.Errors = append(result.Errors, err)
		return result
	}
	result.Total = len(containers)

	// Discover registries and probe for fresh rate limit data.
	// Probes all discovered registries (credentialed or anonymous) so that
	// rate limit info is always available, even when no containers have updates.
	if u.rateTracker != nil {
		counts := make(map[string]int)
		for _, c := range containers {
			host := registry.RegistryHost(c.Image)
			counts[host]++
		}
		for host, n := range counts {
			u.rateTracker.Discover(host, n)
		}

		var creds []registry.RegistryCredential
		if cs := u.checker.CredentialStore(); cs != nil {
			creds, _ = cs.GetRegistryCredentials()
		}
		for host := range counts {
			host = registry.NormaliseRegistryHost(host)
			cred := registry.FindByRegistry(creds, host)
			probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
			headers, err := registry.ProbeRateLimit(probeCtx, host, cred)
			probeCancel()
			if err != nil {
				u.log.Debug("rate limit probe failed", "registry", host, "error", err)
				continue
			}
			u.rateTracker.Record(host, headers)
			if cred != nil {
				u.rateTracker.SetAuth(host, true)
			}
			u.log.Debug("probed rate limits", "registry", host)
		}
	}

	// Filter out Swarm task containers — their updates are handled by scanServices
	// at the service level. Without this filter, task containers get queued under
	// names like "nginx.2.abc123" instead of the service name "nginx".
	filtered := containers[:0]
	for _, c := range containers {
		if _, isTask := c.Labels["com.docker.swarm.task"]; isTask {
			continue
		}
		filtered = append(filtered, c)
	}
	containers = filtered

	// Check Swarm mode and cache the services list once per scan,
	// avoiding duplicate IsSwarmManager + ListServices API calls.
	isSwarm := u.docker.IsSwarmManager(ctx)
	u.log.Debug("swarm check", "isSwarm", isSwarm)
	var swarmServices []swarm.Service
	if isSwarm {
		var svcErr error
		swarmServices, svcErr = u.docker.ListServices(ctx)
		if svcErr != nil {
			u.log.Error("failed to list services", "error", svcErr)
			result.Errors = append(result.Errors, svcErr)
		}
		u.log.Debug("swarm services listed", "count", len(swarmServices))
	}

	// Prune queue entries for containers/services that no longer exist.
	// Include local containers, Swarm services, and remote/Portainer containers
	// so their queue entries (keyed as "hostID::name") are not incorrectly pruned.
	liveNames := make(map[string]bool, len(containers))
	for _, c := range containers {
		liveNames[containerName(c)] = true
	}
	for _, svc := range swarmServices {
		liveNames[svc.Spec.Name] = true
	}
	// Add remote cluster container keys (hostID::name format).
	if u.cluster != nil {
		for _, hostID := range u.cluster.ConnectedHosts() {
			remoteContainers, err := u.cluster.ListContainers(ctx, hostID)
			if err != nil {
				u.log.Debug("prune: failed to list remote containers", "host", hostID, "error", err)
				continue
			}
			for _, rc := range remoteContainers {
				liveNames[store.ScopedKey(hostID, rc.Name)] = true
			}
		}
	}
	// Add Portainer container keys (portainer:<instanceID>:<endpointID>::name format).
	for i := range u.portainerInstances {
		inst := &u.portainerInstances[i]
		endpoints, epErr := inst.Scanner.Endpoints(ctx)
		if epErr != nil {
			continue
		}
		for _, ep := range endpoints {
			if cfg, ok := inst.Endpoints[ep.ID]; ok && (cfg.Blocked || !cfg.Enabled) {
				continue
			}
			epContainers, ecErr := inst.Scanner.EndpointContainers(ctx, ep.ID)
			if ecErr != nil {
				continue
			}
			hostID := fmt.Sprintf("portainer:%s:%d", inst.ID, ep.ID)
			for _, pc := range epContainers {
				liveNames[store.ScopedKey(hostID, pc.Name)] = true
			}
		}
	}
	if pruned := u.queue.Prune(liveNames); pruned > 0 {
		u.log.Info("pruned stale queue entries", "count", pruned)
	}

	// Publish scan start event so the UI can show a progress bar.
	u.publishEvent(events.EventScanStart, "", fmt.Sprintf("total=%d", len(containers)))

	// Load filter patterns once per scan.
	filters := u.loadFilters()

	// Rate limit headroom depends on scan mode.
	reserve := 10
	if mode == ScanManual {
		reserve = 2
	}

	for i, c := range containers {
		if ctx.Err() != nil {
			return result
		}

		name := containerName(c)
		u.publishEvent(events.EventScanProgress, name, fmt.Sprintf("checked=%d total=%d", i+1, len(containers)))
		labels := c.Labels
		tag := registry.ExtractTag(c.Image)
		resolved := ResolvePolicy(u.store, labels, name, tag, u.cfg.DefaultPolicy(), u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		// Skip pinned containers.
		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned container", "name", name)
			result.Skipped++
			continue
		}

		// Sentinel is checked for updates but never auto-updated via the scan loop.
		selfContainer := isSentinel(labels)

		// Skip containers matching filter patterns.
		if MatchesFilter(name, filters) {
			u.log.Debug("skipping filtered container", "name", name)
			result.Skipped++
			continue
		}

		// Per-container schedule check.
		if sched := docker.ContainerSchedule(labels); sched != "" {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			schedule, err := parser.Parse(sched)
			if err != nil {
				u.log.Warn("invalid schedule", "name", name, "schedule", sched, "error", err)
			} else {
				lastChecked, _ := u.store.GetLastContainerScan(name)
				if !lastChecked.IsZero() && u.clock.Now().Before(schedule.Next(lastChecked)) {
					result.Skipped++
					continue
				}
			}
		}

		// Rate limit check: skip if registry quota is too low.
		// Continue to next container — other registries may still be available.
		imageRef := c.Image
		if u.rateTracker != nil {
			host := registry.RegistryHost(imageRef)
			canProceed, wait := u.rateTracker.CanProceed(host, reserve)
			if !canProceed {
				u.log.Debug("rate limit low, skipping container", "name", name, "registry", host, "resets_in", wait)
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: name,
					OldImage:      imageRef,
					Outcome:       "rate_limited",
					Error:         "rate limit low on " + host,
				})
				result.RateLimited++
				continue
			}
		}

		// Check the registry for an update (versioned check also finds newer semver tags).
		semverScope := docker.ContainerSemverScope(labels)
		includeRE, excludeRE := docker.ContainerTagFilters(labels)
		check := u.checker.CheckVersioned(ctx, imageRef, semverScope, includeRE, excludeRE)

		if check.Error != nil {
			u.log.Warn("registry check failed", "name", name, "image", imageRef, "error", check.Error)
			_ = u.store.RecordUpdate(store.UpdateRecord{
				Timestamp:     u.clock.Now(),
				ContainerName: name,
				OldImage:      imageRef,
				Outcome:       "check_failed",
				Error:         check.Error.Error(),
			})
			result.Errors = append(result.Errors, fmt.Errorf("%s: %w", name, check.Error))
			continue
		}

		if check.IsLocal {
			u.log.Debug("local/unresolvable image, skipping", "name", name, "image", imageRef)
			result.Skipped++
			continue
		}

		// Record scan time for per-container schedule tracking.
		_ = u.store.SetLastContainerScan(name, u.clock.Now())

		if !check.UpdateAvailable {
			// Prune stale queue entries: if this container is in the queue
			// but the registry now reports it as up-to-date, remove it.
			if _, queued := u.queue.Get(name); queued {
				u.queue.Remove(name)
				u.log.Info("removed stale queue entry (now up to date)", "name", name)
			}
			u.log.Debug("up to date", "name", name, "image", imageRef)
			result.UpToDate++
			continue
		}

		// Enrich existing queue entries that lack resolved version data
		// (e.g. entries created before version resolution was added).
		if existing, queued := u.queue.Get(name); queued &&
			existing.ResolvedCurrentVersion == "" && existing.ResolvedTargetVersion == "" &&
			(check.ResolvedCurrentVersion != "" || check.ResolvedTargetVersion != "") {
			existing.ResolvedCurrentVersion = check.ResolvedCurrentVersion
			existing.ResolvedTargetVersion = check.ResolvedTargetVersion
			if len(check.NewerVersions) > 0 && len(existing.NewerVersions) == 0 {
				existing.NewerVersions = check.NewerVersions
			}
			u.queue.Add(existing)
			u.log.Info("enriched queue entry with resolved versions", "name", name,
				"current", check.ResolvedCurrentVersion, "target", check.ResolvedTargetVersion)
		}

		// Filter out ignored versions so they don't trigger notifications or queuing.
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
					u.log.Debug("all newer versions ignored", "name", name, "ignored", ignored)
					continue
				}
				check.NewerVersions = filtered
			}
		}

		u.log.Info("update available", "name", name, "image", imageRef,
			"local_digest", check.LocalDigest, "remote_digest", check.RemoteDigest)
		u.publishEvent(events.EventContainerUpdate, name, "update available")

		// Notification dedup: skip if we already notified about this exact digest.
		shouldNotify := true
		notifyMode := u.effectiveNotifyMode(name)
		switch notifyMode {
		case "muted":
			shouldNotify = false
		case "digest_only":
			shouldNotify = false // digest scheduler handles it
		default:
			state, _ := u.store.GetNotifyState(name)
			if state != nil && state.LastDigest == check.RemoteDigest && !state.LastNotified.IsZero() {
				if !state.SnoozedUntil.IsZero() && u.clock.Now().Before(state.SnoozedUntil) {
					shouldNotify = false
					u.log.Debug("notification snoozed", "name", name, "until", state.SnoozedUntil)
				} else if state.SnoozedUntil.IsZero() {
					// No snooze configured: suppress forever for same digest.
					shouldNotify = false
					u.log.Debug("skipping duplicate notification", "name", name, "digest", check.RemoteDigest)
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

		// Track notify state for digest compilation.
		// Only mark LastNotified when notification was actually delivered,
		// so failed deliveries get retried on the next scan.
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
		snoozeDur := docker.ContainerNotifySnooze(c.Labels)
		var snoozedUntil time.Time
		if snoozeDur > 0 && notifyOK {
			snoozedUntil = now.Add(snoozeDur)
		} else if existing != nil && !existing.SnoozedUntil.IsZero() {
			// Preserve existing snooze when not sending a new notification.
			snoozedUntil = existing.SnoozedUntil
		}
		if err := u.store.SetNotifyState(name, &store.NotifyState{
			LastDigest:   check.RemoteDigest,
			LastNotified: lastNotified,
			FirstSeen:    firstSeen,
			SnoozedUntil: snoozedUntil,
		}); err != nil {
			u.log.Warn("failed to persist notify state", "name", name, "error", err)
		}

		// Build target image for semver version bumps.
		scanTarget := ""
		if len(check.NewerVersions) > 0 {
			scanTarget = replaceTag(imageRef, check.NewerVersions[0])
		}

		// Sentinel is always queued (never auto-updated via scan).
		if selfContainer {
			u.queue.Add(PendingUpdate{
				ContainerID:            c.ID,
				ContainerName:          name,
				CurrentImage:           imageRef,
				CurrentDigest:          check.LocalDigest,
				RemoteDigest:           check.RemoteDigest,
				DetectedAt:             u.clock.Now(),
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
			})
			u.selfUpdateQueued.Store(true)
			u.selfUpdateKey.Store(name) // queue key for local self-container is just the name
			u.log.Info("sentinel update detected, queued for manual action", "name", name)
			result.Queued++
			continue
		}

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++
			if u.isDryRun() {
				u.log.Info("dry-run: would update", "name", name, "target", scanTarget)
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: name,
					OldImage:      imageRef,
					NewImage:      scanTarget,
					Outcome:       "dry_run",
				})
				continue
			}
			pullOnly := docker.ContainerPullOnly(labels) || u.isPullOnly()
			if pullOnly {
				target := scanTarget
				if target == "" {
					target = imageRef
				}
				if err := u.docker.PullImage(ctx, target); err != nil {
					u.log.Error("pull-only failed", "name", name, "error", err)
					result.Failed++
					result.Errors = append(result.Errors, fmt.Errorf("%s: pull-only: %w", name, err))
					continue
				}
				_ = u.store.RecordUpdate(store.UpdateRecord{
					Timestamp:     u.clock.Now(),
					ContainerName: name,
					OldImage:      imageRef,
					NewImage:      target,
					Outcome:       "pull_only",
				})
				result.Updated++
				continue
			}
			// Delay check: skip update if the update hasn't been seen long enough.
			delay := docker.ContainerUpdateDelay(labels)
			if delay == 0 {
				delay = u.globalUpdateDelay()
			}
			if delay > 0 {
				state, _ := u.store.GetNotifyState(name)
				if state != nil && !state.FirstSeen.IsZero() {
					age := u.clock.Now().Sub(state.FirstSeen)
					if age < delay {
						u.log.Info("update delayed", "name", name,
							"age", age.Round(time.Minute), "required", delay)
						result.Skipped++
						continue
					}
				} else {
					u.log.Info("update delay: first detection, waiting", "name", name, "delay", delay)
					result.Skipped++
					continue
				}
			}
			// Maintenance window check: skip auto-update if outside window.
			if windowExpr := u.maintenanceWindow(); windowExpr != "" {
				win, err := ParseWindow(windowExpr)
				if err != nil {
					u.log.Warn("invalid maintenance window, proceeding with update (fail-open)", "name", name, "window", windowExpr, "error", err)
				} else if win != nil && !win.IsOpen(u.clock.Now()) {
					u.log.Info("outside maintenance window, deferring auto-update", "name", name, "window", windowExpr)
					result.Skipped++
					continue
				}
			}
			if err := u.UpdateContainer(ctx, c.ID, name, scanTarget); err != nil {
				u.log.Error("auto-update failed", "name", name, "error", err)
				result.Failed++
				result.Errors = append(result.Errors, err)
			} else {
				result.Updated++
			}

		case docker.PolicyManual:
			u.queue.Add(PendingUpdate{
				ContainerID:            c.ID,
				ContainerName:          name,
				CurrentImage:           imageRef,
				CurrentDigest:          check.LocalDigest,
				RemoteDigest:           check.RemoteDigest,
				DetectedAt:             u.clock.Now(),
				NewerVersions:          check.NewerVersions,
				ResolvedCurrentVersion: check.ResolvedCurrentVersion,
				ResolvedTargetVersion:  check.ResolvedTargetVersion,
			})
			u.log.Info("update queued for manual approval", "name", name)
			u.publishEvent(events.EventQueueChange, name, "queued for approval")
			result.Queued++
		}
	}

	// Scan Swarm services using the pre-fetched list.
	if isSwarm && len(swarmServices) > 0 {
		u.scanServices(ctx, swarmServices, mode, &result, filters, reserve)
	}

	// Scan remote hosts if cluster mode is active.
	if u.cluster != nil {
		u.scanRemoteHosts(ctx, mode, &result, filters, reserve)
	}

	if len(u.portainerInstances) > 0 {
		// Collect local container IDs so the Portainer scan can skip containers
		// that Sentinel already monitors via the local Docker socket.
		localIDs := make(map[string]bool, len(containers))
		for _, c := range containers {
			localIDs[c.ID] = true
		}
		u.scanPortainerInstances(ctx, mode, &result, filters, reserve, localIDs)
	}

	u.publishEvent(events.EventScanComplete, "", fmt.Sprintf(
		"total=%d updated=%d queued=%d skipped=%d rate_limited=%d failed=%d services=%d",
		result.Total, result.Updated, result.Queued, result.Skipped, result.RateLimited, result.Failed, result.ServiceUpdates))

	if u.rateTracker != nil {
		u.publishEvent(events.EventRateLimits, "", u.rateTracker.OverallHealth())
		// Persist rate limit state to DB after each scan.
		if u.rateSaver != nil {
			if data, err := u.rateTracker.Export(); err == nil {
				if err := u.rateSaver(data); err != nil {
					u.log.Warn("failed to persist rate limits", "error", err)
				}
			}
		}
	}

	// Launch background GHCR alternative check for Docker Hub containers.
	// Uses a detached context so the goroutine isn't cancelled when the
	// scan context expires. Tracked by WaitGroup for clean shutdown.
	if u.ghcrCache != nil && u.ghcrRunning.CompareAndSwap(false, true) {
		ghcrCtx, ghcrCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		u.ghcrCancel = ghcrCancel
		u.ghcrWg.Add(1)
		go func() {
			defer u.ghcrWg.Done()
			defer u.ghcrRunning.Store(false)
			defer ghcrCancel()
			u.checkGHCRAlternatives(ghcrCtx, containers)
		}()
	}

	metrics.ScansTotal.Inc()
	metrics.ContainersTotal.Set(float64(result.Total))
	metrics.ContainersMonitored.Set(float64(result.Total - result.Skipped))
	metrics.PendingUpdates.Set(float64(result.Queued))
	metrics.ScanDuration.Observe(time.Since(scanStart).Seconds())

	// Record scan summary in history.
	_ = u.store.RecordUpdate(store.UpdateRecord{
		Timestamp: u.clock.Now(),
		Outcome:   "scan_summary",
		Type:      "scan",
		Error: fmt.Sprintf("%d checked, %d up to date, %d updated, %d queued, %d skipped, %d failed",
			result.Total-result.Skipped, result.UpToDate, result.Updated, result.Queued, result.RateLimited, result.Failed),
		Duration: time.Since(scanStart),
	})

	// Publish HA discovery states after scan.
	if u.haDiscovery != nil {
		if err := u.haDiscovery.PublishPendingCount(u.queue.Len()); err != nil {
			u.log.Debug("ha discovery: failed to publish pending count", "error", err)
		}
		for _, item := range u.queue.List() {
			if item.HostID != "" {
				continue // only publish local containers
			}
			if err := u.haDiscovery.PublishContainerState(item.ContainerName, true); err != nil {
				u.log.Debug("ha discovery: failed to publish state", "name", item.ContainerName, "error", err)
			}
		}
	}

	return result
}
