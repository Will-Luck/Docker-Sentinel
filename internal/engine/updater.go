package engine

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/Will-Luck/Docker-Sentinel/internal/clock"
	"github.com/Will-Luck/Docker-Sentinel/internal/config"
	"github.com/Will-Luck/Docker-Sentinel/internal/deps"
	"github.com/Will-Luck/Docker-Sentinel/internal/docker"
	"github.com/Will-Luck/Docker-Sentinel/internal/events"
	"github.com/Will-Luck/Docker-Sentinel/internal/hooks"
	"github.com/Will-Luck/Docker-Sentinel/internal/logging"
	"github.com/Will-Luck/Docker-Sentinel/internal/notify"
	"github.com/Will-Luck/Docker-Sentinel/internal/registry"
	"github.com/Will-Luck/Docker-Sentinel/internal/scanner"
	"github.com/Will-Luck/Docker-Sentinel/internal/store"
	"github.com/Will-Luck/Docker-Sentinel/internal/verify"
)

// ErrUpdateInProgress is returned when an update is attempted on a container
// that already has an update in progress.
var ErrUpdateInProgress = fmt.Errorf("update already in progress")

// ImageScanner scans a container image for vulnerabilities.
type ImageScanner interface {
	Scan(ctx context.Context, imageRef string) (*scanner.ScanResult, error)
}

// ImageVerifier verifies the signature of a container image.
type ImageVerifier interface {
	Verify(ctx context.Context, imageRef string) *verify.Result
}

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

// Updater performs container scanning and update operations.
type Updater struct {
	docker           docker.API
	checker          *registry.Checker
	store            *store.Store
	queue            *Queue
	cfg              *config.Config
	log              *logging.Logger
	clock            clock.Clock
	notifier         *notify.Multi
	events           *events.Bus
	settings         SettingsReader
	rateTracker      *registry.RateLimitTracker // optional: rate limit awareness
	rateSaver        func([]byte) error         // optional: persist rate limits after scan
	ghcrCache        *registry.GHCRCache        // optional: GHCR alternative detection cache
	ghcrSaver        func([]byte) error         // optional: persist GHCR cache after checks
	updating         sync.Map                   // map[string]*sync.Mutex — per-container update locks
	activeUpdates    atomic.Int32               // tracks number of in-progress updates for IsIdle()
	hooks            *hooks.Runner              // optional: lifecycle hook runner
	deps             *deps.Graph                // optional: dependency graph (rebuilt each scan)
	cluster          ClusterScanner             // optional: nil = single-host mode
	haDiscovery      *notify.HADiscovery        // optional: HA MQTT auto-discovery publisher
	portainer        PortainerScanner           // optional: nil = no Portainer integration
	imgScanner       ImageScanner               // optional: trivy vulnerability scanner
	imgVerifier      ImageVerifier              // optional: cosign signature verifier
	scanMode         scanner.ScanMode           // disabled/pre-update/post-update
	verifyMode       verify.Mode                // disabled/warn/enforce
	severityThresh   scanner.Severity           // block threshold for pre-update scans
	ghcrWg           sync.WaitGroup             // tracks background GHCR alternative checks
	ghcrRunning      atomic.Bool                // prevents concurrent GHCR checks
	ghcrCancel       context.CancelFunc         // cancels the running GHCR check on shutdown
	selfUpdateQueued atomic.Bool                // set when a self-update is queued during scan
	selfUpdateKey    atomic.Value               // stores queue key (string) of the self-update entry
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

// SetHADiscovery attaches an HA MQTT auto-discovery publisher.
func (u *Updater) SetHADiscovery(h *notify.HADiscovery) {
	u.haDiscovery = h
}

// SetPortainerScanner attaches a Portainer scanner for remote endpoint scanning.
// When nil (the default), Portainer scanning is skipped entirely.
func (u *Updater) SetPortainerScanner(ps PortainerScanner) {
	u.portainer = ps
}

// SetScanner attaches a vulnerability scanner (Trivy).
func (u *Updater) SetScanner(s ImageScanner) {
	u.imgScanner = s
}

// SetVerifier attaches a signature verifier (cosign).
func (u *Updater) SetVerifier(v ImageVerifier) {
	u.imgVerifier = v
}

// SetScanMode sets when vulnerability scanning runs.
func (u *Updater) SetScanMode(m scanner.ScanMode) {
	u.scanMode = m
}

// SetVerifyMode sets the signature verification behaviour.
func (u *Updater) SetVerifyMode(m verify.Mode) {
	u.verifyMode = m
}

// SetSeverityThreshold sets the minimum severity that blocks a pre-update scan.
func (u *Updater) SetSeverityThreshold(s scanner.Severity) {
	u.severityThresh = s
}

// Close cancels any running background work and waits for goroutines to finish.
func (u *Updater) Close() {
	if u.ghcrCancel != nil {
		u.ghcrCancel()
	}
	u.ghcrWg.Wait()
}

// tryLock attempts to acquire the per-container update lock.
// Returns false if the container already has an update in progress.
func (u *Updater) tryLock(name string) bool {
	mu := &sync.Mutex{}
	actual, _ := u.updating.LoadOrStore(name, mu)
	if actual.(*sync.Mutex).TryLock() {
		u.activeUpdates.Add(1)
		return true
	}
	return false
}

// unlock releases the per-container update lock and removes the entry
// from the map atomically to prevent stale mutex accumulation.
// LoadAndDelete is atomic — no window for another goroutine to LoadOrStore
// between our Unlock and Delete (which was the previous race condition).
func (u *Updater) unlock(name string) {
	if val, ok := u.updating.LoadAndDelete(name); ok {
		val.(*sync.Mutex).Unlock()
		u.activeUpdates.Add(-1)
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

// IsIdle returns true when no container updates are in progress.
func (u *Updater) IsIdle() bool {
	return u.activeUpdates.Load() == 0
}

// SelfUpdateQueued reports whether the last scan found a Sentinel self-update.
func (u *Updater) SelfUpdateQueued() bool {
	return u.selfUpdateQueued.Load()
}

// SelfUpdateKey returns the queue key of the queued self-update, if any.
func (u *Updater) SelfUpdateKey() string {
	v := u.selfUpdateKey.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}
