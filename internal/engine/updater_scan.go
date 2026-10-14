package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Will-Luck/Docker-Sentinel/internal/deps"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/metrics"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/moby/moby/api/types/container"
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
	CheckFailed int // containers whose registry check failed
	Errors      []error

	// Swarm service stats (only populated when Swarm mode is active).
	Services       int
	ServiceUpdates int
}

// counted is the number of containers and services the scan has put in a
// count. Each lands in exactly one, so Total is this sum by construction
// and "checked" (Total less Skipped) is the sum of the other counts.
func (r *ScanResult) counted() int {
	return r.Skipped + r.UpToDate + r.Updated + r.Queued + r.RateLimited + r.Failed + r.CheckFailed
}

// scanTally keeps a scan loop to one count per container (or service):
// next marks the start of an iteration, done closes the last one. An
// iteration that reached no count is counted as skipped.
type scanTally struct {
	r    *ScanResult
	mark int
	open bool
}

func (t *scanTally) next() {
	t.done()
	t.mark, t.open = t.r.counted(), true
}

func (t *scanTally) done() {
	if t.open && t.r.counted() == t.mark {
		t.r.Skipped++
	}
	t.open = false
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

// isImageIDRef reports whether an image reference is a bare image ID rather
// than a repository reference. Docker reports a container's image as its ID
// once the tag it was started from points at a different image.
func isImageIDRef(ref, imageID string) bool {
	return strings.HasPrefix(ref, "sha256:") || (imageID != "" && ref == imageID)
}

// resolveImageRefs replaces image-ID references in the container list with the
// reference each container was configured with, so a container whose tag has
// moved is still checked against the registry instead of being treated as a
// local image. The input slice is not modified.
func (u *Updater) resolveImageRefs(ctx context.Context, containers []container.Summary) []container.Summary {
	out := make([]container.Summary, len(containers))
	copy(out, containers)
	for i := range out {
		c := &out[i]
		if !isImageIDRef(c.Image, c.ImageID) {
			continue
		}
		inspect, err := u.docker.InspectContainer(ctx, c.ID)
		if err != nil || inspect.Config == nil || inspect.Config.Image == "" || isImageIDRef(inspect.Config.Image, c.ImageID) {
			u.log.Debug("could not resolve configured image for image-ID reference", "container", containerName(*c), "error", err)
			continue
		}
		c.Image = inspect.Config.Image
	}
	return out
}

// containerBehindTag reports whether the container runs a different image from
// the one imageRef currently resolves to in the local image store.
func (u *Updater) containerBehindTag(ctx context.Context, c container.Summary, imageRef string) bool {
	if c.ImageID == "" {
		return false
	}
	tagID, err := u.docker.ImageID(ctx, imageRef)
	if err != nil || tagID == "" {
		return false
	}
	return strings.TrimPrefix(tagID, "sha256:") != strings.TrimPrefix(c.ImageID, "sha256:")
}

// resolveBehindTag fills in the versions of an update found because a
// container on a mutable tag runs an older image than the tag: the
// registry check matched the tag, so it resolved none. They are resolved
// from the container's own image digest, as for a remote container.
func (u *Updater) resolveBehindTag(ctx context.Context, c container.Summary, imageRef string, check *registry.CheckResult, scope docker.SemverScope, include, exclude string) {
	if u.resolveVersions == nil {
		return
	}
	if _, semver := registry.ParseSemVer(registry.ExtractTag(imageRef)); semver {
		return // a versioned tag already has its newer versions
	}
	r, ok := u.docker.(docker.RepoDigestReader)
	if !ok {
		return
	}
	digests, err := r.ImageRepoDigests(ctx, c.ImageID)
	if err != nil {
		return
	}
	repo := repoKey(imageRef)
	for _, d := range digests {
		if name, _, ok := strings.Cut(d, "@"); ok && repoKey(name) == repo {
			res := u.resolveVersions(ctx, imageRef, d, scope, include, exclude)
			check.ResolvedCurrentVersion, check.ResolvedTargetVersion = res.ResolvedCurrentVersion, res.ResolvedTargetVersion
			return
		}
	}
}

// ignoreKey returns the key under which an update with no version list (a
// container on a mutable tag such as latest) is ignored: the version the tag
// resolves to when known, otherwise the remote digest ("sha256:..."). A new
// release or digest gives a new key, so an ignore covers one update only.
func ignoreKey(check registry.CheckResult) string {
	if check.ResolvedTargetVersion != "" {
		return check.ResolvedTargetVersion
	}
	return check.RemoteDigest
}

// failedUpdate describes the update a check offers, to record when it is
// rolled back: the target version and its registry digest for a version
// bump (imageRef is the container's image), otherwise the resolved version
// and remote digest of a mutable tag.
func (u *Updater) failedUpdate(ctx context.Context, imageRef string, check registry.CheckResult) store.FailedUpdate {
	if len(check.NewerVersions) > 0 {
		digest, _ := u.docker.DistributionDigest(ctx, replaceTag(imageRef, check.NewerVersions[0]))
		return store.FailedUpdate{Version: check.NewerVersions[0], Digest: digest}
	}
	return store.FailedUpdate{Version: check.ResolvedTargetVersion, Digest: check.RemoteDigest}
}

// rolledBackBefore reports whether the update a check offers is the one
// recorded as rolled back. It matches on the image digest, so a fixed build
// pushed again under the same version is tried. For a version bump whose
// digest cannot be looked up, the version decides.
func (u *Updater) rolledBackBefore(ctx context.Context, name, imageRef string, check registry.CheckResult) bool {
	failed, found, _ := u.store.GetFailedUpdate(name)
	if !found {
		return false
	}
	if len(check.NewerVersions) == 0 {
		return failed.Digest != "" && failed.Digest == check.RemoteDigest
	}
	if failed.Version != check.NewerVersions[0] {
		return false
	}
	if failed.Digest == "" {
		return true
	}
	digest, err := u.docker.DistributionDigest(ctx, replaceTag(imageRef, check.NewerVersions[0]))
	return err != nil || digest == "" || digest == failed.Digest
}

// pruneFailedUpdates forgets the rolled-back update of a container that no
// longer exists, so a new container of the same name starts afresh.
func (u *Updater) pruneFailedUpdates(ctx context.Context) {
	names, err := u.store.ListFailedUpdates()
	if err != nil || len(names) == 0 {
		return
	}
	all, err := u.docker.ListAllContainers(ctx)
	if err != nil {
		return
	}
	present := make(map[string]bool, len(all))
	for _, c := range all {
		present[containerName(c)] = true
	}
	for _, name := range names {
		if !present[name] {
			_ = u.store.ClearFailedUpdate(name)
		}
	}
}

// pruneNotifyStates forgets the "update available" state of local
// containers and services that no longer exist, so they leave the digest.
// Remote containers ("host::name") are left to the cluster. Nothing is
// pruned when the containers, or the services of a Swarm manager, could
// not be listed.
func (u *Updater) pruneNotifyStates(ctx context.Context, isSwarm bool, services []swarm.Service, servicesListed bool) {
	states, err := u.store.AllNotifyStates()
	if err != nil || len(states) == 0 || (isSwarm && !servicesListed) {
		return
	}
	all, err := u.docker.ListAllContainers(ctx)
	if err != nil {
		return
	}
	present := make(map[string]bool, len(all)+len(services))
	for _, c := range all {
		present[containerName(c)] = true
	}
	for _, svc := range services {
		present[svc.Spec.Name] = true
	}
	for name := range states {
		if !present[name] && !strings.Contains(name, "::") {
			_ = u.store.ClearNotifyState(name)
		}
	}
}

// pruneRemoteNotifyStates forgets the "update available" state of remote
// containers that are gone, so the digest stops listing them. Like the queue
// it judges a host only when the host listed its containers this scan; a
// host that is merely offline keeps its states, one that left the cluster
// loses them.
func (u *Updater) pruneRemoteNotifyStates(liveNames, listedHosts map[string]bool) {
	states, err := u.store.AllNotifyStates()
	if err != nil {
		return
	}
	for key := range states {
		hostID, _, remote := strings.Cut(key, "::")
		if !remote {
			continue
		}
		switch {
		case listedHosts[hostID]:
			if liveNames[key] {
				continue
			}
		case u.cluster != nil:
			if _, ok := u.cluster.HostInfo(hostID); ok {
				continue
			}
		}
		_ = u.store.ClearNotifyState(key)
	}
}

// availableTarget is the image an "update available" notification names as
// the new one: the newer tag for a version bump, otherwise the same tag (a
// digest-only update re-pulls it).
func availableTarget(imageRef string, check registry.CheckResult) string {
	if len(check.NewerVersions) > 0 {
		return replaceTag(imageRef, check.NewerVersions[0])
	}
	return imageRef
}

// filterIgnored drops ignored versions from check.NewerVersions and reports
// whether nothing is left to act on. Updates without a version list are
// checked against ignoreKey instead.
func (u *Updater) filterIgnored(name string, check *registry.CheckResult) (allIgnored bool) {
	allIgnored = u.filterIgnoredVersions(name, check)
	if allIgnored {
		// Nothing is pending any more, so it leaves the digest with the queue.
		_ = u.store.ClearNotifyState(name)
	}
	return allIgnored
}

func (u *Updater) filterIgnoredVersions(name string, check *registry.CheckResult) (allIgnored bool) {
	ignored, _ := u.store.GetIgnoredVersions(name)
	if len(ignored) == 0 {
		return false
	}
	ignoredSet := make(map[string]bool, len(ignored))
	for _, v := range ignored {
		ignoredSet[v] = true
	}
	if len(check.NewerVersions) == 0 {
		key := ignoreKey(*check)
		return key != "" && ignoredSet[key]
	}
	var filtered []string
	for _, v := range check.NewerVersions {
		if !ignoredSet[v] {
			filtered = append(filtered, v)
		}
	}
	if len(filtered) == 0 {
		return true
	}
	check.NewerVersions = filtered
	return false
}

// Scan lists running containers, checks for updates, and processes them
// according to each container's policy. The mode controls rate limit headroom.
func (u *Updater) Scan(ctx context.Context, mode ScanMode) (result ScanResult) {
	// Total is what the scan counted, on every return.
	defer func() { result.Total = result.counted() }()
	scanStart := time.Now()
	u.selfUpdateQueued.Store(false)

	if c := u.scanConcurrency(); c > 1 {
		u.log.Info("scan concurrency enabled (experimental)", "concurrency", c)
	}

	// Retry the swaps an earlier update could not finish, so a passing
	// cause clears without a restart, and forget rolled-back updates of
	// containers that are gone.
	u.resolvePendingSwaps(ctx)
	u.pruneFailedUpdates(ctx)

	containers, err := u.docker.ListContainers(ctx)
	if err != nil {
		u.log.Error("failed to list containers", "error", err)
		result.Errors = append(result.Errors, err)
		return result
	}
	containers = u.resolveImageRefs(ctx, containers)

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
	svcListed := true
	if isSwarm {
		var svcErr error
		swarmServices, svcErr = u.docker.ListServices(ctx)
		if svcErr != nil {
			svcListed = false
			u.log.Error("failed to list services", "error", svcErr)
			result.Errors = append(result.Errors, svcErr)
		}
		u.log.Debug("swarm services listed", "count", len(swarmServices))
	}

	// Prune queue entries for containers/services that no longer exist.
	// Include local containers, Swarm services, and remote containers
	// so their queue entries (keyed as "hostID::name") are not incorrectly pruned.
	liveNames := make(map[string]bool, len(containers))
	for _, c := range containers {
		liveNames[containerName(c)] = true
	}
	for _, svc := range swarmServices {
		liveNames[svc.Spec.Name] = true
	}
	// Add remote cluster container keys (hostID::name format). A remote
	// entry can only be judged stale for a host that is connected and
	// listed its containers this scan. Entries of a host that is still in
	// the cluster but did not list (not yet reconnected after a hub
	// restart, partitioned, or whose list failed) are kept. Entries of a
	// host that left the cluster, and all remote entries when cluster mode
	// is off, are pruned.
	listedHosts := make(map[string]bool)
	if u.cluster != nil {
		for _, hostID := range u.cluster.ConnectedHosts() {
			remoteContainers, err := u.cluster.ListContainers(ctx, hostID)
			if err != nil {
				u.log.Debug("prune: failed to list remote containers", "host", hostID, "error", err)
				continue
			}
			listedHosts[hostID] = true
			for _, rc := range remoteContainers {
				liveNames[store.ScopedKey(hostID, rc.Name)] = true
			}
		}
	}
	if u.cluster != nil {
		for _, p := range u.queue.List() {
			if p.HostID == "" || listedHosts[p.HostID] {
				continue
			}
			if _, ok := u.cluster.HostInfo(p.HostID); ok {
				liveNames[p.Key()] = true
			}
		}
	}
	if pruned := u.queue.Prune(liveNames); pruned > 0 {
		u.log.Info("pruned stale queue entries", "count", pruned)
	}
	u.pruneRemoteNotifyStates(liveNames, listedHosts)

	// Publish scan start event so the UI can show a progress bar.
	u.publishEvent(events.EventScanStart, "", fmt.Sprintf("total=%d", len(containers)))

	// Load filter patterns once per scan.
	filters := u.loadFilters()

	// Rate limit headroom depends on scan mode.
	reserve := 10
	if mode == ScanManual {
		reserve = 2
	}

	// Update dependencies before their dependents.
	containers = u.orderByDependencies(containers)

	// Updates refused for want of cosign are notified once, together.
	defer func() { u.notifyUnverifiable(context.WithoutCancel(ctx), result.Errors) }()

	tally := scanTally{r: &result}
	for i, c := range containers {
		if ctx.Err() != nil {
			return result
		}
		tally.next()

		name := containerName(c)
		u.publishEvent(events.EventScanProgress, name, fmt.Sprintf("checked=%d total=%d", i+1, len(containers)))
		if u.setAside(name) || u.store.HasSwap(name) {
			u.log.Debug("skipping a container with a swap in progress or unresolved", "name", name)
			result.Skipped++
			continue
		}
		labels := c.Labels
		tag := registry.ExtractTag(c.Image)
		resolved := ResolvePolicy(u.store, labels, name, tag, u.cfg.DefaultPolicy(), u.cfg.LatestAutoUpdate())
		policy := docker.Policy(resolved.Policy)

		// Skip pinned containers.
		if policy == docker.PolicyPinned {
			u.log.Debug("skipping pinned container", "name", name)
			u.dropQueued(name, "pinned") // no scan looks at it again
			result.Skipped++
			continue
		}

		// Sentinel is checked for updates but never auto-updated via the scan loop.
		selfContainer := isSentinel(labels)

		// Skip containers matching filter patterns.
		if MatchesFilter(name, filters) {
			u.log.Debug("skipping filtered container", "name", name)
			u.dropQueued(name, "filtered out")
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
			result.CheckFailed++
			continue
		}

		if check.IsLocal {
			u.log.Debug("local/unresolvable image, skipping", "name", name, "image", imageRef)
			result.Skipped++
			continue
		}

		// Record scan time for per-container schedule tracking.
		_ = u.store.SetLastContainerScan(name, u.clock.Now())

		// The registry digest comparison looks at the tag, not at the image the
		// container runs. If the tag has since been pulled (a sibling container
		// on the same tag was updated, or someone ran docker pull) the digests
		// match while this container is still on the old image.
		behindTag := false
		if !check.UpdateAvailable && u.containerBehindTag(ctx, c, imageRef) {
			check.UpdateAvailable = true
			behindTag = true
			u.log.Info("container runs an older image than its tag", "name", name, "image", imageRef)
			u.resolveBehindTag(ctx, c, imageRef, &check, semverScope, includeRE, excludeRE)
		}

		// A pull-only container whose tag is already pulled has nothing left
		// for Sentinel to do. Skip it before anything is announced or counted.
		if behindTag && !selfContainer && policy == docker.PolicyAuto &&
			(docker.ContainerPullOnly(labels) || u.isPullOnly()) {
			u.log.Debug("pull-only: tag already pulled, nothing to fetch", "name", name, "image", imageRef)
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
			// Updated outside Sentinel: it no longer belongs in the digest.
			if st, _ := u.store.GetNotifyState(name); st != nil {
				_ = u.store.ClearNotifyState(name)
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
		if u.filterIgnored(name, &check) {
			u.log.Debug("all newer versions ignored", "name", name)
			result.UpToDate++
			continue
		}

		// Under auto policy, an update that was tried and rolled back is not
		// tried (or announced) again until a new version or digest appears.
		// Manual and pinned containers still see it.
		if policy == docker.PolicyAuto && u.rolledBackBefore(ctx, name, imageRef, check) {
			u.log.Debug("update was rolled back before, waiting for a newer one", "name", name)
			result.Skipped++
			continue
		}

		u.log.Info("update available", "name", name, "image", imageRef,
			"local_digest", check.LocalDigest, "remote_digest", check.RemoteDigest)
		u.publishEvent(events.EventContainerUpdate, name, "update available")

		// Notification dedup: skip if we already notified about this exact digest.
		notifyMode := u.effectiveNotifyMode(name)
		state, _ := u.store.GetNotifyState(name)
		shouldNotify := shouldNotifyUpdate(notifyMode, state, check.RemoteDigest, u.clock.Now())
		if !shouldNotify && notifyMode != "muted" && notifyMode != "digest_only" {
			u.log.Debug("skipping duplicate or snoozed notification", "name", name, "digest", check.RemoteDigest)
		}

		notifyOK := false
		if shouldNotify {
			notifyOK = u.notifier.Notify(ctx, notify.Event{
				Type:          notify.EventUpdateAvailable,
				ContainerName: name,
				OldImage:      imageRef,
				NewImage:      availableTarget(imageRef, check),
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
		lastNotified := carriedLastNotified(existing, check.RemoteDigest)
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
				result.Skipped++ // nothing is changed in a dry run
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
			// An update earlier in this scan may have recreated it (a
			// network namespace consumer of an updated provider).
			if cur, err := u.docker.InspectContainer(ctx, name); err == nil && cur.ID != "" {
				c.ID = cur.ID
			}
			err := u.updateContainer(ctx, c.ID, name, scanTarget)
			switch {
			case errors.Is(err, errSkippedByHook):
				result.Skipped++
			case errors.Is(err, errSameImage):
				result.UpToDate++
			case err != nil:
				u.log.Error("auto-update failed", "name", name, "error", err)
				result.Failed++
				result.Errors = append(result.Errors, err)
				if errors.Is(err, ErrRolledBack) {
					if f := u.failedUpdate(ctx, imageRef, check); f.Digest != "" || f.Version != "" {
						if err := u.store.SetFailedUpdate(name, f); err != nil {
							u.log.Warn("failed to record the rolled-back update", "name", name, "error", err)
						}
					}
				}
			default:
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
	tally.done()

	u.pruneNotifyStates(ctx, isSwarm, swarmServices, svcListed)

	if isSwarm && len(swarmServices) > 0 {
		u.scanServices(ctx, swarmServices, mode, &result, filters, reserve)
	}

	// Scan remote hosts if cluster mode is active.
	if u.cluster != nil {
		u.scanRemoteHosts(ctx, mode, &result, filters, reserve)
	}

	result.Total = result.counted()
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
		Error: fmt.Sprintf("%d checked, %d up to date, %d updated, %d queued, %d skipped, %d rate limited, %d failed, %d check failed",
			result.Total-result.Skipped, result.UpToDate, result.Updated, result.Queued, result.Skipped, result.RateLimited, result.Failed, result.CheckFailed),
		Duration: time.Since(scanStart),
	})

	// Publish HA discovery states after scan.
	if u.haDiscovery != nil {
		if err := u.haDiscovery.PublishPendingCount(u.queue.Len()); err != nil {
			u.log.Debug("ha discovery: failed to publish pending count", "error", err)
		}
		var pending []string
		for _, item := range u.queue.List() {
			if item.HostID != "" {
				continue // only publish local containers
			}
			pending = append(pending, item.ContainerName)
		}
		// Sensors for containers that left the queue are set back to OFF.
		if err := u.haDiscovery.SyncContainerStates(pending); err != nil {
			u.log.Debug("ha discovery: failed to publish states", "error", err)
		}
	}

	return result
}

// shouldNotifyUpdate decides whether an "update available" notification goes
// out now for a container in the given notification mode.
//
//   - muted: never; digest_only: left to the digest scheduler.
//   - every_scan: every scan that finds the update, however often it was
//     notified before.
//   - default: once per digest, then silent, unless a snooze was set and has
//     expired.
func shouldNotifyUpdate(mode string, state *store.NotifyState, remoteDigest string, now time.Time) bool {
	switch mode {
	case "muted", "digest_only":
		return false
	case "every_scan":
		return true
	}
	if state == nil || state.LastDigest != remoteDigest || state.LastNotified.IsZero() {
		return true
	}
	if state.SnoozedUntil.IsZero() {
		return false // no snooze configured: suppress the same digest for good
	}
	return !now.Before(state.SnoozedUntil)
}

// orderByDependencies orders containers so that each comes after the
// containers it depends on (sentinel.depends-on, compose depends_on, a
// container: network mode), so a scan updates a dependency before its
// dependents. A cycle is broken by name and logged.
func (u *Updater) orderByDependencies(containers []container.Summary) []container.Summary {
	infos := make([]deps.ContainerInfo, 0, len(containers))
	byName := make(map[string]container.Summary, len(containers))
	for _, c := range containers {
		name := containerName(c)
		byName[name] = c
		infos = append(infos, deps.ContainerInfo{ID: c.ID, Name: name, Labels: c.Labels, NetworkMode: c.HostConfig.NetworkMode})
	}
	if len(byName) != len(containers) {
		return containers // names are unique; never drop one
	}
	order, cycles := deps.Build(infos).Order()
	for _, cycle := range cycles {
		u.log.Warn("dependency cycle, updating these containers in name order", "containers", strings.Join(cycle, " -> "))
	}
	ordered := make([]container.Summary, 0, len(containers))
	for _, name := range order {
		ordered = append(ordered, byName[name])
	}
	return ordered
}

// carriedLastNotified returns the previous notification time only when it was
// for the same digest. A new digest whose delivery failed starts unnotified,
// so the next scan tries again instead of treating it as announced.
func carriedLastNotified(existing *store.NotifyState, digest string) time.Time {
	if existing != nil && existing.LastDigest == digest {
		return existing.LastNotified
	}
	return time.Time{}
}
