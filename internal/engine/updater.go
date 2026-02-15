package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/guardian"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

// ErrUpdateInProgress is returned when an update is attempted on a container
// that already has an update in progress.
var ErrUpdateInProgress = fmt.Errorf("update already in progress")

// finaliseError wraps an error with the stage at which finaliseContainer failed.
// Stage values: "inspect", "stop", "remove", "create", "start".
type finaliseError struct {
	stage string
	err   error
}

func (e *finaliseError) Error() string { return fmt.Sprintf("finalise %s: %v", e.stage, e.err) }
func (e *finaliseError) Unwrap() error { return e.err }

// finaliseStageIsDestructive returns true if the failure stage means the
// container was already removed and is likely down.
func finaliseStageIsDestructive(stage string) bool {
	return stage == "remove" || stage == "create" || stage == "start"
}

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
	Errors      []error
}

// Updater performs container scanning and update operations.
type Updater struct {
	docker      docker.API
	checker     *registry.Checker
	store       *store.Store
	queue       *Queue
	cfg         *config.Config
	log         *logging.Logger
	clock       clock.Clock
	notifier    *notify.Multi
	events      *events.Bus
	settings    SettingsReader
	rateTracker *registry.RateLimitTracker // optional: rate limit awareness
	rateSaver   func([]byte) error         // optional: persist rate limits after scan
	ghcrCache   *registry.GHCRCache        // optional: GHCR alternative detection cache
	ghcrSaver   func([]byte) error         // optional: persist GHCR cache after checks
	updating    sync.Map                   // map[string]*sync.Mutex — per-container update locks
}

// NewUpdater creates an Updater with all dependencies.
func NewUpdater(d docker.API, checker *registry.Checker, s *store.Store, q *Queue, cfg *config.Config, log *logging.Logger, clk clock.Clock, notifier *notify.Multi, bus *events.Bus) *Updater {
	return &Updater{
		docker:   d,
		checker:  checker,
		store:    s,
		queue:    q,
		cfg:      cfg,
		log:      log,
		clock:    clk,
		notifier: notifier,
		events:   bus,
	}
}

// SetSettingsReader attaches a settings reader for runtime filter checks.
func (u *Updater) SetSettingsReader(sr SettingsReader) {
	u.settings = sr
}

// SetRateLimitTracker attaches a rate limit tracker for scan pacing.
func (u *Updater) SetRateLimitTracker(t *registry.RateLimitTracker) {
	u.rateTracker = t
}

// SetRateLimitSaver attaches a function to persist rate limits after each scan.
func (u *Updater) SetRateLimitSaver(fn func([]byte) error) {
	u.rateSaver = fn
}

// SetGHCRCache attaches a GHCR alternative detection cache.
func (u *Updater) SetGHCRCache(c *registry.GHCRCache) {
	u.ghcrCache = c
}

// SetGHCRSaver attaches a function to persist the GHCR cache after checks.
func (u *Updater) SetGHCRSaver(fn func([]byte) error) {
	u.ghcrSaver = fn
}

// tryLock attempts to acquire the per-container update lock.
// Returns false if the container already has an update in progress.
func (u *Updater) tryLock(name string) bool {
	mu := &sync.Mutex{}
	actual, _ := u.updating.LoadOrStore(name, mu)
	return actual.(*sync.Mutex).TryLock()
}

// unlock releases the per-container update lock and removes the entry
// from the map to prevent stale mutex accumulation. This is safe because
// tryLock uses LoadOrStore (atomic) and the per-container lock ensures
// only one goroutine holds the lock at a time.
func (u *Updater) unlock(name string) {
	if val, ok := u.updating.Load(name); ok {
		val.(*sync.Mutex).Unlock()
		u.updating.Delete(name)
	}
}

// IsUpdating reports whether a container currently has an update in progress.
func (u *Updater) IsUpdating(name string) bool {
	val, ok := u.updating.Load(name)
	if !ok {
		return false
	}
	mu := val.(*sync.Mutex)
	if mu.TryLock() {
		mu.Unlock()
		return false
	}
	return true
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
func (u *Updater) publishEvent(evtType events.EventType, name, message string) {
	if u.events == nil {
		return
	}
	u.events.Publish(events.SSEEvent{
		Type:          evtType,
		ContainerName: name,
		Message:       message,
		Timestamp:     u.clock.Now(),
	})
}

// Scan lists running containers, checks for updates, and processes them
// according to each container's policy. The mode controls rate limit headroom.
func (u *Updater) Scan(ctx context.Context, mode ScanMode) ScanResult {
	result := ScanResult{}

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

	// Prune queue entries for containers that no longer exist.
	liveNames := make(map[string]bool, len(containers))
	for _, c := range containers {
		liveNames[containerName(c)] = true
	}
	if pruned := u.queue.Prune(liveNames); pruned > 0 {
		u.log.Info("pruned stale queue entries", "count", pruned)
	}

	// Load filter patterns once per scan.
	filters := u.loadFilters()

	// Rate limit headroom depends on scan mode.
	reserve := 10
	if mode == ScanManual {
		reserve = 2
	}

	for _, c := range containers {
		if ctx.Err() != nil {
			return result
		}

		name := containerName(c)
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

		// Skip Sentinel itself (avoid self-update loops).
		if isSentinel(labels) {
			u.log.Debug("skipping sentinel container", "name", name)
			result.Skipped++
			continue
		}

		// Skip containers matching filter patterns.
		if MatchesFilter(name, filters) {
			u.log.Debug("skipping filtered container", "name", name)
			result.Skipped++
			continue
		}

		// Rate limit check: skip if registry quota is too low.
		imageRef := c.Image
		if u.rateTracker != nil {
			host := registry.RegistryHost(imageRef)
			canProceed, wait := u.rateTracker.CanProceed(host, reserve)
			if !canProceed {
				if mode == ScanManual {
					u.log.Warn("rate limit exhausted, stopping manual scan", "registry", host, "resets_in", wait)
					result.RateLimited++
					break // manual scan: stop entirely
				}
				u.log.Debug("rate limit low, skipping container", "name", name, "registry", host, "resets_in", wait)
				result.RateLimited++
				continue // scheduled scan: skip silently
			}
		}

		// Check the registry for an update (versioned check also finds newer semver tags).
		check := u.checker.CheckVersioned(ctx, imageRef)

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
			// Prune stale queue entries: if this container is in the queue
			// but the registry now reports it as up-to-date, remove it.
			if _, queued := u.queue.Get(name); queued {
				u.queue.Remove(name)
				u.log.Info("removed stale queue entry (now up to date)", "name", name)
			}
			u.log.Debug("up to date", "name", name, "image", imageRef)
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
				shouldNotify = false
				u.log.Debug("skipping duplicate notification", "name", name, "digest", check.RemoteDigest)
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
		_ = u.store.SetNotifyState(name, &store.NotifyState{
			LastDigest:   check.RemoteDigest,
			LastNotified: lastNotified,
			FirstSeen:    firstSeen,
		})

		// Build target image for semver version bumps.
		scanTarget := ""
		if len(check.NewerVersions) > 0 {
			scanTarget = replaceTag(imageRef, check.NewerVersions[0])
		}

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++
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

	u.publishEvent(events.EventScanComplete, "", fmt.Sprintf("total=%d updated=%d", result.Total, result.Updated))

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
	// Use a detached context so the goroutine isn't cancelled when the
	// scan context expires (the caller may cancel it after Scan returns).
	if u.ghcrCache != nil {
		ghcrCtx, ghcrCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		go func() {
			defer ghcrCancel()
			u.checkGHCRAlternatives(ghcrCtx, containers)
		}()
	}

	return result
}

// checkGHCRAlternatives probes GHCR for alternatives to Docker Hub images.
// Runs as a background goroutine after each scan. Skips images that already
// have a valid (non-expired) cache entry.
func (u *Updater) checkGHCRAlternatives(ctx context.Context, containers []container.Summary) {
	// Check if GHCR detection is enabled (default: true).
	if u.settings != nil {
		val, _ := u.settings.LoadSetting("ghcr_check_enabled")
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
		_ = u.docker.StopContainer(ctx, newID, 10)
		_ = u.docker.RemoveContainer(ctx, newID)
		u.doRollback(ctx, name, snapshotData, start)
		return fmt.Errorf("new container %s failed validation", name)
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
