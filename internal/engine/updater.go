package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/deps"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/swarm"
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
	hooks       *hooks.Runner              // optional: lifecycle hook runner
	deps        *deps.Graph                // optional: dependency graph (rebuilt each scan)
	cluster     ClusterScanner             // optional: nil = single-host mode
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

// SetHookRunner attaches a lifecycle hook runner.
func (u *Updater) SetHookRunner(r *hooks.Runner) {
	u.hooks = r
}

// SetClusterScanner attaches a cluster scanner for multi-host scanning.
// When nil (the default), remote host scanning is skipped entirely.
func (u *Updater) SetClusterScanner(cs ClusterScanner) {
	u.cluster = cs
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
	var swarmServices []swarm.Service
	if isSwarm {
		var svcErr error
		swarmServices, svcErr = u.docker.ListServices(ctx)
		if svcErr != nil {
			u.log.Error("failed to list services", "error", svcErr)
			result.Errors = append(result.Errors, svcErr)
		}
	}

	// Prune queue entries for containers/services that no longer exist.
	liveNames := make(map[string]bool, len(containers))
	for _, c := range containers {
		liveNames[containerName(c)] = true
	}
	for _, svc := range swarmServices {
		liveNames[svc.Spec.Name] = true
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
		if err := u.store.SetNotifyState(name, &store.NotifyState{
			LastDigest:   check.RemoteDigest,
			LastNotified: lastNotified,
			FirstSeen:    firstSeen,
		}); err != nil {
			u.log.Warn("failed to persist notify state", "name", name, "error", err)
		}

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

	// Scan Swarm services using the pre-fetched list.
	if isSwarm && len(swarmServices) > 0 {
		u.scanServices(ctx, swarmServices, mode, &result, filters, reserve)
	}

	// Scan remote hosts if cluster mode is active.
	if u.cluster != nil {
		u.scanRemoteHosts(ctx, mode, &result, filters, reserve)
	}

	u.publishEvent(events.EventScanComplete, "", fmt.Sprintf("total=%d updated=%d services=%d", result.Total, result.Updated, result.ServiceUpdates))

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

	metrics.ScansTotal.Inc()
	metrics.ContainersTotal.Set(float64(result.Total))
	metrics.ContainersMonitored.Set(float64(result.Total - result.Skipped))
	metrics.PendingUpdates.Set(float64(result.Queued))

	return result
}

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

	for _, c := range containers {
		if ctx.Err() != nil {
			return
		}

		result.Total++

		// Skip based on policy (same resolution as local containers).
		tag := registry.ExtractTag(c.Image)
		resolved := ResolvePolicy(u.store, c.Labels, c.Name, tag, u.cfg.DefaultPolicy(), u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned remote container", "host", host.HostName, "name", c.Name)
			result.Skipped++
			continue
		}

		// Skip sentinel self-update on remote hosts too.
		if isSentinel(c.Labels) {
			u.log.Debug("skipping sentinel on remote host", "host", host.HostName, "name", c.Name)
			result.Skipped++
			continue
		}

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
		check := u.checker.CheckVersionedWithDigest(ctx, c.Image, c.ImageDigest)
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

		switch policy {
		case docker.PolicyAuto:
			result.AutoCount++

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
